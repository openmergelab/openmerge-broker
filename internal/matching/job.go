package matching

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
	"github.com/uber/h3-go/v4"
)

const h3KRingRadius = 5

// Introducer sends Telegram DMs to introduce matched users.
// Nil means introductions are disabled (no bot token configured).
type Introducer interface {
	Introduce(ctx context.Context, matchID string) error
}

// Job runs the matching logic on a schedule.
type Job struct {
	store      store.SignalStore
	logger     *slog.Logger
	introducer Introducer
}

func NewJob(s store.SignalStore, logger *slog.Logger, introducer Introducer) *Job {
	return &Job{store: s, logger: logger, introducer: introducer}
}

// introduceAsync fires off the Telegram introduction in a background goroutine.
func (j *Job) introduceAsync(matchID string) {
	if j.introducer == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := j.introducer.Introduce(ctx, matchID); err != nil {
			j.logger.Error("introduction failed", "match_id", matchID, "error", err)
		}
	}()
}

// Run finds compatible signal pairs using the batch N² scan and records matches.
// Kept as a sweep to catch edge cases missed by real-time matching.
func (j *Job) Run(ctx context.Context) error {
	j.logger.Info("matching job starting")

	signals, err := j.store.GetUnmatchedSignals(ctx)
	if err != nil {
		return fmt.Errorf("get unmatched signals: %w", err)
	}

	j.logger.Info("loaded unmatched signals", "count", len(signals))

	if len(signals) < 2 {
		j.logger.Info("not enough signals to match")
		return nil
	}

	pairs := findCompatiblePairs(signals)
	j.logger.Info("found compatible pairs", "count", len(pairs))

	matched := 0
	for _, pair := range pairs {
		matchID, created, err := j.store.CreateMatchIfNotExists(ctx, pair.A.AnonymousID, pair.B.AnonymousID, "")
		if err != nil {
			j.logger.Error("failed to create match",
				"user_a", pair.A.AnonymousID,
				"user_b", pair.B.AnonymousID,
				"error", err,
			)
			continue
		}
		if !created {
			continue
		}

		if err := j.store.MarkSignalsMatched(ctx, pair.A.AnonymousID, pair.B.AnonymousID); err != nil {
			j.logger.Error("failed to mark signals matched",
				"match_id", matchID,
				"error", err,
			)
			continue
		}

		j.logger.Info("pair matched",
			"match_id", matchID,
			"user_a", pair.A.AnonymousID,
			"user_b", pair.B.AnonymousID,
		)
		j.introduceAsync(matchID)
		matched++
	}

	j.logger.Info("matching job complete", "introductions_made", matched)
	return nil
}

// MatchSingle performs real-time matching for a single signal on upload.
// Uses DB-side filtering with H3 k-ring, seeking, and age checks.
// Designed to be called from the signal upsert handler for instant matches.
func (j *Job) MatchSingle(ctx context.Context, signal *model.Signal) (int, error) {
	cell := h3.CellFromString(signal.LocationH3)
	if !cell.IsValid() {
		j.logger.Warn("invalid H3 cell for real-time matching", "cell", signal.LocationH3)
		return 0, nil
	}

	disk, _ := h3.GridDisk(cell, h3KRingRadius)
	h3Cells := make([]string, len(disk))
	for i, c := range disk {
		h3Cells[i] = c.String()
	}

	candidates, err := j.store.FindCandidatesForSignal(ctx, signal, h3Cells)
	if err != nil {
		return 0, fmt.Errorf("find candidates: %w", err)
	}

	j.logger.Info("real-time match candidates", "signal", signal.AnonymousID, "candidates", len(candidates))

	matched := 0
	for _, c := range candidates {
		matchID, created, err := j.store.CreateMatchIfNotExists(ctx, signal.AnonymousID, c.AnonymousID, "")
		if err != nil {
			j.logger.Error("failed to create match",
				"user_a", signal.AnonymousID,
				"user_b", c.AnonymousID,
				"error", err,
			)
			continue
		}
		if !created {
			continue // pair already matched previously
		}

		j.logger.Info("real-time match",
			"match_id", matchID,
			"user_a", signal.AnonymousID,
			"user_b", c.AnonymousID,
		)
		j.introduceAsync(matchID)
		matched++
	}

	return matched, nil
}

// Pair holds two compatible signals ready for matching.
type Pair struct {
	A model.UnmatchedSignal
	B model.UnmatchedSignal
}

// RetryMissingIntros finds matches that haven't been introduced yet
// (both users now have Telegram IDs) and retries the introduction.
func (j *Job) RetryMissingIntros(ctx context.Context) error {
	if j.introducer == nil {
		return nil
	}

	matches, err := j.store.GetMatchesMissingIntro(ctx)
	if err != nil {
		return fmt.Errorf("get matches missing intro: %w", err)
	}

	for _, m := range matches {
		if err := j.introducer.Introduce(ctx, m.MatchID); err != nil {
			j.logger.Error("retry introduction failed",
				"match_id", m.MatchID,
				"error", err,
			)
			continue
		}
		j.logger.Info("retry introduction succeeded", "match_id", m.MatchID)
	}
	return nil
}

func findCompatiblePairs(signals []model.UnmatchedSignal) []Pair {
	var pairs []Pair
	type pairKey struct{ a, b string }
	seen := make(map[pairKey]bool)

	for i := 0; i < len(signals); i++ {
		for j := i + 1; j < len(signals); j++ {
			a, b := signals[i], signals[j]

			if !geographicallyClose(a.LocationH3, b.LocationH3) {
				continue
			}
			if !ageCompatible(a, b) {
				continue
			}
			if !seekingCompatible(a, b) {
				continue
			}

			key := pairKey{a.AnonymousID, b.AnonymousID}
			if seen[key] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, Pair{A: a, B: b})
		}
	}

	return pairs
}

// geographicallyClose returns true if two H3 cells are within k-ring radius.
func geographicallyClose(h3A, h3B string) bool {
	cellA := h3.CellFromString(h3A)
	cellB := h3.CellFromString(h3B)
	if !cellA.IsValid() || !cellB.IsValid() {
		return false
	}
	dist, err := h3.GridDistance(cellA, cellB)
	if err != nil {
		return false
	}
	return dist <= h3KRingRadius
}

// ageCompatible returns true if each user's actual age falls within the
// other's preferred range. Checks actual age, not just range overlap.
func ageCompatible(a, b model.UnmatchedSignal) bool {
	aInBRange := a.Age >= b.AgeMin && a.Age <= b.AgeMax
	bInARange := b.Age >= a.AgeMin && b.Age <= a.AgeMax
	return aInBRange && bInARange
}

// seekingCompatible returns true if seeking preferences are mutually compatible.
// seekingCompatible returns true if each user's gender matches what the other
// is seeking. "any" seeking matches all genders; NB gender matches any seeking.
func seekingCompatible(a, b model.UnmatchedSignal) bool {
	aMatchesBSeeking := b.Seeking == "any" || a.Gender == b.Seeking
	bMatchesASeeking := a.Seeking == "any" || b.Gender == a.Seeking
	return aMatchesBSeeking && bMatchesASeeking
}
