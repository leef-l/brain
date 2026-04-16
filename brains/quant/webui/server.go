package webui

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

// ServerConfig holds the dependencies for the WebUI server.
type ServerConfig struct {
	Addr           string // listen address, default ":8380"
	QB             *quant.QuantBrain
	Accounts       map[string]*quant.Account
	AccountConfigs map[string]quant.AccountConfig // account ID → config (for initial_equity)
	PGStore        *tradestore.PGStore            // optional, for equity curve queries
	FullConfig     *quant.FullConfig              // mutable reference for config read/write
	Logger         *slog.Logger
}

// Server is the WebUI HTTP/WebSocket server.
type Server struct {
	cfg    ServerConfig
	hub    *Hub
	srv    *http.Server
	logger *slog.Logger
}

// NewServer creates a WebUI server with the given config.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8380"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	logger := cfg.Logger.With("module", "webui")

	hub := newHub(logger)

	mux := http.NewServeMux()

	// API routes
	api := &apiHandler{
		qb:             cfg.QB,
		accounts:       cfg.Accounts,
		accountConfigs: cfg.AccountConfigs,
		pgStore:        cfg.PGStore,
		fullConfig:     cfg.FullConfig,
		hub:            hub,
		logger:         logger,
	}
	mux.HandleFunc("GET /api/v1/portfolio", api.handlePortfolio)
	mux.HandleFunc("GET /api/v1/positions", api.handlePositions)
	mux.HandleFunc("POST /api/v1/positions/close", api.handleClosePosition)
	mux.HandleFunc("POST /api/v1/positions/close-all", api.handleCloseAllPositions)
	mux.HandleFunc("POST /api/v1/positions/sync", api.handleSyncPositions)
	mux.HandleFunc("POST /api/v1/trading/pause", api.handlePause)
	mux.HandleFunc("POST /api/v1/trading/resume", api.handleResume)
	mux.HandleFunc("GET /api/v1/trading/status", api.handleTradingStatus)
	mux.HandleFunc("GET /api/v1/trades", api.handleTrades)
	mux.HandleFunc("GET /api/v1/equity-curve", api.handleEquityCurve)
	mux.HandleFunc("GET /api/v1/candles", api.handleCandles)
	mux.HandleFunc("GET /api/v1/symbols", api.handleSymbols)
	mux.HandleFunc("GET /api/v1/accounts", api.handleAccounts)
	mux.HandleFunc("GET /api/v1/strategy-info", api.handleStrategyInfo)
	mux.HandleFunc("GET /api/v1/config", api.handleGetConfig)
	mux.HandleFunc("PUT /api/v1/config", api.handleUpdateConfig)
	mux.HandleFunc("GET /api/v1/config/defaults", api.handleConfigDefaults)

	// WebSocket
	mux.HandleFunc("GET /ws", hub.handleWS)

	// Static files (SPA) — no-cache to ensure browser picks up new deploys
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		fileServer.ServeHTTP(w, r)
	}))

	return &Server{
		cfg:    cfg,
		hub:    hub,
		logger: logger,
		srv: &http.Server{
			Addr:         cfg.Addr,
			Handler:      corsMiddleware(mux),
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
	}
}

// Start begins listening and pushing data. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) {
	// Start WebSocket hub
	go s.hub.run(ctx)

	// Start periodic data push (1 second interval)
	go s.pushLoop(ctx)

	// Start HTTP server
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		s.logger.Error("webui listen failed", "addr", s.cfg.Addr, "err", err)
		return
	}
	s.logger.Info("webui server started", "addr", s.cfg.Addr)

	go func() {
		<-ctx.Done()
		s.srv.Close()
	}()

	if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		s.logger.Error("webui server error", "err", err)
	}
}

// pushLoop sends portfolio ticks every 1 second via WebSocket.
func (s *Server) pushLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.hub.clientCount() == 0 {
				continue // no clients, skip work
			}
			data := s.collectPortfolioTick(ctx)
			s.hub.broadcast(wsMessage{
				Type: "portfolio_tick",
				Data: data,
				TS:   time.Now().UnixMilli(),
			})
		}
	}
}

