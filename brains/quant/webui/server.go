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
	Addr     string // listen address, default ":8380"
	QB       *quant.QuantBrain
	Accounts map[string]*quant.Account
	PGStore  *tradestore.PGStore // optional, for equity curve queries
	Logger   *slog.Logger
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
		qb:       cfg.QB,
		accounts: cfg.Accounts,
		pgStore:  cfg.PGStore,
		hub:      hub,
		logger:   logger,
	}
	mux.HandleFunc("GET /api/v1/portfolio", api.handlePortfolio)
	mux.HandleFunc("GET /api/v1/positions", api.handlePositions)
	mux.HandleFunc("POST /api/v1/positions/close", api.handleClosePosition)
	mux.HandleFunc("GET /api/v1/trades", api.handleTrades)
	mux.HandleFunc("GET /api/v1/equity-curve", api.handleEquityCurve)
	mux.HandleFunc("GET /api/v1/candles", api.handleCandles)
	mux.HandleFunc("GET /api/v1/symbols", api.handleSymbols)
	mux.HandleFunc("GET /api/v1/accounts", api.handleAccounts)

	// WebSocket
	mux.HandleFunc("GET /ws", hub.handleWS)

	// Static files (SPA)
	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/", fileServer)

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
func (s *Server) collectPortfolioTick(ctx context.Context) portfolioTick {
	tick := portfolioTick{
		Accounts:  make([]accountTick, 0, len(s.cfg.Accounts)),
		Positions: make([]positionTick, 0),
	}

	for id, acct := range s.cfg.Accounts {
		balance, err := acct.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		at := accountTick{
			AccountID: id,
			Equity:    balance.Equity,
			Available: balance.Available,
			Margin:    balance.Margin,
		}
		tick.TotalEquity += balance.Equity
		tick.Accounts = append(tick.Accounts, at)

		positions, err := acct.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}
		for _, p := range positions {
			if p.Quantity <= 0 {
				continue
			}
			markPrice := p.MarkPrice
			if markPrice <= 0 {
				markPrice = p.AvgPrice
			}
			notional := p.Quantity * markPrice

			// Get health from tracker
			health := -1.0
			units := s.cfg.QB.Units()
			for _, u := range units {
				if u.Account.ID == id {
					h := s.cfg.QB.PositionHealth(u.ID + ":" + p.Symbol)
					if h >= 0 {
						health = h
					}
				}
			}

			pt := positionTick{
				AccountID: id,
				Symbol:    p.Symbol,
				Side:      p.Side,
				Quantity:  p.Quantity,
				AvgPrice:  p.AvgPrice,
				MarkPrice: markPrice,
				PnL:       p.UnrealizedPL,
				Notional:  notional,
				Health:    health,
			}
			tick.Positions = append(tick.Positions, pt)
			tick.TotalPnL += p.UnrealizedPL
		}
	}

	// Trade stats
	units := s.cfg.QB.Units()
	for _, u := range units {
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		tick.DayPnL += stats.TotalPnL
		tick.TotalTrades += stats.TotalTrades
		tick.Wins += stats.Wins
		tick.Losses += stats.Losses
	}
	if tick.TotalTrades > 0 {
		tick.WinRate = float64(tick.Wins) / float64(tick.TotalTrades) * 100
	}

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
	TotalEquity float64        `json:"total_equity"`
	TotalPnL    float64        `json:"unrealized_pnl"`
	DayPnL      float64        `json:"day_pnl"`
	TotalTrades int            `json:"total_trades"`
	Wins        int            `json:"wins"`
	Losses      int            `json:"losses"`
	WinRate     float64        `json:"win_rate"`
	Accounts    []accountTick  `json:"accounts"`
	Positions   []positionTick `json:"positions"`
}

type accountTick struct {
	AccountID string  `json:"account_id"`
	Equity    float64 `json:"equity"`
	Available float64 `json:"available"`
	Margin    float64 `json:"margin"`
}

type positionTick struct {
	AccountID string  `json:"account_id"`
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Quantity  float64 `json:"quantity"`
	AvgPrice  float64 `json:"avg_price"`
	MarkPrice float64 `json:"mark_price"`
	PnL       float64 `json:"pnl"`
	Notional  float64 `json:"notional"`
	Health    float64 `json:"health"` // -1 = not tracked
}

// Exchange balance helper — re-export for use in handlers
type balanceInfo = exchange.BalanceInfo
