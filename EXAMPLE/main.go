package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/merge-project/merge-broker/internal/auth"
	"github.com/merge-project/merge-broker/internal/broker"
	"github.com/merge-project/merge-broker/internal/db"
	"github.com/merge-project/merge-broker/internal/discord"
	"github.com/merge-project/merge-broker/internal/matching"
)

func main() {
	// Load .env in development
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM,
	)
	defer stop()

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.New(ctx)
	if err != nil {
		logger.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	logger.Info("database connected")

	// ── Discord client ────────────────────────────────────────────────────────
	discordClient := &discord.Client{}

	// ── Handlers ──────────────────────────────────────────────────────────────
	handlers := broker.NewHandlers(database)

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Structured request logging — never logs request bodies
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				// Never log: Authorization header, request body, query params
			)
		})
	})

	// Public routes
	r.Get("/health", handlers.Health)
	r.Post("/auth/discord", handlers.AuthDiscord)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)

		r.Post("/verify/age/start", handlers.VerifyAgeStart)
		r.Get("/verify/age/status/{sessionId}", handlers.VerifyAgeStatus)

		r.Post("/signal", handlers.UpsertSignal)
		r.Delete("/signal", handlers.DeleteSignal)

		r.Get("/matches", handlers.GetMatches)

		r.Delete("/account", handlers.DeleteAccount)
	})

	// ── Matching scheduler ────────────────────────────────────────────────────
	matchingJob := matching.NewJob(database, discordClient, logger)
	go runScheduler(ctx, matchingJob, database, logger)

	// ── Server ────────────────────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("broker starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	logger.Info("broker stopped")
}

// runScheduler runs periodic jobs on a fixed schedule.
// Matching runs every 6 hours to align with the skill's onSchedule hook.
// Cleanup runs hourly.
func runScheduler(
	ctx context.Context,
	job *matching.Job,
	database *db.DB,
	logger *slog.Logger,
) {
	matchTicker := time.NewTicker(6 * time.Hour)
	cleanTicker := time.NewTicker(1 * time.Hour)
	defer matchTicker.Stop()
	defer cleanTicker.Stop()

	// Run matching immediately on startup
	if err := job.Run(ctx); err != nil {
		logger.Error("initial matching job failed", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-matchTicker.C:
			if err := job.Run(ctx); err != nil {
				logger.Error("matching job failed", "err", err)
			}

		case <-cleanTicker.C:
			n, err := database.CleanExpiredSignals(ctx)
			if err != nil {
				logger.Error("cleanup failed", "err", err)
			} else if n > 0 {
				logger.Info("cleaned expired signals", "count", n)
			}

			if err := database.CleanExpiredVerifications(ctx); err != nil {
				logger.Error("verification cleanup failed", "err", err)
			}
		}
	}
}
