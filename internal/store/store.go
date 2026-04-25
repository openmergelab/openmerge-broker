package store

import (
	"context"

	"github.com/open-merge/broker/internal/model"
)

type SignalStore interface {
	UpsertSignal(ctx context.Context, signal *model.Signal) error
	GetSignalByID(ctx context.Context, signalID string) (*model.Signal, error)
	GetSignalByAnonID(ctx context.Context, anonymousID string) (*model.Signal, error)
	DeleteSignal(ctx context.Context, anonymousID string) error
	FindSignalsByH3Cells(ctx context.Context, cells []string) ([]model.DiscoveryResult, error)
	CleanExpired(ctx context.Context) (int64, error)
	GetMatchesForUser(ctx context.Context, anonymousID string) ([]model.Match, error)
	SignalActive(ctx context.Context, anonymousID string) (bool, error)
	EnsureUser(ctx context.Context, anonymousID string, discordIDHash string) (resolvedID string, err error)
	EnsureUserFromDiscord(ctx context.Context, anonymousID, discordIDHash, discordID, discordHandle string) (resolvedID string, err error)
	GetUnmatchedSignals(ctx context.Context) ([]model.UnmatchedSignal, error)
	FindCandidatesForSignal(ctx context.Context, signal *model.Signal, h3Cells []string) ([]model.UnmatchedSignal, error)
	CreateMatchIfNotExists(ctx context.Context, userA, userB, discordChannelID string) (matchID string, created bool, err error)
	MarkSignalsMatched(ctx context.Context, idA, idB string) error
	CreateMatch(ctx context.Context, userA, userB, discordChannelID string) (matchID string, err error)
	CleanExpiredVerifications(ctx context.Context) error
	GetMatchUsersDiscord(ctx context.Context, matchID string) (discordIDA, discordIDB, handleA, handleB string, bothMembers bool, err error)
	UpdateMatchChannel(ctx context.Context, matchID, channelID string) error
	GetMatchesMissingChannel(ctx context.Context) ([]model.MatchWithUsers, error)
	SetServerMember(ctx context.Context, discordID string, handle string) error
	MarkAgeVerified(ctx context.Context, anonymousID, provider string) error
	DeleteUser(ctx context.Context, anonymousID string) error
	Ping(ctx context.Context) error
}

var ErrConflict = &StoreError{Code: "conflict", Message: "Signal already exists for this anonymousId"}
var ErrNotFound = &StoreError{Code: "not_found", Message: "Signal not found"}

type StoreError struct {
	Code    string
	Message string
}

func (e *StoreError) Error() string {
	return e.Message
}
