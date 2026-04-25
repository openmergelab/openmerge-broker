package model

import "time"

type Signal struct {
	SignalID         string    `json:"signalId"`
	AnonymousID      string    `json:"anonymousId"`
	LocationH3       string    `json:"locationH3"`
	Gender           string    `json:"gender"`
	Seeking          string    `json:"seeking"`
	Age              int       `json:"age"`
	AgeMin           int       `json:"ageMin"`
	AgeMax           int       `json:"ageMax"`
	PublicKey        string    `json:"publicKey"`
	EncryptedVector  []byte    `json:"encryptedVector,omitempty"`
	DiscordIDHash    string    `json:"discordIdHash,omitempty"`
	PushToken        *string   `json:"pushToken,omitempty"`
	Matched          bool      `json:"matched"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type AgeRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type SignalRequest struct {
	AnonymousID     string   `json:"anonymousId"`
	LocationH3      string   `json:"locationH3"`
	Gender          string   `json:"gender"`
	Seeking         string   `json:"seeking"`
	Age             int      `json:"age"`
	AgeRange        AgeRange `json:"ageRange"`
	PublicKey       string   `json:"publicKey"`
	EncryptedVector string   `json:"encryptedVector"`
	DiscordIDHash   string   `json:"discordIdHash"`
	PushToken       *string  `json:"pushToken,omitempty"`
}

type SignalResponse struct {
	SignalID  string `json:"signalId"`
	ExpiresAt string `json:"expiresAt"`
}

type ErrorResponse struct {
	Error   string   `json:"error"`
	Message string   `json:"message"`
	Fields  []string `json:"fields,omitempty"`
}

type DiscoveryResult struct {
	AnonymousID     string `json:"anonymousId"`
	LocationH3      string `json:"locationH3"`
	Seeking         string `json:"seeking"`
	AgeMin          int    `json:"ageMin"`
	AgeMax          int    `json:"ageMax"`
	PublicKey       string `json:"publicKey"`
	EncryptedVector []byte `json:"encryptedVector,omitempty"`
	DiscordIDHash   string `json:"discordIdHash,omitempty"`
}

// UnmatchedSignal is the projection used by the matching job.
// Joined from agent_signals + users.
type UnmatchedSignal struct {
	AnonymousID   string
	LocationH3    string
	Gender        string
	Seeking       string
	Age           int
	AgeMin        int
	AgeMax        int
	DiscordID     string
	DiscordHandle string
}

// MatchWithUsers is used by the retry job to backfill missing Discord channels.
type MatchWithUsers struct {
	MatchID    string
	UserA      string
	UserB      string
	DiscordIDA string
	DiscordIDB string
	HandleA    string
	HandleB    string
}

type Match struct {
	ID               string    `json:"-"`
	UserA            string    `json:"-"`
	UserB            string    `json:"-"`
	DiscordChannelID string    `json:"-"`
	CreatedAt        time.Time `json:"-"`
}

type MatchResponse struct {
	MatchID          string `json:"matchId"`
	PartnerID        string `json:"partnerId"`
	DiscordChannelID string `json:"discordChannelId"`
	IntroducedAt     string `json:"introducedAt"`
}

type MatchesEnvelope struct {
	Matches      []MatchResponse `json:"matches"`
	SignalActive bool            `json:"signalActive"`
}
