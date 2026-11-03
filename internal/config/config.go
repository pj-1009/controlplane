// Package config loads control plane settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Config holds everything the control plane process needs to start.
type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	AMQPURL       string
	RelayInterval time.Duration
	LogLevel      slog.Level
}

// Load reads the configuration from the environment, applying defaults
// that match docker-compose.yml.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8000"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		AMQPURL:     os.Getenv("AMQP_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.AMQPURL == "" {
		return Config{}, errors.New("AMQP_URL is required")
	}

	interval, err := time.ParseDuration(getenv("RELAY_INTERVAL", "500ms"))
	if err != nil {
		return Config{}, fmt.Errorf("parse RELAY_INTERVAL: %w", err)
	}
	if interval <= 0 {
		return Config{}, errors.New("RELAY_INTERVAL must be positive")
	}
	cfg.RelayInterval = interval

	level, err := ParseLogLevel(getenv("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	return cfg, nil
}

// ParseLogLevel turns "debug", "info", "warn" or "error" into a slog.Level.
func ParseLogLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("parse LOG_LEVEL: %w", err)
	}
	return level, nil
}

// NewLogger returns a JSON slog logger writing to stdout.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
