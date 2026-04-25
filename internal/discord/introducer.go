package discord

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/open-merge/broker/internal/store"
)

// Introducer implements the matching.Introducer interface using the Discord API.
type Introducer struct {
	client *Client
	store  store.SignalStore
	logger *slog.Logger
}

// NewIntroducer creates a Discord-backed introducer. Returns nil if client is nil.
func NewIntroducer(client *Client, s store.SignalStore, logger *slog.Logger) *Introducer {
	if client == nil {
		return nil
	}
	return &Introducer{client: client, store: s, logger: logger}
}

// Introduce creates a private channel, adds both users, and sends the intro message.
// It updates the match record with the channel ID on success.
func (i *Introducer) Introduce(ctx context.Context, matchID string) error {
	discordIDA, discordIDB, handleA, handleB, bothMembers, err := i.store.GetMatchUsersDiscord(ctx, matchID)
	if err != nil {
		return fmt.Errorf("get match users: %w", err)
	}

	if !bothMembers {
		i.logger.Info("skipping introduction — not both server members",
			"match_id", matchID)
		return nil
	}

	channelID, err := i.client.CreateMatchChannel(ctx, handleA, handleB)
	if err != nil {
		return fmt.Errorf("create channel: %w", err)
	}

	if err := i.client.AddUserToChannel(ctx, channelID, discordIDA); err != nil {
		return fmt.Errorf("add user A to channel: %w", err)
	}
	if err := i.client.AddUserToChannel(ctx, channelID, discordIDB); err != nil {
		return fmt.Errorf("add user B to channel: %w", err)
	}

	msg := fmt.Sprintf(
		"🌿 **You've been matched!**\n\n"+
			"<@%s> meet <@%s>. You're both here because Merge found you compatible.\n\n"+
			"Take it from here — this channel is just for you two.",
		discordIDA, discordIDB,
	)
	if err := i.client.SendMessage(ctx, channelID, msg); err != nil {
		// Channel exists, users added — log but don't fail the match
		i.logger.Error("failed to send intro message", "match_id", matchID, "error", err)
	}

	if err := i.store.UpdateMatchChannel(ctx, matchID, channelID); err != nil {
		return fmt.Errorf("update match channel: %w", err)
	}

	i.logger.Info("introduction complete",
		"match_id", matchID,
		"channel_id", channelID,
	)
	return nil
}
