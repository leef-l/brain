package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/store"
	"github.com/leef-l/brain/sdk/sidecar"
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

// sidecarConfig mirrors the on-disk YAML/JSON configuration.
type sidecarConfig struct {
	PG         string `json:"pg_url"`
	Instruments []string `json:"instruments"`
	ActiveList struct {
		MinVolume24h   float64 `json:"min_volume_24h"`
		MaxInstruments int     `json:"max_instruments"`
	} `json:"active_list"`
	Backfill struct {
		Enabled bool `json:"enabled"`
		MaxDays int  `json:"max_days"`
	} `json:"backfill"`
}

func loadConfig(logger *slog.Logger) (data.Config, store.Store) {
	cfg := data.Config{
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
	}

	var st store.Store

	configPath := os.Getenv("DATA_CONFIG")
	if configPath != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			logger.Warn("failed to read config file, using defaults", "path", configPath, "err", err)
			return cfg, connectPG(os.Getenv("PG_URL"), logger)
		}

		var sc sidecarConfig
		if err := json.Unmarshal(raw, &sc); err != nil {
			logger.Warn("failed to parse config file, using defaults", "err", err)
			return cfg, connectPG(os.Getenv("PG_URL"), logger)
		}

		if len(sc.Instruments) > 0 {
			cfg.ActiveList.AlwaysInclude = sc.Instruments
		}
		if sc.ActiveList.MinVolume24h > 0 {
			cfg.ActiveList.MinVolume24h = sc.ActiveList.MinVolume24h
		}
		if sc.ActiveList.MaxInstruments > 0 {
			cfg.ActiveList.MaxInstruments = sc.ActiveList.MaxInstruments
		}
		if sc.Backfill.Enabled {
			cfg.Backfill.Enabled = true
		}
		if sc.Backfill.MaxDays > 0 {
			cfg.Backfill.MaxDays = sc.Backfill.MaxDays
		}

		pgURL := sc.PG
		if pgURL == "" {
			pgURL = os.Getenv("PG_URL")
		}
		st = connectPG(pgURL, logger)
	} else {
		st = connectPG(os.Getenv("PG_URL"), logger)
	}

	return cfg, st
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
