package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/open-merge/broker/internal/discord"
	"github.com/open-merge/broker/internal/middleware"
	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
)

const tokenTTL = 30 * 24 * time.Hour // 30 days

type AuthHandler struct {
	store           store.SignalStore
	secret          string
	discordClient   *discord.Client
	discordClientID string
	discordSecret   string
}

func NewAuthHandler(s store.SignalStore, secret string, dc *discord.Client, discordClientID, discordSecret string) *AuthHandler {
	return &AuthHandler{
		store:           s,
		secret:          secret,
		discordClient:   dc,
		discordClientID: discordClientID,
		discordSecret:   discordSecret,
	}
}

type sessionRequest struct {
	AnonymousID   string `json:"anonymousId"`
	DiscordIDHash string `json:"discordIdHash"`
}

type sessionResponse struct {
	Token       string `json:"token"`
	AnonymousID string `json:"anonymousId"`
	ExpiresIn   int    `json:"expiresIn"`
}

// HandleCreateSession exchanges anonymousId + discordIdHash for a signed session token.
// POST /auth/session
func (h *AuthHandler) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "Invalid request body",
		})
		return
	}

	if req.AnonymousID == "" || req.DiscordIDHash == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "anonymousId and discordIdHash are required",
			Fields:  []string{"anonymousId", "discordIdHash"},
		})
		return
	}

	if len(req.DiscordIDHash) != 64 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "discordIdHash must be a 64-character hex SHA-256",
		})
		return
	}

	resolvedID, err := h.store.EnsureUser(r.Context(), req.AnonymousID, req.DiscordIDHash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to create session",
		})
		return
	}

	token, err := middleware.IssueToken(h.secret, resolvedID, tokenTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to issue token",
		})
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{
		Token:       token,
		AnonymousID: resolvedID,
		ExpiresIn:   int(tokenTTL.Seconds()),
	})
}

// --- Discord OAuth -----------------------------------------------------------

type discordAuthRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirectUri"`
}

type discordAuthResponse struct {
	Token         string `json:"token"`
	AnonymousID   string `json:"anonymousId"`
	ExpiresIn     int    `json:"expiresIn"`
	DiscordID     string `json:"discordId"`
	DiscordHandle string `json:"discordHandle"`
	ServerInvite  string `json:"serverInvite,omitempty"`
}

// HandleDiscordAuth exchanges a Discord authorization code for user identity
// and a broker session token. The client never sees the client_secret.
// POST /auth/discord
func (h *AuthHandler) HandleDiscordAuth(w http.ResponseWriter, r *http.Request) {
	if h.discordClientID == "" || h.discordSecret == "" {
		writeJSON(w, http.StatusServiceUnavailable, model.ErrorResponse{
			Error:   "not_configured",
			Message: "Discord OAuth is not configured on this broker",
		})
		return
	}

	var req discordAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "Invalid request body",
		})
		return
	}

	if req.Code == "" || req.RedirectURI == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "code and redirectUri are required",
		})
		return
	}

	// 1. Exchange code for access token with Discord
	accessToken, err := h.exchangeDiscordCode(req.Code, req.RedirectURI)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, model.ErrorResponse{
			Error:   "discord_error",
			Message: "Failed to exchange authorization code",
		})
		return
	}

	// 2. Fetch Discord user identity
	discordID, discordHandle, err := h.fetchDiscordUser(accessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, model.ErrorResponse{
			Error:   "discord_error",
			Message: "Failed to fetch Discord user",
		})
		return
	}

	// 3. Create/resolve broker user
	discordIDHash := fmt.Sprintf("%x", sha256.Sum256([]byte(discordID)))
	candidateID := newUUID()
	resolvedID, err := h.store.EnsureUserFromDiscord(r.Context(), candidateID, discordIDHash, discordID, discordHandle)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to create session",
		})
		return
	}

	// 4. Issue session token
	token, err := middleware.IssueToken(h.secret, resolvedID, tokenTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to issue token",
		})
		return
	}

	resp := discordAuthResponse{
		Token:         token,
		AnonymousID:   resolvedID,
		ExpiresIn:     int(tokenTTL.Seconds()),
		DiscordID:     discordID,
		DiscordHandle: discordHandle,
	}

	// 5. Generate a server invite link for the user to join voluntarily
	if h.discordClient != nil {
		inviteURL, inviteErr := h.discordClient.GenerateInvite(r.Context())
		if inviteErr != nil {
			slog.Error("discord GenerateInvite failed", "error", inviteErr)
		} else {
			resp.ServerInvite = inviteURL
		}
	}

	// 6. Mark age verified (Discord OAuth gate implies age verification)
	if err := h.store.MarkAgeVerified(r.Context(), resolvedID, "discord"); err != nil {
		slog.Error("failed to mark age verified", "anonymous_id", resolvedID, "error", err)
	}

	writeJSON(w, http.StatusOK, resp)
}

const discordTokenURL = "https://discord.com/api/oauth2/token"
const discordUserURL = "https://discord.com/api/v10/users/@me"

// exchangeDiscordCode exchanges an authorization code for an access token.
func (h *AuthHandler) exchangeDiscordCode(code, redirectURI string) (string, error) {
	data := url.Values{
		"client_id":     {h.discordClientID},
		"client_secret": {h.discordSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(discordTokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token exchange: status %d: %s", resp.StatusCode, body)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("discord returned empty access_token")
	}
	return tokenResp.AccessToken, nil
}

// fetchDiscordUser retrieves the Discord user's ID and username.
func (h *AuthHandler) fetchDiscordUser(accessToken string) (id, username string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", discordUserURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("user request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("user fetch: status %d: %s", resp.StatusCode, body)
	}

	var user struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", "", fmt.Errorf("decode user: %w", err)
	}
	if user.ID == "" {
		return "", "", fmt.Errorf("discord returned empty user id")
	}
	return user.ID, user.Username, nil
}

// newUUID generates a UUID v4 string using crypto/rand.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
