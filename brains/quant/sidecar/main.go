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

	// Collect explicitly configured symbols from enabled units.
	// If ANY unit has an empty symbols list, it trades all active instruments,
	// so the embedded DataBrain must use the full active-list discovery mode.
	symbolSet := make(map[string]bool)
	hasOpenUnit := false // true if any unit trades "all active instruments"
	for _, uc := range cfg.Units {
		if !uc.Enabled {
			continue
		}
		if len(uc.Symbols) == 0 {
			hasOpenUnit = true
		}
		for _, s := range uc.Symbols {
			symbolSet[s] = true
		}
	}
	pinned := make([]string, 0, len(symbolSet))
	for s := range symbolSet {
		pinned = append(pinned, s)
	}

	// Build DataBrain config based on whether we need active-list discovery.
	var dataCfg data.Config
	if hasOpenUnit {
		// Volatility discovery mode: from liquid instruments, pick the ones
		// with the highest 24h amplitude (price swing). Low-volatility coins
		// produce weak signals and poor risk/reward.
		dataCfg = data.Config{
			ActiveList: data.ActiveListConfig{
				AlwaysInclude:    pinned,
				MaxInstruments:   20,           // top 20 by volatility
				MinVolume24h:     10_000_000,   // $10M minimum daily volume (liquidity filter)
				RankByVolatility: true,          // rank by 24h amplitude, not volume
				MinAmplitudePct:  2.0,           // skip coins with < 2% daily swing
			},
		}
		logger.Info("data mode: volatility discovery", "pinned", len(pinned), "max", 20, "min_amplitude", "2%")
	} else if len(pinned) > 0 {
		// Fixed mode: only trade the explicitly configured symbols.
		dataCfg = data.Config{
			ActiveList: data.ActiveListConfig{
				AlwaysInclude:  pinned,
				MaxInstruments: len(pinned),
				MinVolume24h:   0, // no volume filter — trade exactly these
			},
		}
		logger.Info("data mode: fixed symbols", "symbols", pinned)
	} else {
		// No units or all disabled — fallback defaults.
		dataCfg = data.Config{
			ActiveList: data.ActiveListConfig{
				AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"},
				MaxInstruments: 2,
				MinVolume24h:   0,
			},
		}
		logger.Info("data mode: fallback defaults (BTC, ETH)")
	}
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
		}, logger)
		qb.AddUnit(unit)
		logger.Info("unit registered", "id", uc.ID, "timeframe", tf,
			"long_threshold", agg.BaseAggregator().LongThreshold,
			"short_threshold", agg.BaseAggregator().ShortThreshold)
	}

	return accounts, qb
}
