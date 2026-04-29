// Command brain-quant is the QuantBrain sidecar binary.
//
// It reads market data from the Data sidecar via Kernel's
// specialist.call_tool RPC and runs the strategy→aggregate→risk→execute
// pipeline. No embedded DataBrain �?single data source architecture.
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
	"time"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/learning"
	"github.com/leef-l/brain/brains/quant/risk"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
	"github.com/leef-l/brain/brains/quant/webui"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/sidecar"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Load + Build (exported for thin cmd/ wrappers)
// ---------------------------------------------------------------------------

// Load reads the quant config in this order:
//   1. QUANT_CONFIG env var (explicit path)
//   2. ~/.brain/quant-brain.yaml
//   3. Built-in paper-trading defaults
func Load(logger *slog.Logger) quant.FullConfig {
	configPath := os.Getenv("QUANT_CONFIG")
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			candidate := filepath.Join(home, ".brain", "quant-brain.yaml")
			if _, err := os.Stat(candidate); err == nil {
				configPath = candidate
				logger.Info("found default config", "path", configPath)
			}
		}
	}

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

	cfg.ConfigPath = configPath
	logger.Info("config loaded", "path", configPath,
		"accounts", len(cfg.Accounts), "units", len(cfg.Units))
	return cfg
}

// Build constructs a QuantBrain from config, starts background services,
// and returns the sidecar handler plus a cleanup function that must be
// called after sidecar.Run returns.
func Build(cfg quant.FullConfig, logger *slog.Logger) (sidecar.BrainHandler, func()) {
	accounts, qb, paperSaver, pgStore, wsManager := buildQuantBrain(cfg, logger)
	handler := NewHandler(qb, accounts, logger)

	// Start the quant brain evaluation loop in background.
	ctx, cancel := context.WithCancel(context.Background())

	if err := qb.Start(ctx); err != nil {
		logger.Warn("quant brain start", "err", err)
	}

	if wsManager != nil {
		if err := wsManager.StartAll(ctx); err != nil {
			logger.Warn("OKX private WS start", "err", err)
		}
	}

	if paperSaver != nil {
		go paperSaver.Run(ctx, 30*time.Second)
	}

	if cfg.WebUI.Enabled {
		addr := cfg.WebUI.Addr
		if addr == "" {
			addr = ":8380"
		}
		acConfigs := make(map[string]quant.AccountConfig, len(cfg.Accounts))
		for _, ac := range cfg.Accounts {
			acConfigs[ac.ID] = ac
		}
		webServer := webui.NewServer(webui.ServerConfig{
			Addr:           addr,
			QB:             qb,
			Accounts:       accounts,
			AccountConfigs: acConfigs,
			PGStore:        pgStore,
			FullConfig:     &cfg,
			Logger:         logger,
		})
		go webServer.Start(ctx)
	}

	stop := func() {
		cancel()
		qb.Stop(context.Background())
		if wsManager != nil {
			wsManager.StopAll()
		}
		if paperSaver != nil {
			logger.Info("saving paper state before exit...")
			paperSaver.SaveAll()
		}
	}

	return handler, stop
}

// ---------------------------------------------------------------------------
// Main (legacy entry point, kept for backward compat)
// ---------------------------------------------------------------------------

// Main is the sidecar entry point. Thin cmd/ wrappers should prefer
// calling Load + Build + sidecar.Run directly so that the license and
// stdio wiring live in main.
func Main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	verifyOpts, err := license.VerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: license config: %v\n", err)
		os.Exit(1)
	}
	if _, err := license.CheckSidecar("brain-quant", verifyOpts); err != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: license: %v\n", err)
		os.Exit(1)
	}

	cfg := Load(logger)
	handler, stop := Build(cfg, logger)
	defer stop()

	listenAddr := ""
	for i, arg := range os.Args[1:] {
		if arg == "--listen" && i+1 < len(os.Args[1:]) {
			listenAddr = os.Args[i+2]
		}
	}

	var runErr error
	if listenAddr != "" {
		runErr = sidecar.ListenAndServe(listenAddr, handler)
	} else {
		runErr = sidecar.Run(handler)
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "brain-quant: %v\n", runErr)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// paperStateSaver manages periodic saving of paper exchange state to PG.
type paperStateSaver struct {
	store  *tradestore.PaperPGStore
	papers map[string]*exchange.PaperExchange // accountID �?PaperExchange
	logger *slog.Logger
}

// Run saves paper state every interval until ctx is cancelled.
func (s *paperStateSaver) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SaveAll()
		}
	}
}

