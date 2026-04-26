package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-merge/broker/internal/middleware"
	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
	"github.com/open-merge/broker/internal/telegram"
)

const tokenTTL = 30 * 24 * time.Hour // 30 days

// pendingOAuth stores PKCE state and result for broker-mediated OAuth flows.
type pendingOAuth struct {
	codeVerifier string
	createdAt    time.Time
	// result is set by the callback handler when auth completes
	status string // "pending", "complete", "error"
	result map[string]string
}

type AuthHandler struct {
	store          store.SignalStore
	secret         string
	telegramClient *telegram.Client
	clientID       string
	clientSecret   string
	publicURL      string

	pendingMu sync.Mutex
	pending   map[string]*pendingOAuth
}

func NewAuthHandler(s store.SignalStore, secret string, tc *telegram.Client, clientID, clientSecret, publicURL string) *AuthHandler {
	return &AuthHandler{
		store:          s,
		secret:         secret,
		telegramClient: tc,
		clientID:       clientID,
		clientSecret:   clientSecret,
		publicURL:      publicURL,
		pending:        make(map[string]*pendingOAuth),
	}
}

type sessionRequest struct {
	AnonymousID    string `json:"anonymousId"`
	TelegramIDHash string `json:"telegramIdHash"`
}

type sessionResponse struct {
	Token       string `json:"token"`
	AnonymousID string `json:"anonymousId"`
	ExpiresIn   int    `json:"expiresIn"`
}

// HandleCreateSession exchanges anonymousId + telegramIdHash for a signed session token.
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

	if req.AnonymousID == "" || req.TelegramIDHash == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "anonymousId and telegramIdHash are required",
			Fields:  []string{"anonymousId", "telegramIdHash"},
		})
		return
	}

	if len(req.TelegramIDHash) != 64 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "telegramIdHash must be a 64-character hex SHA-256",
		})
		return
	}

	resolvedID, err := h.store.EnsureUser(r.Context(), req.AnonymousID, req.TelegramIDHash)
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

// --- Telegram OIDC Auth ------------------------------------------------------

const telegramTokenURL = "https://oauth.telegram.org/token"

type telegramOIDCRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

type telegramAuthResponse struct {
	Token          string `json:"token"`
	AnonymousID    string `json:"anonymousId"`
	ExpiresIn      int    `json:"expiresIn"`
	TelegramID     string `json:"telegramId"`
	TelegramHandle string `json:"telegramHandle"`
}

// HandleTelegramAuth exchanges an OAuth 2.0 authorization code for a broker session.
// The client sends the code + PKCE verifier; the broker exchanges it at Telegram's
// token endpoint and validates the returned JWT id_token.
// POST /auth/telegram
func (h *AuthHandler) HandleTelegramAuth(w http.ResponseWriter, r *http.Request) {
	if h.clientID == "" || h.clientSecret == "" {
		writeJSON(w, http.StatusServiceUnavailable, model.ErrorResponse{
			Error:   "not_configured",
			Message: "Telegram OIDC auth is not configured on this broker",
		})
		return
	}

	var req telegramOIDCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "Invalid request body",
		})
		return
	}

	if req.Code == "" || req.CodeVerifier == "" || req.RedirectURI == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "code, code_verifier, and redirect_uri are required",
		})
		return
	}

	// Exchange authorization code for tokens at Telegram
	tokenResp, err := h.exchangeCode(req.Code, req.CodeVerifier, req.RedirectURI)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, model.ErrorResponse{
			Error:   "token_exchange_failed",
			Message: "Failed to exchange code with Telegram: " + err.Error(),
		})
		return
	}

	// Decode the JWT id_token (signature trusted via TLS + client credentials)
	claims, err := decodeJWTPayload(tokenResp.IDToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, model.ErrorResponse{
			Error:   "invalid_id_token",
			Message: "Failed to decode Telegram id_token: " + err.Error(),
		})
		return
	}

	// Validate issuer and audience
	if iss, _ := claims["iss"].(string); iss != "https://oauth.telegram.org" {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
			Error:   "invalid_issuer",
			Message: "id_token issuer mismatch",
		})
		return
	}
	audStr := ""
	switch v := claims["aud"].(type) {
	case string:
		audStr = v
	case float64:
		audStr = strconv.FormatInt(int64(v), 10)
	}
	if audStr != h.clientID {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
			Error:   "invalid_audience",
			Message: "id_token audience mismatch",
		})
		return
	}

	// Extract Telegram user info from JWT claims
	telegramID := ""
	switch v := claims["id"].(type) {
	case float64:
		telegramID = strconv.FormatInt(int64(v), 10)
	case string:
		telegramID = v
	}
	if telegramID == "" {
		if sub, ok := claims["sub"].(string); ok {
			telegramID = sub
		}
	}
	telegramHandle, _ := claims["preferred_username"].(string)

	if telegramID == "" {
		writeJSON(w, http.StatusBadGateway, model.ErrorResponse{
			Error:   "missing_user_id",
			Message: "Telegram id_token does not contain a user ID",
		})
		return
	}

	// Create/resolve broker user
	telegramIDHash := fmt.Sprintf("%x", sha256.Sum256([]byte(telegramID)))
	candidateID := newUUID()
	resolvedID, err := h.store.EnsureUserFromTelegram(r.Context(), candidateID, telegramIDHash, telegramID, telegramHandle)
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

	if err := h.store.MarkAgeVerified(r.Context(), resolvedID, "telegram"); err != nil {
		_ = err
	}

	writeJSON(w, http.StatusOK, telegramAuthResponse{
		Token:          token,
		AnonymousID:    resolvedID,
		ExpiresIn:      int(tokenTTL.Seconds()),
		TelegramID:     telegramID,
		TelegramHandle: telegramHandle,
	})
}

