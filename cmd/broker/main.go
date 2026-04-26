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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-merge/broker/internal/config"
	"github.com/open-merge/broker/internal/handler"
	"github.com/open-merge/broker/internal/matching"
	"github.com/open-merge/broker/internal/middleware"
	"github.com/open-merge/broker/internal/store"
	"github.com/open-merge/broker/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to database:" + cfg.DatabaseURL)

	signalStore := store.NewPostgresStore(pool, cfg.SignalTTL)

	// Telegram integration (optional — nil client disables introductions)
	telegramClient := telegram.New(cfg.TelegramBotToken)
	var introducer matching.Introducer
	if intro := telegram.NewIntroducer(telegramClient, signalStore, logger); intro != nil {
		introducer = intro
		slog.Info("telegram introductions enabled")
	} else {
		slog.Info("telegram introductions disabled (no TELEGRAM_BOT_TOKEN)")
	}

	matchJob := matching.NewJob(signalStore, logger, introducer)
	signalHandler := handler.NewSignalHandler(signalStore, cfg.SignalTTL, matchJob)
	discoveryHandler := handler.NewDiscoveryHandler(signalStore)
	matchHandler := handler.NewMatchHandler(signalStore)
	authHandler := handler.NewAuthHandler(signalStore, cfg.AuthSecret, telegramClient, cfg.TelegramClientID, cfg.TelegramClientSecret, cfg.PublicURL)
	healthHandler := handler.NewHealthHandler(signalStore)
	rateLimiter := middleware.NewRateLimiter(cfg.RateLimit)

	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Logging(logger))
	r.Use(middleware.Recovery)

	// Public routes (no auth)
	r.Get("/healthz", healthHandler.HandleHealthz)
	r.Get("/readyz", healthHandler.HandleReadyz)
	r.Post("/auth/session", authHandler.HandleCreateSession)
	r.Post("/auth/telegram", authHandler.HandleTelegramAuth)
	r.Post("/auth/telegram/start", authHandler.HandleTelegramStart)
	r.Get("/auth/telegram/callback", authHandler.HandleTelegramCallback)
	r.Get("/auth/telegram/poll", authHandler.HandleTelegramPoll)

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(cfg.AuthSecret))
		r.Use(rateLimiter.Middleware)

		r.Put("/signal", signalHandler.HandleUpsertSignal)
		r.Delete("/signal", signalHandler.HandleDeleteSignal)
		r.Get("/signal/{signalId}", signalHandler.HandleGetSignal)
		r.Get("/signals", discoveryHandler.HandleDiscoverSignals)
		r.Get("/matches", matchHandler.HandleGetMatches)
		r.Delete("/account", signalHandler.HandleDeleteAccount)
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Scheduler: matching + cleanup
	go runScheduler(ctx, matchJob, signalStore, cfg, logger)
	authHandler.StartCleanup()

	go func() {
		slog.Info("broker starting", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	slog.Info("broker stopped")
}

func runScheduler(ctx context.Context, matchJob *matching.Job, signalStore store.SignalStore, cfg *config.Config, logger *slog.Logger) {
	matchTicker := time.NewTicker(cfg.MatchInterval)
	cleanTicker := time.NewTicker(cfg.CleanupInterval)
	defer matchTicker.Stop()
	defer cleanTicker.Stop()

	if err := matchJob.Run(ctx); err != nil {
		logger.Error("initial matching job failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-matchTicker.C:
			if err := matchJob.Run(ctx); err != nil {
				logger.Error("matching job failed", "error", err)
			}
		case <-cleanTicker.C:
			n, err := signalStore.CleanExpired(ctx)
			if err != nil {
				logger.Error("signal cleanup failed", "error", err)
			} else if n > 0 {
				logger.Info("cleaned expired signals", "count", n)
			}
			if err := signalStore.CleanExpiredVerifications(ctx); err != nil {
				logger.Error("verification cleanup failed", "error", err)
			}
			if err := matchJob.RetryMissingIntros(ctx); err != nil {
				logger.Error("retry missing intros failed", "error", err)
			}
		}
	}
}
