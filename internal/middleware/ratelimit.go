package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-merge/broker/internal/model"
)

type RateLimiter struct {
	limit    int
	counters sync.Map // key: "sessionID:minuteBucket" → *int64
}

func NewRateLimiter(limit int) *RateLimiter {
	rl := &RateLimiter{limit: limit}
	// Lazy cleanup: remove expired buckets every 2 minutes
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().UTC().Unix()/60 - 2
			rl.counters.Range(func(key, _ any) bool {
				k := key.(string)
				// Extract the minute bucket from the end of the key
				var bucket int64
				if _, err := fmt.Sscanf(k[len(k)-20:], "%d", &bucket); err == nil {
					if bucket < cutoff {
						rl.counters.Delete(key)
					}
				}
				return true
			})
		}
	}()
	return rl
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := GetSessionID(r.Context())
		if sid == "" {
			next.ServeHTTP(w, r)
			return
		}

		window := time.Now().UTC().Unix() / 60
		key := fmt.Sprintf("%s:%d", sid, window)

		counter, _ := rl.counters.LoadOrStore(key, new(int64))
		count := atomic.AddInt64(counter.(*int64), 1)

		if count > int64(rl.limit) {
			secondsLeft := 60 - (time.Now().UTC().Unix() % 60)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", secondsLeft))
			writeJSON(w, http.StatusTooManyRequests, model.ErrorResponse{
				Error:   "rate_limited",
				Message: "Rate limit exceeded",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
