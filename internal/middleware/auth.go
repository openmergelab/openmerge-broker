package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/open-merge/broker/internal/model"
)

const SessionIDKey contextKey = "sessionID"

type tokenPayload struct {
	SID string `json:"sid"`
	Exp int64  `json:"exp"`
}

func Auth(secret string) func(http.Handler) http.Handler {
	secretBytes := []byte(secret)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Missing or invalid authorization header",
				})
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			parts := strings.SplitN(token, ".", 2)
			if len(parts) != 2 {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Malformed token",
				})
				return
			}

			payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Malformed token",
				})
				return
			}

			sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Malformed token",
				})
				return
			}

			mac := hmac.New(sha256.New, secretBytes)
			mac.Write(payloadBytes)
			expectedSig := mac.Sum(nil)
			if !hmac.Equal(sigBytes, expectedSig) {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Invalid token signature",
				})
				return
			}

			var payload tokenPayload
			if err := json.Unmarshal(payloadBytes, &payload); err != nil {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Malformed token",
				})
				return
			}

			if time.Unix(payload.Exp, 0).Before(time.Now()) {
				writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
					Error:   "unauthorized",
					Message: "Token expired",
				})
				return
			}

			ctx := context.WithValue(r.Context(), SessionIDKey, payload.SID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetSessionID(ctx context.Context) string {
	if sid, ok := ctx.Value(SessionIDKey).(string); ok {
		return sid
	}
	return ""
}

// IssueToken creates an HMAC-SHA256 signed token for the given session ID.
func IssueToken(secret string, sid string, ttl time.Duration) (string, error) {
	payload := tokenPayload{
		SID: sid,
		Exp: time.Now().Add(ttl).Unix(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return payloadB64 + "." + sigB64, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
