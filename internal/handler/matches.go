package handler

import (
	"net/http"
	"time"

	"github.com/open-merge/broker/internal/middleware"
	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
)

type MatchHandler struct {
	store store.SignalStore
}

func NewMatchHandler(s store.SignalStore) *MatchHandler {
	return &MatchHandler{store: s}
}

func (h *MatchHandler) HandleGetMatches(w http.ResponseWriter, r *http.Request) {
	anonymousID := middleware.GetSessionID(r.Context())
	if anonymousID == "" {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
			Error:   "unauthorized",
			Message: "Missing session identity",
		})
		return
	}

	matches, err := h.store.GetMatchesForUser(r.Context(), anonymousID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Internal server error",
		})
		return
	}

	signalActive, _ := h.store.SignalActive(r.Context(), anonymousID)

	resp := make([]model.MatchResponse, 0, len(matches))
	for _, m := range matches {
		partner := m.UserB
		if m.UserB == anonymousID {
			partner = m.UserA
		}
		resp = append(resp, model.MatchResponse{
			MatchID:          m.ID,
			PartnerID:        partner,
			IntroChannelID: m.IntroChannelID,
			IntroducedAt:     m.CreatedAt.Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, model.MatchesEnvelope{
		Matches:      resp,
		SignalActive: signalActive,
	})
}