// telegramTokenResponse represents the response from Telegram's token endpoint.
type telegramTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token"`
}

// exchangeCode exchanges an authorization code at Telegram's OIDC token endpoint.
func (h *AuthHandler) exchangeCode(code, codeVerifier, redirectURI string) (*telegramTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {h.clientID},
		"code_verifier": {codeVerifier},
	}

	req, err := http.NewRequest("POST", telegramTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	creds := base64.StdEncoding.EncodeToString([]byte(h.clientID + ":" + h.clientSecret))
	req.Header.Set("Authorization", "Basic "+creds)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp telegramTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if tokenResp.IDToken == "" {
		return nil, fmt.Errorf("no id_token in response: %s", string(body))
	}
	return &tokenResp, nil
}

// decodeJWTPayload base64-decodes the payload section of a JWT.
// Signature verification is not performed here; the token is trusted because
// it was received directly from Telegram's token endpoint over TLS with
// authenticated client credentials.
func decodeJWTPayload(idToken string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("expected 3 JWT parts, got %d", len(parts))
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}
	return claims, nil
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

// generatePKCE creates a code_verifier and S256 code_challenge for PKCE.
func generatePKCE() (verifier, challenge string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return
}

const telegramOIDCAuthURL = "https://oauth.telegram.org/auth"