// collectPortfolioTick gathers all account data for a WebSocket push.
//
// Shared-account deduplication: when multiple logical accounts share the
// same OKX API key, QueryBalance/QueryPositions return identical data.
// We query each physical exchange only once and assign positions to the
// account whose unit has a matching open trade record. This eliminates
// duplicate "orphan" positions and inflated equity totals.
func (s *Server) collectPortfolioTick(ctx context.Context) portfolioTick {
	tick := portfolioTick{
		Accounts:  make([]accountTick, 0, len(s.cfg.Accounts)),
		Positions: make([]positionTick, 0),
	}

	units := s.cfg.QB.Units()

	// --- Step 1: deduplicate exchange queries by API key ---
	// Group accounts that share the same OKX API key so we only query once.
	type exchangeData struct {
		balance   exchange.BalanceInfo
		positions []exchange.PositionInfo
		queried   bool
	}
	apiKeyData := make(map[string]*exchangeData) // apiKey → data
	accountAPIKey := make(map[string]string)      // accountID → apiKey (empty for paper)

	for id := range s.cfg.Accounts {
		if ac, ok := s.cfg.AccountConfigs[id]; ok && ac.APIKey != "" {
			accountAPIKey[id] = ac.APIKey
		}
	}

	// Query each physical exchange once.
	for id, acct := range s.cfg.Accounts {
		apiKey := accountAPIKey[id]
		if apiKey != "" {
			if _, exists := apiKeyData[apiKey]; exists {
				continue // already queried this physical exchange
			}
		}
		balance, err := acct.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		positions, _ := acct.Exchange.QueryPositions(ctx)
		if apiKey != "" {
			apiKeyData[apiKey] = &exchangeData{balance: balance, positions: positions, queried: true}
		} else {
			// Paper or unique exchange — store under account ID as key.
			apiKeyData["_paper_"+id] = &exchangeData{balance: balance, positions: positions, queried: true}
		}
	}

	// --- Step 2: for shared accounts, find which unit owns each position ---
	// posKey = "apiKey:symbol:side" → owning accountID
	type posOwner struct {
		accountID string
		tradeID   string
		strategy  string
		sl, tp    float64
		openTime  int64
		health    float64
		timeframe string
	}
	posOwners := make(map[string]*posOwner)

	for apiKey, ed := range apiKeyData {
		if !ed.queried {
			continue
		}
		for _, p := range ed.positions {
			if p.Quantity <= 0 {
				continue
			}
			pk := apiKey + ":" + p.Symbol + ":" + p.Side
			// Search all units across all accounts sharing this API key for a matching trade.
			for _, u := range units {
				uAPIKey := accountAPIKey[u.Account.ID]
				effectiveKey := uAPIKey
				if effectiveKey == "" {
					effectiveKey = "_paper_" + u.Account.ID
				}
				if effectiveKey != apiKey {
					continue
				}
				openTrades := u.TradeStore.Query(tradestore.Filter{
					UnitID:   u.ID,
					Symbol:   p.Symbol,
					OpenOnly: true,
					Limit:    1,
				})
				for _, tr := range openTrades {
					if tr.ExitPrice == 0 {
						health := s.cfg.QB.PositionHealth(u.ID + ":" + p.Symbol)
						posOwners[pk] = &posOwner{
							accountID: u.Account.ID,
							tradeID:   tr.ID,
							strategy:  tr.Strategy,
							sl:        tr.StopLoss,
							tp:        tr.TakeProfit,
							health:    health,
							timeframe: u.Timeframe,
						}
						if !tr.EntryTime.IsZero() {
							posOwners[pk].openTime = tr.EntryTime.UnixMilli()
						}
						break
					}
				}
				if posOwners[pk] != nil {
					break
				}
			}
			// Fallback: search by symbol only (no unit_id filter) for renamed units.
			if posOwners[pk] == nil {
				for _, u := range units {
					uAPIKey := accountAPIKey[u.Account.ID]
					effectiveKey := uAPIKey
					if effectiveKey == "" {
						effectiveKey = "_paper_" + u.Account.ID
					}
					if effectiveKey != apiKey || u.TradeStore == nil {
						continue
					}
					openTrades := u.TradeStore.Query(tradestore.Filter{
						Symbol:   p.Symbol,
						OpenOnly: true,
						Limit:    1,
					})
					for _, tr := range openTrades {
						if tr.ExitPrice == 0 {
							health := s.cfg.QB.PositionHealth(u.ID + ":" + p.Symbol)
							posOwners[pk] = &posOwner{
								accountID: u.Account.ID,
								tradeID:   tr.ID,
								strategy:  tr.Strategy,
								sl:        tr.StopLoss,
								tp:        tr.TakeProfit,
								health:    health,
								timeframe: u.Timeframe,
							}
							if !tr.EntryTime.IsZero() {
								posOwners[pk].openTime = tr.EntryTime.UnixMilli()
							}
							break
						}
					}
					if posOwners[pk] != nil {
						break
					}
				}
			}
		}
	}

	// --- Step 3: build account ticks and position ticks ---
	// Track which positions have been emitted to avoid duplicates.
	emittedPositions := make(map[string]bool) // "apiKey:symbol:side" → emitted
	exEquityCounted := make(map[string]bool)  // effectiveKey → counted (avoid 4x OKX balance)

	for id, acct := range s.cfg.Accounts {
		apiKey := accountAPIKey[id]
		effectiveKey := apiKey
		if effectiveKey == "" {
			effectiveKey = "_paper_" + id
		}
		ed := apiKeyData[effectiveKey]
		if ed == nil {
			continue
		}

		// Use initial_equity for equity display when configured.
		var initEquity float64
		if ac, ok := s.cfg.AccountConfigs[id]; ok {
			initEquity = ac.InitialEquity
		}
		displayEquity := ed.balance.Equity
		if initEquity > 0 {
			displayEquity = initEquity
		}

		at := accountTick{
			AccountID:      id,
			Equity:         displayEquity,
			ExchangeEquity: ed.balance.Equity,
			Available:      ed.balance.Available,
			Margin:         ed.balance.Margin,
			InitialEquity:  initEquity,
		}
		tick.TotalEquity += displayEquity
		if !exEquityCounted[effectiveKey] {
			tick.ExchangeEquity += ed.balance.Equity
			exEquityCounted[effectiveKey] = true
		}
		tick.TotalMargin += ed.balance.Margin
		tick.InitialEquity += initEquity
		tick.Accounts = append(tick.Accounts, at)

		// Emit positions: only show a position under the account that owns it.
		// For unowned (true orphan) positions, show under the first account only.
		_ = acct // suppress unused
		for _, p := range ed.positions {
			if p.Quantity <= 0 {
				continue
			}
			pk := effectiveKey + ":" + p.Symbol + ":" + p.Side
			if emittedPositions[pk] {
				continue // already emitted under another account
			}

			owner := posOwners[pk]
			if owner != nil && owner.accountID != id {
				continue // owned by a different account, skip
			}
			// Either owned by this account, or unowned (show under first account).
			emittedPositions[pk] = true

			markPrice := p.MarkPrice
			if markPrice <= 0 {
				markPrice = p.AvgPrice
			}
			notional := p.Quantity * markPrice

			var tradeID, strat, timeframe string
			var sl, tp float64
			var openTime int64
			health := -1.0
			if owner != nil {
				tradeID = owner.tradeID
				strat = owner.strategy
				sl = owner.sl
				tp = owner.tp
				openTime = owner.openTime
				health = owner.health
				timeframe = owner.timeframe
			} else {
				// Unowned position: get timeframe from first matching unit.
				for _, u := range units {
					if u.Account.ID == id {
						timeframe = u.Timeframe
						break
					}
				}
			}

			pt := positionTick{
				AccountID:  id,
				Symbol:     p.Symbol,
				Side:       p.Side,
				Quantity:   p.Quantity,
				AvgPrice:   p.AvgPrice,
				MarkPrice:  markPrice,
				PnL:        p.UnrealizedPL,
				Notional:   notional,
				Health:     health,
				Leverage:   p.Leverage,
				Strategy:   strat,
				StopLoss:   sl,
				TakeProfit: tp,
				Timeframe:  timeframe,
				OpenTime:   openTime,
				TradeID:    tradeID,
			}
			tick.Positions = append(tick.Positions, pt)
			tick.TotalPnL += p.UnrealizedPL

			// Locked PnL: only count guaranteed profit from SL in profit zone.
			var slPnl float64
			if sl > 0 && p.AvgPrice > 0 && p.Quantity > 0 {
				if p.Side == "long" {
					slPnl = (sl - p.AvgPrice) * p.Quantity
				} else {
					slPnl = (p.AvgPrice - sl) * p.Quantity
				}
			}
			if slPnl > 0 {
				tick.LockedPnL += slPnl
			}
		}
	}

	// Trade stats:
	// DayPnL = today's realized (closed trades) + current unrealized (open positions).
	// This matches what a trader expects: "how much am I up/down today?"
	todayStart := time.Now().UTC().Truncate(24 * time.Hour)
	for _, u := range units {
		// Today's closed trades for realized PnL.
		todayStats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID, Since: todayStart})
		tick.DayPnL += todayStats.TotalPnL
		// Lifetime stats for total trades / win rate (only closed trades).
		allTrades := u.TradeStore.Query(tradestore.Filter{UnitID: u.ID})
		var closedWins, closedLosses, closedTotal int
		for _, t := range allTrades {
			if t.ExitPrice > 0 { // only count closed trades
				closedTotal++
				if t.PnL > 0 {
					closedWins++
				} else if t.PnL < 0 {
					closedLosses++
				}
			}
		}
		tick.TotalTrades += closedTotal
		tick.Wins += closedWins
		tick.Losses += closedLosses
	}
	// Add unrealized PnL from open positions to get total day PnL.
	tick.DayPnL += tick.TotalPnL
	if tick.TotalTrades > 0 {
		tick.WinRate = float64(tick.Wins) / float64(tick.TotalTrades) * 100
	}
	tick.Paused = s.cfg.QB.IsPaused()

	return tick
}

