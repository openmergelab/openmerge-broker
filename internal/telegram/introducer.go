package telegram

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/open-merge/broker/internal/store"
)

// Introducer implements the matching.Introducer interface using Telegram DMs.
type Introducer struct {
	client *Client
	store  store.SignalStore
	logger *slog.Logger
}

// NewIntroducer creates a Telegram-backed introducer. Returns nil if client is nil.
func NewIntroducer(client *Client, s store.SignalStore, logger *slog.Logger) *Introducer {
	if client == nil {
		return nil
	}
	return &Introducer{client: client, store: s, logger: logger}
}

// Introduce sends DMs to both matched users with each other's intro card.
// It updates the match record to mark the introduction as delivered.
func (i *Introducer) Introduce(ctx context.Context, matchID string) error {
	telegramIDA, telegramIDB, handleA, handleB, err := i.store.GetMatchUsersTelegram(ctx, matchID)
	if err != nil {
		return fmt.Errorf("get match users: %w", err)
	}

	if telegramIDA == "" || telegramIDB == "" {
		i.logger.Info("skipping introduction — missing Telegram IDs",
			"match_id", matchID)
		return nil
	}

	// Message to user A about user B
	msgA := fmt.Sprintf(
		"🌿 *You've been matched!*\n\n"+
			"Meet @%s. You're both here because Merge found you compatible.\n\n"+
			"Send them a message — take it from here.",
		handleB,
	)

	// Message to user B about user A
	msgB := fmt.Sprintf(
		"🌿 *You've been matched!*\n\n"+
			"Meet @%s. You're both here because Merge found you compatible.\n\n"+
			"Send them a message — take it from here.",
		handleA,
	)

	if err := i.client.SendMessage(ctx, telegramIDA, msgA); err != nil {
		i.logger.Error("failed to DM user A", "match_id", matchID, "error", err)
	}

	if err := i.client.SendMessage(ctx, telegramIDB, msgB); err != nil {
		i.logger.Error("failed to DM user B", "match_id", matchID, "error", err)
	}

	if err := i.store.MarkMatchIntroduced(ctx, matchID); err != nil {
		return fmt.Errorf("mark match introduced: %w", err)
	}

	i.logger.Info("introduction complete",
		"match_id", matchID,
	)
	return nil
}
