// Command brain-quant is the QuantBrain sidecar binary.
//
// It reads market data from the Data sidecar via Kernel's
// specialist.call_tool RPC and runs the strategy→aggregate→risk→execute
// pipeline. No embedded DataBrain — single data source architecture.
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

// buildQuantBrain constructs accounts and QuantBrain from config.
// Market data comes from the Data sidecar via RemoteBufferManager (wired
// later in SetKernelCaller); a placeholder empty BufferManager is used here.
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

	// Placeholder buffers — replaced by RemoteBufferManager in SetKernelCaller.
	placeholder := ringbuf.NewBufferManager(1)

	// Build quant brain
	qb := quant.New(cfg.Brain, placeholder, logger.With("brain", "quant"))

	// Apply global risk config from YAML (zero values → use defaults).
	if cfg.GlobalRisk.MaxGlobalExposurePct > 0 {
		qb.SetGlobalRiskConfig(cfg.GlobalRisk)
		logger.Info("global risk config applied from config file")
	}

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

	// Build shared risk components from config (applied to all units).
	guard := cfg.Risk.BuildGuard()
	sizer := cfg.Risk.BuildSizer()

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

		tf := uc.Timeframe
		if tf == "" {
			tf = cfg.Brain.DefaultTimeframe
		}

		// Build aggregator with timeframe-adaptive thresholds.
		agg := cfg.Strategy.BuildAggregator(tf)

		// Resolve route config from account definition.
		var routeCfg quant.RouteConfig
		for _, ac := range cfg.Accounts {
			if ac.ID == uc.AccountID && ac.Route != nil {
				routeCfg = *ac.Route
				break
			}
		}

		unit := quant.NewTradingUnit(quant.TradingUnitConfig{
			ID:          uc.ID,
			Account:     acc,
			Symbols:     uc.Symbols,
			Timeframe:   uc.Timeframe,
			MaxLeverage: uc.MaxLeverage,
			TradeStore:  ts,
			Aggregator:  agg,
			Guard:       guard,
			Sizer:       sizer,
			RouteConfig: routeCfg,
		}, logger)
		qb.AddUnit(unit)
		logger.Info("unit registered", "id", uc.ID, "timeframe", tf,
			"long_threshold", agg.BaseAggregator().LongThreshold,
			"short_threshold", agg.BaseAggregator().ShortThreshold)
	}

	return accounts, qb
}