// corsMiddleware adds CORS headers for local development.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Data types for WebSocket push
type portfolioTick struct {
	TotalEquity    float64        `json:"total_equity"`
	ExchangeEquity float64        `json:"exchange_equity"` // OKX real total
	TotalMargin    float64        `json:"total_margin"`
	InitialEquity  float64        `json:"initial_equity"`
	Paused         bool           `json:"paused"`
	TotalPnL       float64        `json:"unrealized_pnl"`
	LockedPnL      float64        `json:"locked_pnl"`      // floor PnL: locked profit (SL>entry) or unrealized
	DayPnL         float64        `json:"day_pnl"`
	TotalTrades    int            `json:"total_trades"`
	Wins           int            `json:"wins"`
	Losses         int            `json:"losses"`
	WinRate        float64        `json:"win_rate"`
	Accounts       []accountTick  `json:"accounts"`
	Positions      []positionTick `json:"positions"`
}

type accountTick struct {
	AccountID      string  `json:"account_id"`
	Equity         float64 `json:"equity"`
	ExchangeEquity float64 `json:"exchange_equity"` // OKX real balance
	Available      float64 `json:"available"`
	Margin         float64 `json:"margin"`
	InitialEquity  float64 `json:"initial_equity"`
}

type positionTick struct {
	AccountID  string  `json:"account_id"`
	Symbol     string  `json:"symbol"`
	Side       string  `json:"side"`
	Quantity   float64 `json:"quantity"`
	AvgPrice   float64 `json:"avg_price"`
	MarkPrice  float64 `json:"mark_price"`
	PnL        float64 `json:"pnl"`
	Notional   float64 `json:"notional"`
	Health     float64 `json:"health"`      // -1 = not tracked
	Leverage   int     `json:"leverage"`
	Strategy   string  `json:"strategy"`    // dominant strategy from open trade record
	StopLoss   float64 `json:"stop_loss"`   // SL price from open trade
	TakeProfit float64 `json:"take_profit"` // TP price from open trade
	Timeframe  string  `json:"timeframe"`   // unit timeframe (1m, 15m, 1H, 4H)
	OpenTime   int64   `json:"open_time"`   // entry time as unix milliseconds
	TradeID    string  `json:"trade_id"`    // trade record ID (empty = orphan position)
}

// Exchange balance helper — re-export for use in handlers
type balanceInfo = exchange.BalanceInfo
