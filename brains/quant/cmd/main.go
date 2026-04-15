// Command quant-brain starts the quant brain trading system.
// It creates a DataBrain for market data, then launches QuantBrain
// with configured trading units (accounts + strategies).
//
// Usage:
//
//	quant-brain -config config.json
//	quant-brain -paper              # quick start with paper trading
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"

	"gopkg.in/yaml.v3"
)

func main() {
	configFile := flag.String("config", "", "path to config file (YAML or JSON)")
	paperMode := flag.Bool("paper", false, "quick start with paper trading (no config file needed)")
	equity := flag.Float64("equity", 10000, "initial equity for paper mode")
	pgURL := flag.String("pg", os.Getenv("PG_URL"), "PostgreSQL URL for persistent trade history")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var cfg quant.FullConfig

	if *paperMode {
		cfg = quant.DefaultFullConfig()
		cfg.Accounts[0].InitialEquity = *equity
		logger.Info("starting in paper mode", "equity", *equity)
	} else if *configFile != "" {
		f, err := os.ReadFile(*configFile)
		if err != nil {
			logger.Error("read config", "err", err)
			os.Exit(1)
		}
		ext := strings.ToLower(filepath.Ext(*configFile))
		switch ext {
		case ".yaml", ".yml":
			if err := yaml.Unmarshal(f, &cfg); err != nil {
				logger.Error("parse config (yaml)", "err", err)
				os.Exit(1)
			}
		case ".json":
			if err := json.Unmarshal(f, &cfg); err != nil {
				logger.Error("parse config (json)", "err", err)
				os.Exit(1)
			}
		default:
			// 尝试 YAML 优先（YAML 是 JSON 的超集）
			if err := yaml.Unmarshal(f, &cfg); err != nil {
				logger.Error("parse config", "err", err)
				os.Exit(1)
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "usage: quant-brain -paper OR quant-brain -config config.yaml")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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
			logger.Error("unknown exchange type", "exchange", ac.Exchange, "account", ac.ID)
			os.Exit(1)
		}
		accounts[ac.ID] = &quant.Account{
			ID:       ac.ID,
			Exchange: ex,
			Tags:     ac.Tags,
		}
		logger.Info("account created", "id", ac.ID, "exchange", ac.Exchange)
	}

	// Optional: persistent trade store via PostgreSQL
	var pgStore *tradestore.PGStore
	if *pgURL != "" {
		var err error
		pgStore, err = tradestore.NewPGStoreFromURL(ctx, *pgURL)
		if err != nil {
			logger.Error("pg trade store connect failed", "err", err)
			os.Exit(1)
		}
		if err := pgStore.Migrate(ctx); err != nil {
			logger.Error("pg trade store migrate failed", "err", err)
			os.Exit(1)
		}
		logger.Info("trade store: PostgreSQL connected")
	} else {
		logger.Info("trade store: in-memory (use -pg to persist trade history)")
	}

	// Start data brain (paper mode uses minimal config)
	dataCfg := data.Config{}
	dataBrain := data.New(dataCfg, nil, logger.With("brain", "data"))

	// For paper mode without real data, create a standalone buffer manager
	// In production, data brain provides the buffers
	var buffers *ringbuf.BufferManager
	if err := dataBrain.Start(ctx); err != nil {
		logger.Warn("data brain start failed, using standalone buffers", "err", err)
		buffers = ringbuf.NewBufferManager(1024)
	} else {
		buffers = dataBrain.Buffers()
		logger.Info("data brain started")
	}

	// Build quant brain
	qb := quant.New(cfg.Brain, buffers, logger.With("brain", "quant"))

	// Apply global risk config from YAML (zero values → use defaults).
	if cfg.GlobalRisk.MaxGlobalExposurePct > 0 {
		qb.SetGlobalRiskConfig(cfg.GlobalRisk)
	}

	// Wire up PG-backed trace store if available
	if pgStore != nil {
		pgTraceStore := tracer.NewPGTraceStore(pgStore.Pool())
		if err := pgTraceStore.Migrate(ctx); err != nil {
			logger.Error("trace store migrate failed", "err", err)
			os.Exit(1)
		}
		qb.SetTraceStore(pgTraceStore)
		logger.Info("trace store: PostgreSQL")
	}

	// Build shared risk components from config.
	guard := cfg.Risk.BuildGuard()
	sizer := cfg.Risk.BuildSizer()

	for _, uc := range cfg.Units {
		acc, ok := accounts[uc.AccountID]
		if !ok {
			logger.Error("unit references unknown account", "unit", uc.ID, "account", uc.AccountID)
			os.Exit(1)
		}
		if !uc.Enabled {
			logger.Info("unit disabled, skipping", "unit", uc.ID)
			continue
		}

		// Use PGStore if available, otherwise each unit gets its own MemoryStore
		var ts tradestore.Store
		if pgStore != nil {
			ts = pgStore
		}

		tf := uc.Timeframe
		if tf == "" {
			tf = cfg.Brain.DefaultTimeframe
		}
		agg := cfg.Strategy.BuildAggregator(tf)

		// Resolve route config from account.
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
			Pool:        cfg.Strategy.BuildPool(),
			TradeStore:  ts,
			Aggregator:  agg,
			Guard:       guard,
			Sizer:       sizer,
			RouteConfig: routeCfg,
		}, logger)
		qb.AddUnit(unit)
	}

	// Run crash recovery if PG is available (restores trade history + validates positions)
	if pgStore != nil {
		if err := qb.RecoverState(ctx, quant.DefaultRecoveryConfig()); err != nil {
			logger.Warn("crash recovery", "err", err)
		}
	}

	if err := qb.Start(ctx); err != nil {
		logger.Error("quant brain start failed", "err", err)
		os.Exit(1)
	}

	logger.Info("system running, press Ctrl+C to stop")

	// Health reporter
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.Info("health", "data", dataBrain.Health(), "quant", qb.Health())
			}
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := qb.Stop(shutdownCtx); err != nil {
		logger.Error("quant brain stop", "err", err)
	}
	if err := dataBrain.Stop(shutdownCtx); err != nil {
		logger.Error("data brain stop", "err", err)
	}
	if pgStore != nil {
		pgStore.Close()
		logger.Info("trade store closed")
	}

	logger.Info("shutdown complete")
}
