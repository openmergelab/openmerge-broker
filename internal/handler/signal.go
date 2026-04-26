package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/open-merge/broker/internal/matching"
	"github.com/open-merge/broker/internal/middleware"
	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
)

type SignalHandler struct {
	store    store.SignalStore
	ttl      time.Duration
	matchJob *matching.Job
}

func NewSignalHandler(s store.SignalStore, ttl time.Duration, matchJob *matching.Job) *SignalHandler {
	return &SignalHandler{store: s, ttl: ttl, matchJob: matchJob}
}

func (h *SignalHandler) HandleUpsertSignal(w http.ResponseWriter, r *http.Request) {
	var req model.SignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "invalid_payload",
			Message: "Invalid JSON in request body",
		})
		return
	}

	if invalid := model.ValidateSignalRequest(&req); len(invalid) > 0 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "invalid_payload",
			Message: "Validation failed",
			Fields:  invalid,
		})
		return
	}

	// Decode encrypted vector from base64 string to bytes
	encVec, _ := store.DecodeEncryptedVector(req.EncryptedVector)

	now := time.Now().UTC()
	sig := &model.Signal{
		SignalID:        store.GenerateSignalID(),
		AnonymousID:     req.AnonymousID,
		LocationH3:      req.LocationH3,
		Gender:          req.Gender,
		Seeking:         req.Seeking,
		Age:             req.Age,
		AgeMin:          req.AgeRange.Min,
		AgeMax:          req.AgeRange.Max,
		PublicKey:       req.PublicKey,
		EncryptedVector: encVec,
		TelegramIDHash:  req.TelegramIDHash,
		PushToken:       req.PushToken,
		CreatedAt:       now,
		ExpiresAt:       now.Add(h.ttl),
	}

	if err := h.store.UpsertSignal(r.Context(), sig); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Internal server error",
		})
		return
	}

	// Real-time matching: find compatible partners immediately
	if h.matchJob != nil {
		go func() {
			if _, err := h.matchJob.MatchSingle(r.Context(), sig); err != nil {
				// logged inside MatchSingle; non-fatal
				_ = err
			}
		}()
	}

	writeJSON(w, http.StatusOK, model.SignalResponse{
		SignalID:  sig.SignalID,
		ExpiresAt: sig.ExpiresAt.Format(time.RFC3339),
	})
}

func (h *SignalHandler) HandleDeleteSignal(w http.ResponseWriter, r *http.Request) {
	sid := middleware.GetSessionID(r.Context())
	if sid == "" {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
			Error:   "unauthorized",
			Message: "Missing session",
		})
		return
	}

	if err := h.store.DeleteSignal(r.Context(), sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to remove signal",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

func (h *SignalHandler) HandleGetSignal(w http.ResponseWriter, r *http.Request) {
	signalID := chi.URLParam(r, "signalId")
	if signalID == "" {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "invalid_payload",
			Message: "signalId is required",
		})
		return
	}

	sig, err := h.store.GetSignalByID(r.Context(), signalID)
	if err != nil {
		var storeErr *store.StoreError
		if errors.As(err, &storeErr) && storeErr.Code == "not_found" {
			writeJSON(w, http.StatusNotFound, model.ErrorResponse{
				Error:   "not_found",
				Message: "Signal not found",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Internal server error",
		})
		return
	}

	writeJSON(w, http.StatusOK, sig)
}

func (h *SignalHandler) HandleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	sid := middleware.GetSessionID(r.Context())
	if sid == "" {
		writeJSON(w, http.StatusUnauthorized, model.ErrorResponse{
			Error:   "unauthorized",
			Message: "Missing session",
		})
		return
	}

	if err := h.store.DeleteUser(r.Context(), sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Failed to delete account",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
