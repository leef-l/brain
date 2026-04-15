// Command brain-quant is the QuantBrain sidecar binary.
//
// It starts a complete DataBrain + QuantBrain pipeline and exposes
// account-query tools via the sidecar stdio JSON-RPC protocol.
// The Kernel launches this as a child process through BrainRegistration.
//
// Configuration is read from the path in QUANT_CONFIG env var,
// or falls back to paper-trading defaults.
//
// See docs: 37-量化大脑设计.md §13, 35-量化系统三脑架构总览.md §5.
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"

	"gopkg.in/yaml.v3"
)

// Main is the sidecar entry point. It is exported so that a thin cmd/
// wrapper can call it (package main cannot be inside a library package).
func Main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if _, err := license.CheckSidecar("brain-quant", license.VerifyOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: license: %v\n", err)
		os.Exit(1)
	}

	cfg := loadConfig(logger)
	accounts, qb := buildQuantBrain(cfg, logger)

	handler := NewHandler(qb, accounts, logger)

	// Start the quant brain evaluation loop in background.
	ctx := context.Background()
	if err := qb.Start(ctx); err != nil {
		// Non-fatal: sidecar tools still work for status queries even if
		// the evaluation loop can't start (e.g. no units configured).
		logger.Warn("quant brain start", "err", err)
	}

	if err := sidecar.Run(handler); err != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: %v\n", err)
		os.Exit(1)
	}
}

// loadConfig reads the quant config from QUANT_CONFIG env or defaults.
func loadConfig(logger *slog.Logger) quant.FullConfig {
	configPath := os.Getenv("QUANT_CONFIG")
	if configPath == "" {
		logger.Info("QUANT_CONFIG not set, using paper-trading defaults")
		return quant.DefaultFullConfig()
	}

	f, err := os.ReadFile(configPath)
	if err != nil {
		logger.Error("read config", "path", configPath, "err", err)
		os.Exit(1)
	}

	var cfg quant.FullConfig
	ext := strings.ToLower(filepath.Ext(configPath))
	switch ext {
	case ".json":
		err = json.Unmarshal(f, &cfg)
	default:
		err = yaml.Unmarshal(f, &cfg)
	}
	if err != nil {
		logger.Error("parse config", "path", configPath, "err", err)
		os.Exit(1)
	}

	logger.Info("config loaded", "path", configPath,
		"accounts", len(cfg.Accounts), "units", len(cfg.Units))
	return cfg
}

// buildQuantBrain constructs accounts, DataBrain, and QuantBrain from config.
func buildQuantBrain(cfg quant.FullConfig, logger *slog.Logger) (map[string]*quant.Account, *quant.QuantBrain) {
	ctx := context.Background()

	// Build accounts
	accounts := make(map[string]*quant.Account, len(cfg.Accounts))
	for _, ac := range cfg.Accounts {
		var ex exchange.Exchange
		switch ac.Exchange {
		case "paper":
			ex = exchange.NewPaperExchange(exchange.PaperConfig{
				InitialEquity: ac.InitialEquity,
			})
		case "okx":
			ex = exchange.NewOKXExchange(exchange.OKXConfig{
				APIKey:     ac.APIKey,
				SecretKey:  ac.SecretKey,
				Passphrase: ac.Passphrase,
				BaseURL:    ac.BaseURL,
				Simulated:  ac.Simulated,
			})
		default:
			logger.Error("unknown exchange", "exchange", ac.Exchange, "account", ac.ID)
			os.Exit(1)
		}
		accounts[ac.ID] = &quant.Account{
			ID:       ac.ID,
			Exchange: ex,
			Tags:     ac.Tags,
		}
	}

	// Start data brain for ring buffers
	dataCfg := data.Config{}
	dataBrain := data.New(dataCfg, nil, logger.With("brain", "data"))

	var buffers *ringbuf.BufferManager
	if err := dataBrain.Start(ctx); err != nil {
		logger.Warn("data brain start failed, using standalone buffers", "err", err)
		buffers = ringbuf.NewBufferManager(1024)
	} else {
		buffers = dataBrain.Buffers()
	}

	// Build quant brain
	qb := quant.New(cfg.Brain, buffers, logger.With("brain", "quant"))

	// Optional PG trade store
	pgURL := os.Getenv("PG_URL")
	var pgStore *tradestore.PGStore
	if pgURL != "" {
		var err error
		pgStore, err = tradestore.NewPGStoreFromURL(ctx, pgURL)
		if err != nil {
			logger.Warn("pg trade store connect failed, using in-memory", "err", err)
		} else {
			if err := pgStore.Migrate(ctx); err != nil {
				logger.Error("pg trade store migrate failed", "err", err)
			}
			pgTraceStore := tracer.NewPGTraceStore(pgStore.Pool())
			if err := pgTraceStore.Migrate(ctx); err != nil {
				logger.Error("pg trace store migrate failed", "err", err)
			}
			qb.SetTraceStore(pgTraceStore)
			logger.Info("trade store: PostgreSQL connected")
		}
	}

	// Register trading units
	for _, uc := range cfg.Units {
		acc, ok := accounts[uc.AccountID]
		if !ok {
			logger.Error("unit references unknown account", "unit", uc.ID, "account", uc.AccountID)
			continue
		}
		if !uc.Enabled {
			continue
		}

		var ts tradestore.Store
		if pgStore != nil {
			ts = pgStore
		}

		unit := quant.NewTradingUnit(quant.TradingUnitConfig{
			ID:          uc.ID,
			Account:     acc,
			Symbols:     uc.Symbols,
			Timeframe:   uc.Timeframe,
			MaxLeverage: uc.MaxLeverage,
			TradeStore:  ts,
		}, logger)
		qb.AddUnit(unit)
	}

	return accounts, qb
}
