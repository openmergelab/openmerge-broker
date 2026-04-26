package broker

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/merge-project/merge-broker/internal/auth"
	"github.com/merge-project/merge-broker/internal/db"
	"github.com/merge-project/merge-broker/internal/discord"
	"github.com/merge-project/merge-broker/internal/verify"
)

// Handlers holds dependencies for all HTTP handlers.
type Handlers struct {
	db *db.DB
}

func NewHandlers(database *db.DB) *Handlers {
	return &Handlers{db: database}
}

// respond writes JSON to the response writer.
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func respondErr(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}

// ─── POST /auth/discord ───────────────────────────────────────────────────────

type authDiscordRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// AuthDiscord exchanges a Discord OAuth code for a session token.
// Creates or updates the user record.
// Returns a JWT the skill stores and uses for all subsequent requests.
func (h *Handlers) AuthDiscord(w http.ResponseWriter, r *http.Request) {
	var req authDiscordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	discordUser, err := discord.ExchangeCode(r.Context(), req.Code, req.RedirectURI)
	if err != nil {
		respondErr(w, http.StatusBadGateway, "discord auth failed")
		return
	}

	// Hash the Discord ID — broker stores the hash, not the raw ID
	// We still need the raw ID to create DM channels
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(discordUser.ID)))

	user, err := h.db.UpsertUser(r.Context(), hash, discordUser.ID, discordUser.Username)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}

	token, err := auth.Issue(user.AnonymousID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "token error")
		return
	}

	// Generate a single-use server invite if user is not yet a member
	var serverInvite string
	if !user.ServerMember {
		invite, err := discord.GenerateInvite(r.Context())
		if err == nil {
			serverInvite = invite
		}
	}

	respond(w, http.StatusOK, map[string]interface{}{
		"token":         token,
		"anonymous_id":  user.AnonymousID,
		"server_invite": serverInvite, // empty string if already a member
	})
}

// ─── POST /verify/age/start ───────────────────────────────────────────────────

// VerifyAgeStart initiates a Stripe Identity verification session.
// The skill opens the verification URL on the user's device.
func (h *Handlers) VerifyAgeStart(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	verifyURL, sessionID, err := verify.StartSession(r.Context(), claims.AnonymousID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "verification start failed")
		return
	}

	if err := h.db.CreateVerificationSession(r.Context(), sessionID, claims.AnonymousID); err != nil {
		respondErr(w, http.StatusInternalServerError, "session store failed")
		return
	}

	respond(w, http.StatusOK, map[string]string{
		"verification_url": verifyURL,
		"session_id":       sessionID,
	})
}

// ─── GET /verify/age/status/:sessionId ───────────────────────────────────────

// VerifyAgeStatus polls a verification session.
// Called by the skill while the user completes ID verification.
func (h *Handlers) VerifyAgeStatus(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	sessionID := r.PathValue("sessionId")
	if sessionID == "" {
		respondErr(w, http.StatusBadRequest, "missing session id")
		return
	}

	// Confirm session belongs to this user
	storedAnonID, err := h.db.GetVerificationSession(r.Context(), sessionID)
	if err != nil || storedAnonID != claims.AnonymousID {
		respondErr(w, http.StatusNotFound, "session not found")
		return
	}

	verified, err := verify.CheckSession(r.Context(), sessionID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "verification check failed")
		return
	}

	if verified {
		// Mark user as age-verified in the database
		if err := h.db.MarkAgeVerified(r.Context(), claims.AnonymousID, "stripe"); err != nil {
			respondErr(w, http.StatusInternalServerError, "database error")
			return
		}
		// Clean up the session record
		_ = h.db.DeleteVerificationSession(r.Context(), sessionID)
	}

	respond(w, http.StatusOK, map[string]bool{"verified": verified})
}

// ─── POST /signal ─────────────────────────────────────────────────────────────

type signalRequest struct {
	AnonymousID      string  `json:"anonymousId"`
	LocationH3       string  `json:"locationH3"`
	Seeking          string  `json:"seeking"`
	AgeMin           int     `json:"ageMin"`
	AgeMax           int     `json:"ageMax"`
	PreferenceVector []byte  `json:"preferenceVector"` // encrypted — broker cannot read
	PublicKey        string  `json:"publicKey"`
	DiscordIDHash    string  `json:"discordIdHash"`
	PushToken        *string `json:"pushToken"`
}

// UpsertSignal parks an availability signal from the skill.
// The preference vector arrives already encrypted — the broker never
// has the key to decrypt it.
func (h *Handlers) UpsertSignal(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req signalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate seeking value
	validSeeking := map[string]bool{"M": true, "F": true, "NB": true, "any": true}
	if !validSeeking[req.Seeking] {
		respondErr(w, http.StatusBadRequest, "invalid seeking value")
		return
	}

	if req.AgeMin < 18 || req.AgeMax > 120 || req.AgeMin > req.AgeMax {
		respondErr(w, http.StatusBadRequest, "invalid age range")
		return
	}

	// Confirm user is age-verified before accepting signal
	user, err := h.db.GetUserByAnonymousID(r.Context(), claims.AnonymousID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}
	if !user.AgeVerified {
		respondErr(w, http.StatusForbidden, "age verification required")
		return
	}

	signal, err := h.db.UpsertSignal(r.Context(),
		claims.AnonymousID,
		req.LocationH3,
		req.Seeking,
		req.AgeMin,
		req.AgeMax,
		req.PreferenceVector,
		req.PublicKey,
		req.PushToken,
	)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}

	respond(w, http.StatusOK, map[string]string{
		"signal_id":  signal.AnonymousID,
		"expires_at": signal.ExpiresAt.Format("2006-01-02T15:04:05Z"),
	})
}

// ─── DELETE /signal ───────────────────────────────────────────────────────────

// DeleteSignal removes the user's signal immediately.
// Called when the user goes offline or pauses matching.
func (h *Handlers) DeleteSignal(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.db.DeleteSignal(r.Context(), claims.AnonymousID); err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}

	respond(w, http.StatusOK, map[string]bool{"removed": true})
}

// ─── GET /matches ─────────────────────────────────────────────────────────────

// GetMatches returns all Discord introductions made for this user.
// Does not expose any information about the matched person —
// just the Discord channel ID where they can find the introduction.
func (h *Handlers) GetMatches(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	matches, err := h.db.GetMatchesForUser(r.Context(), claims.AnonymousID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}

	signalActive, _ := h.db.SignalActive(r.Context(), claims.AnonymousID)

	type matchResponse struct {
		MatchID          string `json:"matchId"`
		DiscordChannelID string `json:"discordChannelId"`
		IntroducedAt     string `json:"introducedAt"`
	}

	resp := make([]matchResponse, 0, len(matches))
	for _, m := range matches {
		resp = append(resp, matchResponse{
			MatchID:          m.ID,
			DiscordChannelID: m.DiscordChannelID,
			IntroducedAt:     m.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	respond(w, http.StatusOK, map[string]any{
		"matches":       resp,
		"signal_active": signalActive,
	})
}

// ─── DELETE /account ─────────────────────────────────────────────────────────

// DeleteAccount removes all broker records for this user.
// The user's local profile and Discord conversations are unaffected —
// the broker never had them to delete.
func (h *Handlers) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// CASCADE on users table handles signals deletion
	if err := h.db.DeleteUser(r.Context(), claims.AnonymousID); err != nil {
		respondErr(w, http.StatusInternalServerError, "database error")
		return
	}

	respond(w, http.StatusOK, map[string]bool{"deleted": true})
}

// ─── GET /health ──────────────────────────────────────────────────────────────

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}
