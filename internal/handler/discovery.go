package handler

import (
	"net/http"
	"strconv"

	"github.com/open-merge/broker/internal/model"
	"github.com/open-merge/broker/internal/store"
	"github.com/uber/h3-go/v4"
)

type DiscoveryHandler struct {
	store store.SignalStore
}

func NewDiscoveryHandler(s store.SignalStore) *DiscoveryHandler {
	return &DiscoveryHandler{store: s}
}

func (h *DiscoveryHandler) HandleDiscoverSignals(w http.ResponseWriter, r *http.Request) {
	locationH3 := r.URL.Query().Get("locationH3")
	radiusStr := r.URL.Query().Get("radius")

	var invalid []string

	// V-010: validate locationH3 query param
	cell := h3.CellFromString(locationH3)
	if locationH3 == "" || !cell.IsValid() {
		invalid = append(invalid, "locationH3")
	}

	// V-011: validate radius (1-10)
	radius, err := strconv.Atoi(radiusStr)
	if err != nil || radius < 1 || radius > 10 {
		invalid = append(invalid, "radius")
	}

	if len(invalid) > 0 {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{
			Error:   "invalid_query",
			Message: "Invalid query parameters",
			Fields:  invalid,
		})
		return
	}

	// Compute H3 grid disk (all cells within radius)
	disk, err := h3.GridDisk(cell, radius)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Internal server error",
		})
		return
	}
	cellStrings := make([]string, len(disk))
	for i, c := range disk {
		cellStrings[i] = c.String()
	}

	results, err := h.store.FindSignalsByH3Cells(r.Context(), cellStrings)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
			Error:   "internal",
			Message: "Internal server error",
		})
		return
	}

	// Return empty array, not null
	if results == nil {
		results = []model.DiscoveryResult{}
	}

	writeJSON(w, http.StatusOK, results)
}
