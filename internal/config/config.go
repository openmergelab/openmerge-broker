package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	ListenAddr      string
	DatabaseURL     string
	SignalTTL       time.Duration
	RateLimit       int
	AuthSecret      string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	MatchInterval   time.Duration
	CleanupInterval time.Duration

	// Telegram (optional — leave empty to disable introductions and auth)
	TelegramBotToken     string
	TelegramClientID     string
	TelegramClientSecret string

	// Public URL for OAuth redirects (e.g. https://broker.example.com)
	PublicURL string
}

func Load() (*Config, error) {
	// Load .env file if present; real env vars take precedence
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	authSecret := os.Getenv("BROKER_AUTH_SECRET")
	if authSecret == "" {
		return nil, fmt.Errorf("BROKER_AUTH_SECRET is required")
	}

	cfg := &Config{
		ListenAddr:      envOrDefault("BROKER_LISTEN_ADDR", ":8080"),
		DatabaseURL:     databaseURL,
		SignalTTL:       parseDuration("BROKER_SIGNAL_TTL", 168*time.Hour),
		RateLimit:       parseInt("BROKER_RATE_LIMIT", 60),
		AuthSecret:      authSecret,
		LogLevel:        parseLogLevel(os.Getenv("BROKER_LOG_LEVEL")),
		ShutdownTimeout: parseDuration("BROKER_SHUTDOWN_TIMEOUT", 30*time.Second),
		MatchInterval:   parseDuration("BROKER_MATCH_INTERVAL", 6*time.Hour),
		CleanupInterval: parseDuration("BROKER_CLEANUP_INTERVAL", 1*time.Hour),

		TelegramBotToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramClientID:     os.Getenv("TELEGRAM_CLIENT_ID"),
		TelegramClientSecret: os.Getenv("TELEGRAM_CLIENT_SECRET"),

		PublicURL: os.Getenv("BROKER_PUBLIC_URL"),
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func parseInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
