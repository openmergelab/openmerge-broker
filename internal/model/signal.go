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
	TelegramIDHash   string    `json:"telegramIdHash,omitempty"`
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
	TelegramIDHash  string   `json:"telegramIdHash"`
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
	TelegramIDHash  string `json:"telegramIdHash,omitempty"`
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
	TelegramID     string
	TelegramHandle string
}

// MatchWithUsers is used by the retry job to backfill missing introductions.
type MatchWithUsers struct {
	MatchID    string
	UserA      string
	UserB      string
	TelegramIDA string
	TelegramIDB string
	HandleA    string
	HandleB    string
}

type Match struct {
	ID               string    `json:"-"`
	UserA            string    `json:"-"`
	UserB            string    `json:"-"`
	IntroChannelID   string    `json:"-"`
	CreatedAt        time.Time `json:"-"`
}

type MatchResponse struct {
	MatchID        string `json:"matchId"`
	PartnerID      string `json:"partnerId"`
	IntroChannelID string `json:"introChannelId"`
	IntroducedAt   string `json:"introducedAt"`
}

type MatchesEnvelope struct {
	Matches      []MatchResponse `json:"matches"`
	SignalActive bool            `json:"signalActive"`
}