// HandleTelegramStart initiates a broker-mediated OAuth flow.
// The CLI calls this to get an auth URL and state, then opens the browser.
// POST /auth/telegram/start
func (h *AuthHandler) HandleTelegramStart(w http.ResponseWriter, r *http.Request) {
	if h.publicURL == "" || h.clientID == "" {
		writeJSON(w, http.StatusServiceUnavailable, model.ErrorResponse{
			Error:   "not_configured",
			Message: "Broker OAuth not configured (BROKER_PUBLIC_URL and TELEGRAM_CLIENT_ID required)",
		})
		return
	}

	// Generate a random state and broker-side PKCE pair
	state := newUUID()
	codeVerifier, codeChallenge := generatePKCE()

	// Store pending auth state
	h.pendingMu.Lock()
	h.pending[state] = &pendingOAuth{
		codeVerifier: codeVerifier,
		createdAt:    time.Now(),
		status:       "pending",
	}
	h.pendingMu.Unlock()

	// Build Telegram OIDC authorization URL
	redirectURI := strings.TrimRight(h.publicURL, "/") + "/auth/telegram/callback"
	authURL := telegramOIDCAuthURL + "?" + url.Values{
		"client_id":             {h.clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile"},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	slog.Info("telegram oauth start", "state", state)
	writeJSON(w, http.StatusOK, map[string]string{
		"authUrl": authURL,
		"state":   state,
	})
}

// HandleTelegramCallback is the OAuth callback from Telegram.
// It exchanges the code for tokens, creates a broker session, and stores
// the result for the CLI to poll.
// GET /auth/telegram/callback?code=...&state=...
func (h *AuthHandler) HandleTelegramCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")

	// Look up pending state
	h.pendingMu.Lock()
	pending, ok := h.pending[state]
	h.pendingMu.Unlock()

	// Handle Telegram error response
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		if ok {
			h.pendingMu.Lock()
			pending.status = "error"
			pending.result = map[string]string{"error": errMsg}
			h.pendingMu.Unlock()
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "<html><body><h2>Login failed: %s</h2><p>You can close this tab.</p></body></html>", errMsg)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" || state == "" {
		http.Error(w, "code and state query parameters are required", http.StatusBadRequest)
		return
	}

	if !ok || time.Since(pending.createdAt) > 5*time.Minute {
		http.Error(w, "Invalid or expired OAuth state", http.StatusBadRequest)
		return
	}

	// Exchange authorization code at Telegram's token endpoint
	redirectURI := strings.TrimRight(h.publicURL, "/") + "/auth/telegram/callback"
	tokenResp, err := h.exchangeCode(code, pending.codeVerifier, redirectURI)
	if err != nil {
		slog.Error("telegram token exchange failed", "error", err)
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "token_exchange_failed"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Token exchange error. You can close this tab.</p></body></html>")
		return
	}

	// Decode and validate JWT id_token
	claims, err := decodeJWTPayload(tokenResp.IDToken)
	if err != nil {
		slog.Error("telegram id_token decode failed", "error", err)
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "invalid_id_token"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Invalid token. You can close this tab.</p></body></html>")
		return
	}

	if iss, _ := claims["iss"].(string); iss != "https://oauth.telegram.org" {
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "invalid_issuer"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Invalid issuer. You can close this tab.</p></body></html>")
		return
	}
	audStr := ""
	switch v := claims["aud"].(type) {
	case string:
		audStr = v
	case float64:
		audStr = strconv.FormatInt(int64(v), 10)
	}
	if audStr != h.clientID {
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "invalid_audience"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Invalid audience. You can close this tab.</p></body></html>")
		return
	}

	// Extract Telegram user info
	telegramID := ""
	switch v := claims["id"].(type) {
	case float64:
		telegramID = strconv.FormatInt(int64(v), 10)
	case string:
		telegramID = v
	}
	if telegramID == "" {
		if sub, ok := claims["sub"].(string); ok {
			telegramID = sub
		}
	}
	telegramHandle, _ := claims["preferred_username"].(string)

	if telegramID == "" {
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "missing_user_id"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>No user ID. You can close this tab.</p></body></html>")
		return
	}

	// Create/resolve broker user
	telegramIDHash := fmt.Sprintf("%x", sha256.Sum256([]byte(telegramID)))
	candidateID := newUUID()
	resolvedID, err := h.store.EnsureUserFromTelegram(r.Context(), candidateID, telegramIDHash, telegramID, telegramHandle)
	if err != nil {
		slog.Error("failed to create user from telegram callback", "error", err)
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "internal_error"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Internal error. You can close this tab.</p></body></html>")
		return
	}

	token, err := middleware.IssueToken(h.secret, resolvedID, tokenTTL)
	if err != nil {
		slog.Error("failed to issue token from telegram callback", "error", err)
		h.pendingMu.Lock()
		pending.status = "error"
		pending.result = map[string]string{"error": "internal_error"}
		h.pendingMu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Internal error. You can close this tab.</p></body></html>")
		return
	}

	if err := h.store.MarkAgeVerified(r.Context(), resolvedID, "telegram"); err != nil {
		_ = err
	}

	// Store result for polling
	h.pendingMu.Lock()
	pending.status = "complete"
	pending.result = map[string]string{
		"token":          token,
		"telegramId":     telegramID,
		"telegramHandle": telegramHandle,
		"anonymousId":    resolvedID,
	}
	h.pendingMu.Unlock()

	slog.Info("telegram oauth complete", "state", state, "telegramId", telegramID)
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "<html><body><h2>Login successful!</h2><p>You can close this tab and return to your terminal.</p></body></html>")
}

// HandleTelegramPoll lets the CLI poll for the auth result.
// GET /auth/telegram/poll?state=...
// Returns: {"status":"pending"} | {"status":"complete","token":...,"telegramId":...} | {"status":"error","error":...}
func (h *AuthHandler) HandleTelegramPoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "bad_request",
			Message: "state query parameter is required",
		})
		return
	}

	h.pendingMu.Lock()
	pending, ok := h.pending[state]
	if !ok {
		h.pendingMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{
			"status": "expired",
			"error":  "unknown or expired state",
		})
		return
	}

	if time.Since(pending.createdAt) > 5*time.Minute {
		delete(h.pending, state)
		h.pendingMu.Unlock()
		writeJSON(w, http.StatusGone, map[string]string{
			"status": "expired",
			"error":  "auth session expired",
		})
		return
	}

	status := pending.status
	result := pending.result

	// If complete or error, remove from map (one-time read)
	if status == "complete" || status == "error" {
		delete(h.pending, state)
	}
	h.pendingMu.Unlock()

	resp := map[string]string{"status": status}
	if result != nil {
		for k, v := range result {
			resp[k] = v
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// StartCleanup starts a goroutine that removes expired pending auth states.
func (h *AuthHandler) StartCleanup() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			h.pendingMu.Lock()
			for state, p := range h.pending {
				if time.Since(p.createdAt) > 10*time.Minute {
					delete(h.pending, state)
				}
			}
			h.pendingMu.Unlock()
		}
	}()
}
