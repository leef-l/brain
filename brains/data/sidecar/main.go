package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/store"
	"github.com/leef-l/brain/sdk/sidecar"

	"gopkg.in/yaml.v3"
)

// Main is the entry point for the data brain sidecar binary.
// It reads configuration from DATA_CONFIG env var, constructs a DataBrain,
// starts it, and runs the sidecar stdio JSON-RPC loop.
func Main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, st := loadConfig(logger)

	db := data.New(cfg, st, logger)

	ctx := context.Background()
	if err := db.Start(ctx); err != nil {
		logger.Error("failed to start data brain", "err", err)
		os.Exit(1)
	}

	handler := NewHandler(db, logger)
	logger.Info("data brain sidecar starting", "tools", len(handler.Tools()))

	if err := sidecar.Run(handler); err != nil {
		logger.Error("sidecar run failed", "err", err)
		os.Exit(1)
	}
}

// fullSidecarConfig wraps data.Config with an optional pg_url field.
type fullSidecarConfig struct {
	data.Config `json:",inline" yaml:",inline"`
	PG          string `json:"pg_url" yaml:"pg_url"`
}

func loadConfig(logger *slog.Logger) (data.Config, store.Store) {
	defaults := data.Config{
		ActiveList: data.ActiveListConfig{
			MinVolume24h:   10_000_000,
			MaxInstruments: 100,
			UpdateInterval: 7 * 24 * time.Hour,
			AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"},
		},
		Backfill: data.BackfillConfig{
			Enabled:   false,
			MaxDays:   90,
			BatchSize: 100,
		},
		Validation: data.ValidationConfig{
			MaxPriceJump: 0.10,
		},
		RingBuffer: data.RingBufferConfig{
			CandleDepth:    1000,
			TradeDepth:     5000,
			OrderBookDepth: 100,
		},
		Feature: data.FeatureConfig{
			Enabled:  true,
			Windows:  []int{5, 10, 20, 60},
			Interval: time.Second,
		},
	}

	configPath := os.Getenv("DATA_CONFIG")
	if configPath == "" {
		logger.Info("DATA_CONFIG not set, using defaults")
		return defaults, connectPG(os.Getenv("PG_URL"), logger)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		logger.Warn("failed to read config file, using defaults", "path", configPath, "err", err)
		return defaults, connectPG(os.Getenv("PG_URL"), logger)
	}

	sc := fullSidecarConfig{Config: defaults}
	ext := strings.ToLower(filepath.Ext(configPath))
	switch ext {
	case ".json":
		err = json.Unmarshal(raw, &sc)
	default:
		err = yaml.Unmarshal(raw, &sc)
	}
	if err != nil {
		logger.Warn("failed to parse config file, using defaults", "path", configPath, "err", err)
		return defaults, connectPG(os.Getenv("PG_URL"), logger)
	}

	logger.Info("config loaded", "path", configPath)

	pgURL := sc.PG
	if pgURL == "" {
		pgURL = os.Getenv("PG_URL")
	}
	return sc.Config, connectPG(pgURL, logger)
}

func connectPG(pgURL string, logger *slog.Logger) store.Store {
	if pgURL == "" {
		logger.Warn("no PG_URL — running without persistence")
		return nil
	}
	pgStore, err := store.NewPGStore(context.Background(), pgURL)
	if err != nil {
		logger.Error("failed to connect to PostgreSQL", "err", err)
		return nil
	}
	if err := pgStore.Migrate(context.Background()); err != nil {
		logger.Error("failed to run migrations", "err", err)
		return nil
	}
	logger.Info("connected to PostgreSQL")
	return pgStore
}
