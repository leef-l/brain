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
	"github.com/leef-l/brain/brains/quant/learning"
	"github.com/leef-l/brain/brains/quant/risk"
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
	} else if envConfig := os.Getenv("QUANT_CONFIG"); envConfig != "" {
		// Sidecar mode: kernel injects QUANT_CONFIG env var.
		f, err := os.ReadFile(envConfig)
		if err != nil {
			logger.Error("read config from QUANT_CONFIG", "path", envConfig, "err", err)
			os.Exit(1)
		}
		ext := strings.ToLower(filepath.Ext(envConfig))
		switch ext {
		case ".json":
			if err := json.Unmarshal(f, &cfg); err != nil {
				logger.Error("parse config (json)", "err", err)
				os.Exit(1)
			}
		default:
			if err := yaml.Unmarshal(f, &cfg); err != nil {
				logger.Error("parse config", "err", err)
				os.Exit(1)
			}
		}
		logger.Info("loaded config from QUANT_CONFIG", "path", envConfig)
	} else {
		fmt.Fprintln(os.Stderr, "usage: quant-brain -paper OR quant-brain -config config.yaml")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Build accounts — track paper exchanges for PG persistence.
	accounts := make(map[string]*quant.Account, len(cfg.Accounts))
	paperExchanges := make(map[string]*exchange.PaperExchange)
	for _, ac := range cfg.Accounts {
		var ex exchange.Exchange
		switch ac.Exchange {
		case "paper":
			pe := exchange.NewPaperExchange(exchange.PaperConfig{
				AccountID:     ac.ID,
				InitialEquity: ac.InitialEquity,
				SlippageBps:   ac.SlippageBps,
				FeeBps:        ac.FeeBps,
			})
			ex = pe
			paperExchanges[ac.ID] = pe
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

	// Apply signal exit config.
	if cfg.SignalExit.Enabled {
		qb.SetSignalExitConfig(cfg.SignalExit)
		logger.Info("signal exit enabled", "min_confidence", cfg.SignalExit.MinConfidence,
			"require_multi_strategy", cfg.SignalExit.RequireMultiStrategy,
			"min_hold", cfg.SignalExit.MinHoldDuration,
			"cooldown", cfg.SignalExit.CooldownAfterExit)
	}

	// Wire up PG-backed trace store if available
	var paperStore *tradestore.PaperPGStore
	if pgStore != nil {
		pgTraceStore := tracer.NewPGTraceStore(pgStore.Pool())
		if err := pgTraceStore.Migrate(ctx); err != nil {
			logger.Error("trace store migrate failed", "err", err)
			os.Exit(1)
		}
		qb.SetTraceStore(pgTraceStore)
		logger.Info("trace store: PostgreSQL")

		// Paper exchange PG persistence.
		if len(paperExchanges) > 0 {
			paperStore = tradestore.NewPaperPGStore(pgStore.Pool(), logger)
			if err := paperStore.Migrate(ctx); err != nil {
				logger.Error("paper pg store migrate failed", "err", err)
				paperStore = nil // prevent use of unmigrated store
			} else {
				for id, pe := range paperExchanges {
					if err := pe.RestoreState(ctx, id, paperStore, logger); err != nil {
						logger.Warn("restore paper state failed (starting fresh)", "account", id, "err", err)
					}
					// Restore cumulative realized PnL from closed trade records.
					if pgStore != nil {
						stats := pgStore.Stats(tradestore.Filter{AccountID: id})
						if stats.TotalPnL != 0 {
							pe.RestoreCumulativePnL(stats.TotalPnL, 0)
							logger.Info("restored cumulative realized PnL",
								"account", id, "pnl", stats.TotalPnL, "trades", stats.TotalTrades)
						}
					}
				}
			}
		}
	}

	// Build risk components — auto-scale per account if enabled.
	// When auto_risk is on, each account gets its own guard/sizer tuned to its equity.
	// When off, all accounts share the same manually configured guard/sizer.
	type accountRisk struct {
		guard *risk.AdaptiveGuard
		sizer *risk.BayesianSizer
	}
	perAccountRisk := make(map[string]accountRisk)

	if cfg.AutoRisk.Enabled {
		for id, acc := range accounts {
			bal, err := acc.Exchange.QueryBalance(ctx)
			equity := bal.Equity
			if err != nil || equity <= 0 {
				// Fallback: use initial_equity from config.
				for _, ac := range cfg.Accounts {
					if ac.ID == id {
						equity = ac.InitialEquity
						break
					}
				}
			}
			scaled := cfg.AutoRisk.AutoScale(equity, cfg.Risk)
			perAccountRisk[id] = accountRisk{
				guard: scaled.BuildGuard(),
				sizer: scaled.BuildSizer(),
			}
			logger.Info("auto_risk scaled",
				"account", id,
				"equity", equity,
				"level", cfg.AutoRisk.Level,
				"max_concurrent", scaled.Guard.MaxConcurrentPositions,
				"max_exposure", scaled.Guard.MaxTotalExposurePct,
				"min_fraction", scaled.Sizer.MinFraction,
				"max_fraction", scaled.Sizer.MaxFraction,
				"kelly_scale", scaled.Sizer.ScaleFraction,
			)
		}
		// Also auto-scale global risk.
		globalRisk := cfg.AutoRisk.AutoScaleGlobalRisk(cfg.GlobalRisk)
		qb.SetGlobalRiskConfig(globalRisk)
		logger.Info("auto_risk global",
			"max_exposure", globalRisk.MaxGlobalExposurePct,
			"max_same_dir", globalRisk.MaxGlobalSameDirection,
			"max_daily_loss", globalRisk.MaxGlobalDailyLoss,
		)
	} else {
		// Manual mode: shared guard/sizer for all accounts.
		shared := accountRisk{
			guard: cfg.Risk.BuildGuard(),
			sizer: cfg.Risk.BuildSizer(),
		}
		for id := range accounts {
			perAccountRisk[id] = shared
		}
	}

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

		ar := perAccountRisk[uc.AccountID]
		unit := quant.NewTradingUnit(quant.TradingUnitConfig{
			ID:          uc.ID,
			Account:     acc,
			Symbols:     uc.Symbols,
			Timeframe:   uc.Timeframe,
			MaxLeverage: uc.MaxLeverage,
			Pool:        cfg.Strategy.BuildPool(),
			TradeStore:  ts,
			Aggregator:  agg,
			Guard:       ar.guard,
			Sizer:       ar.sizer,
			RouteConfig: routeCfg,
		}, logger)
		qb.AddUnit(unit)
	}

	// Wire up L1 adaptive learning (requires PG trade store for history).
	if pgStore != nil {
		wa := learning.NewWeightAdapter(learning.WeightAdapterConfig{
			BaseWeights: cfg.Strategy.Weights,
			WindowSize:  100,
			MinSamples:  20,
		}, logger.With("component", "weight_adapter"))
		ss := learning.NewSymbolScorer(learning.SymbolScorerConfig{
			WindowDays: 7,
			MinTrades:  5,
		}, logger.With("component", "symbol_scorer"))
		opt := learning.NewSLTPOptimizer(learning.SLTPOptimizerConfig{
			MinSamples: 20,
			WindowDays: 14,
		}, logger.With("component", "sltp_optimizer"))
		qb.SetLearning(wa, ss, opt)
		logger.Info("L1 adaptive learning enabled")
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

	// Periodic paper state saver.
	if paperStore != nil && len(paperExchanges) > 0 {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					saveCtx, saveCancel := context.WithTimeout(context.Background(), 15*time.Second)
					for id, pe := range paperExchanges {
						if err := pe.SaveState(saveCtx, id, paperStore); err != nil {
							logger.Error("save paper state failed", "account", id, "err", err)
						}
					}
					saveCancel()
				}
			}
		}()
	}

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	// Stop quant brain first — ensures no new trades are generated.
	if err := qb.Stop(shutdownCtx); err != nil {
		logger.Error("quant brain stop", "err", err)
	}

	// Then save paper state (now guaranteed no concurrent writes).
	if paperStore != nil {
		for id, pe := range paperExchanges {
			if err := pe.SaveState(shutdownCtx, id, paperStore); err != nil {
				logger.Error("save paper state on shutdown failed", "account", id, "err", err)
			} else {
				logger.Info("paper state saved", "account", id)
			}
		}
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
