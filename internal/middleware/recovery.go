package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/open-merge/broker/internal/model"
)

func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				slog.Error("panic recovered",
					"error", rvr,
					"stack", string(debug.Stack()),
					"request_id", GetRequestID(r.Context()),
				)
				writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{
					Error:   "internal",
					Message: "Internal server error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
