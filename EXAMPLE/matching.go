package matching

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/merge-project/merge-broker/internal/db"
	"github.com/merge-project/merge-broker/internal/discord"
	"github.com/uber/h3-go/v4"
)

const (
	// H3 resolution 8 cells are ~0.7km². K-ring of 5 covers ~3km radius.
	h3KRingRadius = 5

	// introMessage is the only thing Merge ever posts in a match channel.
	// After this the channel belongs entirely to the two humans.
	// The bot does not post again. Ever.
	introMessage = "Your Merge agents think you should meet. 🌿\n\nThe rest is up to you."
)

// Job runs the matching logic on a schedule.
type Job struct {
	db     *db.DB
	logger *slog.Logger
}

func NewJob(database *db.DB, logger *slog.Logger) *Job {
	return &Job{
		db:     database,
		logger: logger,
	}
}

// Run finds compatible signal pairs and creates Discord introductions.
//
// Option 2 flow per pair:
//   1. Create a private channel in the Merge Discord server
//   2. Grant VIEW_CHANNEL to user A
//   3. Grant VIEW_CHANNEL to user B
//   4. Post one intro message
//   5. Record match in DB
//   6. Mark both signals as matched
//
// Neither user needs to be online. Runs entirely server-side.
func (j *Job) Run(ctx context.Context) error {
	j.logger.Info("matching job starting")

	signals, err := j.db.GetUnmatchedSignals(ctx)
	if err != nil {
		return err
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
		if err := j.introducePair(ctx, pair); err != nil {
			j.logger.Error("failed to introduce pair",
				"user_a_anon", pair.A.AnonymousID,
				"user_b_anon", pair.B.AnonymousID,
				"err", err,
			)
			continue
		}
		matched++
	}

	j.logger.Info("matching job complete", "introductions_made", matched)
	return nil
}

// Pair holds two compatible signals ready for introduction.
type Pair struct {
	A db.Signal
	B db.Signal
}

// findCompatiblePairs does coarse filtering on the full signal set.
//
// The broker deliberately does NOT rank pairs — it uses insertion order
// (created_at ASC from the DB query). No implicit preference for newer
// or older signals. No score. No weighting.
//
// Fine compatibility scoring happens on the skill side using the
// encrypted preference vectors — the broker cannot read those.
//
// Compatibility criteria applied here:
//   - Geographic proximity (H3 k-ring ≤ 5 steps ≈ 3km)
//   - Age range overlap (mutual)
//   - Seeking compatibility (mutual)
//   - Age verification (enforced upstream in the DB query)
func findCompatiblePairs(signals []db.Signal) []Pair {
	var pairs []Pair
	used := make(map[string]bool)

	for i := 0; i < len(signals); i++ {
		if used[signals[i].AnonymousID] {
			continue
		}
		for j := i + 1; j < len(signals); j++ {
			if used[signals[j].AnonymousID] {
				continue
			}

			a, b := signals[i], signals[j]

			if !geographicallyClose(a.LocationH3, b.LocationH3) {
				continue
			}
			if !ageCompatible(a, b) {
				continue
			}
			if !seekingCompatible(a.Seeking, b.Seeking) {
				continue
			}

			pairs = append(pairs, Pair{A: a, B: b})

			// Each signal produces at most one match per job run.
			// The next run will reconsider any still-unmatched signals.
			used[a.AnonymousID] = true
			used[b.AnonymousID] = true
			break
		}
	}

	return pairs
}

// introducePair implements Option 2:
//   - Private channel in the Merge Discord server
//   - Both users granted access
//   - One intro message posted
//   - Match recorded, signals marked
func (j *Job) introducePair(ctx context.Context, pair Pair) error {

	// Step 1 — Create a private channel in the Merge server.
	// Channel is invisible to @everyone by default.
	// Named after both handles e.g. "meet-maya-james"
	channelID, err := discord.CreateMatchChannel(
		ctx,
		pair.A.DiscordHandle,
		pair.B.DiscordHandle,
	)
	if err != nil {
		return fmt.Errorf("create channel: %w", err)
	}

	// Step 2 — Grant VIEW_CHANNEL + SEND_MESSAGES to user A.
	if err := discord.AddUserToChannel(ctx, channelID, pair.A.DiscordID); err != nil {
		return fmt.Errorf("add user A to channel: %w", err)
	}

	// Step 3 — Grant VIEW_CHANNEL + SEND_MESSAGES to user B.
	if err := discord.AddUserToChannel(ctx, channelID, pair.B.DiscordID); err != nil {
		return fmt.Errorf("add user B to channel: %w", err)
	}

	// Step 4 — Post the introduction message.
	// This is the only message the bot ever posts in this channel.
	// After this the channel belongs to the two humans.
	if err := discord.SendMessage(ctx, channelID, introMessage); err != nil {
		return fmt.Errorf("send intro message: %w", err)
	}

	// Step 5 — Record the match.
	_, err = j.db.CreateMatch(ctx,
		pair.A.AnonymousID,
		pair.B.AnonymousID,
		channelID,
	)
	if err != nil {
		return fmt.Errorf("record match: %w", err)
	}

	// Step 6 — Mark both signals as matched.
	// They will not appear in future job runs.
	if err := j.db.MarkSignalsMatched(ctx,
		pair.A.AnonymousID,
		pair.B.AnonymousID,
	); err != nil {
		return fmt.Errorf("mark signals matched: %w", err)
	}

	j.logger.Info("pair introduced",
		"channel_id", channelID,
		// Log anonymous IDs only — never Discord IDs or handles
		"user_a_anon", pair.A.AnonymousID,
		"user_b_anon", pair.B.AnonymousID,
	)

	return nil
}

// ─── Compatibility helpers ────────────────────────────────────────────────────

// geographicallyClose returns true if two H3 cells are within k-ring radius.
// H3 grid distance is O(1) — no trigonometry needed.
func geographicallyClose(h3A, h3B string) bool {
	cellA := h3.IndexFromString(h3A)
	cellB := h3.IndexFromString(h3B)
	if cellA == 0 || cellB == 0 {
		return false
	}
	dist, err := h3.GridDistance(cellA, cellB)
	if err != nil {
		return false
	}
	return dist <= h3KRingRadius
}

// ageCompatible returns true if both users fall within each other's ranges.
// Mutual — Maya must be within James's range AND James within Maya's.
func ageCompatible(a, b db.Signal) bool {
	aInBRange := a.Age >= b.AgeMin && a.Age <= b.AgeMax
	bInARange := b.Age >= a.AgeMin && b.Age <= a.AgeMax
	return aInBRange && bInARange
}

// seekingCompatible returns true if seeking preferences are mutually compatible.
func seekingCompatible(seekA, seekB string) bool {
	if seekA == "any" || seekB == "any" {
		return true
	}
	return seekA == seekB ||
		(seekA == "M" && seekB == "F") ||
		(seekA == "F" && seekB == "M") ||
		seekA == "NB" || seekB == "NB"
}

// haversineKm returns great-circle distance in km — kept for future use
// as a sanity check on H3 proximity results.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
