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
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Load + Build (exported for thin cmd/ wrappers)
// ---------------------------------------------------------------------------

// Load reads configuration in this order:
//   1. DATA_CONFIG env var (explicit path)
//   2. ~/.brain/data-brain.yaml
//   3. Built-in defaults
func Load(logger *slog.Logger) (data.Config, store.Store) {
	defaults := defaultDataConfig()

	configPath := os.Getenv("DATA_CONFIG")
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			candidate := filepath.Join(home, ".brain", "data-brain.yaml")
			if _, err := os.Stat(candidate); err == nil {
				configPath = candidate
				logger.Info("found default config", "path", configPath)
			}
		}
	}

	if configPath == "" {
		logger.Info("no config file found, using defaults")
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

// Build constructs a DataBrain from config, starts it, and returns the
// sidecar handler. On fatal startup error it logs and exits.
func Build(cfg data.Config, st store.Store, logger *slog.Logger) sidecar.BrainHandler {
	db := data.New(cfg, st, logger)

	ctx := context.Background()
	if err := db.Start(ctx); err != nil {
		logger.Error("failed to start data brain", "err", err)
		os.Exit(1)
	}

	handler := NewHandler(db, logger)
	logger.Info("data brain sidecar handler built", "tools", len(handler.Tools()))
	return handler
}

// ---------------------------------------------------------------------------
// Main (legacy entry point, kept for backward compat)
// ---------------------------------------------------------------------------

// Main is the entry point for the data brain sidecar binary.
// Thin cmd/ wrappers should prefer calling Load + Build + sidecar.Run
// directly so that the license and stdio wiring live in main.
func Main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		logger.Error("license config failed", "err", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-data", verifyOpts); err != nil {
		logger.Error("license check failed", "err", err)
		os.Exit(1)
	}

	cfg, st := Load(logger)
	handler := Build(cfg, st, logger)

	listen := os.Getenv("BRAIN_LISTEN")
	if listen == "" {
		for i, arg := range os.Args[1:] {
			if arg == "--listen" && i+1 < len(os.Args[1:]) {
				listen = os.Args[i+2]
			}
		}
	}

	var runErr error
	if listen != "" {
		runErr = sidecar.ListenAndServe(listen, handler)
	} else {
		runErr = sidecar.Run(handler)
	}
	if runErr != nil {
		logger.Error("sidecar run failed", "err", runErr)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fullSidecarConfig wraps data.Config with an optional pg_url field.
type fullSidecarConfig struct {
	data.Config `json:",inline" yaml:",inline"`
	PG          string `json:"pg_url" yaml:"pg_url"`
}

func defaultDataConfig() data.Config {
	return data.Config{
		ActiveList: data.ActiveListConfig{
			MinVolume24h:   10_000_000,
			MaxInstruments: 100,
			UpdateInterval: 7 * 24 * time.Hour,
			AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"},
		},
		Backfill: data.BackfillConfig{
			Enabled:   true,
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