// SaveAll persists all paper accounts' state to PG.
func (s *paperStateSaver) SaveAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for id, pe := range s.papers {
		if err := pe.SaveState(ctx, id, s.store); err != nil {
			s.logger.Error("save paper state failed", "account", id, "err", err)
		} else {
			s.logger.Debug("paper state saved", "account", id)
		}
	}
}

// buildQuantBrain constructs accounts and QuantBrain from config.
// Market data comes from the Data sidecar via RemoteBufferManager (wired
// later in SetKernelCaller); a placeholder empty BufferManager is used here.
func buildQuantBrain(cfg quant.FullConfig, logger *slog.Logger) (map[string]*quant.Account, *quant.QuantBrain, *paperStateSaver, *tradestore.PGStore, *exchange.PrivateWSManager) {
	ctx := context.Background()

	// Build accounts �?track paper exchanges for PG persistence.
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
			okxEx := exchange.NewOKXExchange(exchange.OKXConfig{
				APIKey:     ac.APIKey,
				SecretKey:  ac.SecretKey,
				Passphrase: ac.Passphrase,
				BaseURL:    ac.BaseURL,
				Simulated:  ac.Simulated,
			})
			if err := okxEx.Init(ctx); err != nil {
				logger.Error("okx init failed", "account", ac.ID, "err", err)
			} else {
				logger.Info("okx account initialized", "account", ac.ID, "simulated", ac.Simulated)
			}
			ex = okxEx
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

	// Placeholder buffers �?replaced by RemoteBufferManager in SetKernelCaller.
	placeholder := ringbuf.NewBufferManager(1)

	// Build quant brain
	qb := quant.New(cfg.Brain, placeholder, logger.With("brain", "quant"))

	// Apply global risk config from YAML (zero values �?use defaults).
	if cfg.GlobalRisk.MaxGlobalExposurePct > 0 {
		qb.SetGlobalRiskConfig(cfg.GlobalRisk)
		logger.Info("global risk config applied from config file")
	}

	// Apply signal exit config.
	if cfg.SignalExit.Enabled {
		qb.SetSignalExitConfig(cfg.SignalExit)
		logger.Info("signal exit enabled", "min_confidence", cfg.SignalExit.MinConfidence,
			"require_multi_strategy", cfg.SignalExit.RequireMultiStrategy,
			"min_hold", cfg.SignalExit.MinHoldDuration,
			"cooldown", cfg.SignalExit.CooldownAfterExit)
	}

	// Apply trailing stop config.
	if cfg.TrailingStop.Enabled {
		qb.SetTrailingStopConfig(cfg.TrailingStop)
		logger.Info("trailing stop enabled",
			"activation_pct", cfg.TrailingStop.ActivationPct,
			"callback_pct", cfg.TrailingStop.CallbackPct,
			"step_pct", cfg.TrailingStop.StepPct)
	}

	// Optional PG trade store
	pgURL := os.Getenv("PG_URL")
	var pgStore *tradestore.PGStore
	var paperStore *tradestore.PaperPGStore
	var saver *paperStateSaver
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

			if len(paperExchanges) > 0 {
				paperStore = tradestore.NewPaperPGStore(pgStore.Pool(), logger)
				if err := paperStore.Migrate(ctx); err != nil {
					logger.Error("paper pg store migrate failed", "err", err)
					paperStore = nil
				} else {
					for id, pe := range paperExchanges {
						if err := pe.RestoreState(ctx, id, paperStore, logger); err != nil {
							logger.Warn("restore paper state failed (starting fresh)", "account", id, "err", err)
						}
						if pgStore != nil {
							stats := pgStore.Stats(tradestore.Filter{AccountID: id})
							if stats.TotalPnL != 0 {
								pe.RestoreCumulativePnL(stats.TotalPnL, 0)
								logger.Info("restored cumulative realized PnL",
									"account", id, "pnl", stats.TotalPnL, "trades", stats.TotalTrades)
							}
						}
					}
					saver = &paperStateSaver{
						store:  paperStore,
						papers: paperExchanges,
						logger: logger,
					}
				}
			}
		}
	}

	// Build risk components �?auto-scale per account if enabled.
	type accountRisk struct {
		guard *risk.AdaptiveGuard
		sizer *risk.BayesianSizer
	}
	perAccountRisk := make(map[string]accountRisk)

	if cfg.AutoRisk.Enabled {
		for id, acc := range accounts {
			var equity float64
			for _, ac := range cfg.Accounts {
				if ac.ID == id && ac.InitialEquity > 0 {
					equity = ac.InitialEquity
					break
				}
			}
			if equity <= 0 {
				bal, err := acc.Exchange.QueryBalance(ctx)
				if err == nil && bal.Equity > 0 {
					equity = bal.Equity
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
		globalRisk := cfg.AutoRisk.AutoScaleGlobalRisk(cfg.GlobalRisk)
		qb.SetGlobalRiskConfig(globalRisk)
		logger.Info("auto_risk global",
			"max_exposure", globalRisk.MaxGlobalExposurePct,
			"max_same_dir", globalRisk.MaxGlobalSameDirection,
			"max_daily_loss", globalRisk.MaxGlobalDailyLoss,
		)
	} else {
		shared := accountRisk{
			guard: cfg.Risk.BuildGuard(),
			sizer: cfg.Risk.BuildSizer(),
		}
		for id := range accounts {
			perAccountRisk[id] = shared
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

		tf := uc.Timeframe
		if tf == "" {
			tf = cfg.Brain.DefaultTimeframe
		}

		stratCfg := cfg.Strategy
		if uc.Strategy != nil {
			stratCfg = *uc.Strategy
		}

		agg := stratCfg.BuildAggregator(tf)

		var routeCfg quant.RouteConfig
		var budgetEquity float64
		for _, ac := range cfg.Accounts {
			if ac.ID == uc.AccountID {
				if ac.Route != nil {
					routeCfg = *ac.Route
				}
				budgetEquity = ac.InitialEquity
				break
			}
		}

		ar := perAccountRisk[uc.AccountID]
		if uc.Risk != nil {
			ar = accountRisk{
				guard: uc.Risk.BuildGuard(),
				sizer: uc.Risk.BuildSizer(),
			}
		}

		unit := quant.NewTradingUnit(quant.TradingUnitConfig{
			ID:           uc.ID,
			Account:      acc,
			Symbols:      uc.Symbols,
			Timeframe:    uc.Timeframe,
			MaxLeverage:  uc.MaxLeverage,
			Pool:         stratCfg.BuildPool(),
			TradeStore:   ts,
			Aggregator:   agg,
			Guard:        ar.guard,
			Sizer:        ar.sizer,
			RouteConfig:  routeCfg,
			BudgetEquity: budgetEquity,
		}, logger)
		qb.AddUnit(unit)
		logger.Info("unit registered", "id", uc.ID, "timeframe", tf,
			"long_threshold", agg.BaseAggregator().LongThreshold,
			"short_threshold", agg.BaseAggregator().ShortThreshold)
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
		ho := learning.NewHyperOptimizer("default", nil, learning.HyperOptimizerConfig{}, logger.With("component", "hyper_optimizer"))
		qb.SetLearning(wa, ss, opt, ho)
		logger.Info("L1 adaptive learning enabled")
	}

	// Build OKX private WebSocket manager for real-time order/position notifications.
	var wsManager *exchange.PrivateWSManager
	for _, ac := range cfg.Accounts {
		if ac.Exchange != "okx" || ac.APIKey == "" {
			continue
		}
		if wsManager == nil {
			wsManager = exchange.NewPrivateWSManager(logger.With("component", "private_ws"))
		}
		wsConn := exchange.NewPrivateWSConn(ac.ID, exchange.PrivateWSConfig{
			APIKey:     ac.APIKey,
			SecretKey:  ac.SecretKey,
			Passphrase: ac.Passphrase,
			Simulated:  ac.Simulated,
		}, exchange.PrivateWSCallbacks{
			OnOrderFill: func(accountID string, evt exchange.OrderFillEvent) {
				logger.Info("OKX order fill",
					"account", accountID,
					"symbol", evt.InstID,
					"side", evt.Side,
					"posSide", evt.PosSide,
					"fillPrice", evt.FillPrice,
					"fillQty", evt.FillQty,
					"fee", evt.Fee,
					"state", evt.State,
					"orderId", evt.OrderID,
					"clientId", evt.ClientID)
			},
			OnPositionUpdate: func(accountID string, evt exchange.PositionUpdateEvent) {
				logger.Info("OKX position update",
					"account", accountID,
					"symbol", evt.InstID,
					"posSide", evt.PosSide,
					"qty", evt.Quantity,
					"avgPrice", evt.AvgPrice,
					"uPnL", evt.UPnL)
			},
			OnAccountUpdate: func(accountID string, evt exchange.AccountUpdateEvent) {
				logger.Debug("OKX account update",
					"account", accountID,
					"equity", evt.TotalEquity)
			},
		}, logger)
		wsManager.Add(wsConn)
		logger.Info("OKX private WS registered", "account", ac.ID, "simulated", ac.Simulated)
	}

	return accounts, qb, saver, pgStore, wsManager
}
