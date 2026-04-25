package handler

import (
	"net/http"

	"github.com/open-merge/broker/internal/store"
)

type HealthHandler struct {
	store store.SignalStore
}

func NewHealthHandler(s store.SignalStore) *HealthHandler {
	return &HealthHandler{store: s}
}

func (h *HealthHandler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *HealthHandler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "not_ready",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
