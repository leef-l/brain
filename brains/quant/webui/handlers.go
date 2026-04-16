package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

type apiHandler struct {
	qb       *quant.QuantBrain
	accounts map[string]*quant.Account
	pgStore  *tradestore.PGStore
	hub      *Hub
	logger   *slog.Logger
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// GET /api/v1/portfolio
func (h *apiHandler) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type accountOut struct {
		AccountID string  `json:"account_id"`
		Exchange  string  `json:"exchange"`
		Equity    float64 `json:"equity"`
		Available float64 `json:"available"`
		Margin    float64 `json:"margin"`
	}

	type result struct {
		TotalEquity   float64      `json:"total_equity"`
		UnrealizedPnL float64      `json:"unrealized_pnl"`
		DayPnL        float64      `json:"day_pnl"`
		TotalTrades   int          `json:"total_trades"`
		Wins          int          `json:"wins"`
		Losses        int          `json:"losses"`
		WinRate       float64      `json:"win_rate"`
		AvgWin        float64      `json:"avg_win"`
		AvgLoss       float64      `json:"avg_loss"`
		LongExposure  float64      `json:"long_exposure"`
		ShortExposure float64      `json:"short_exposure"`
		TotalExposure float64      `json:"total_exposure"`
		Accounts      []accountOut `json:"accounts"`
	}

	out := result{Accounts: make([]accountOut, 0)}

	for id, acct := range h.accounts {
		bal, err := acct.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		out.TotalEquity += bal.Equity
		out.Accounts = append(out.Accounts, accountOut{
			AccountID: id,
			Exchange:  acct.Exchange.Name(),
			Equity:    bal.Equity,
			Available: bal.Available,
			Margin:    bal.Margin,
		})

		positions, err := acct.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}
		for _, p := range positions {
			if p.Quantity <= 0 {
				continue
			}
			mark := p.MarkPrice
			if mark <= 0 {
				mark = p.AvgPrice
			}
			notional := p.Quantity * mark
			out.UnrealizedPnL += p.UnrealizedPL
			if p.Side == "long" {
				out.LongExposure += notional
			} else {
				out.ShortExposure += notional
			}
		}
	}
	out.TotalExposure = out.LongExposure + out.ShortExposure

	// Trade stats from all units
	for _, u := range h.qb.Units() {
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		out.DayPnL += stats.TotalPnL
		out.TotalTrades += stats.TotalTrades
		out.Wins += stats.Wins
		out.Losses += stats.Losses
		out.AvgWin += stats.AvgWin * float64(stats.Wins)
		out.AvgLoss += stats.AvgLoss * float64(stats.Losses)
	}
	if out.Wins > 0 {
		out.AvgWin /= float64(out.Wins)
	}
	if out.Losses > 0 {
		out.AvgLoss /= float64(out.Losses)
	}
	if out.TotalTrades > 0 {
		out.WinRate = float64(out.Wins) / float64(out.TotalTrades) * 100
	}

	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/positions
func (h *apiHandler) handlePositions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type posOut struct {
		AccountID string  `json:"account_id"`
		Symbol    string  `json:"symbol"`
		Side      string  `json:"side"`
		Quantity  float64 `json:"quantity"`
		AvgPrice  float64 `json:"avg_price"`
		MarkPrice float64 `json:"mark_price"`
		PnL       float64 `json:"pnl"`
		Notional  float64 `json:"notional"`
		Margin    float64 `json:"margin"`
		Leverage  int     `json:"leverage"`
		Health    float64 `json:"health"`
	}

	positions := make([]posOut, 0)

	for id, acct := range h.accounts {
		posList, err := acct.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}
		for _, p := range posList {
			if p.Quantity <= 0 {
				continue
			}
			mark := p.MarkPrice
			if mark <= 0 {
				mark = p.AvgPrice
			}

			health := -1.0
			for _, u := range h.qb.Units() {
				if u.Account.ID == id {
					if v := h.qb.PositionHealth(u.ID + ":" + p.Symbol); v >= 0 {
						health = v
					}
				}
			}

			positions = append(positions, posOut{
				AccountID: id,
				Symbol:    p.Symbol,
				Side:      p.Side,
				Quantity:  p.Quantity,
				AvgPrice:  p.AvgPrice,
				MarkPrice: mark,
				PnL:       p.UnrealizedPL,
				Notional:  p.Quantity * mark,
				Margin:    p.Margin,
				Leverage:  p.Leverage,
				Health:    health,
			})
		}
	}

	writeJSON(w, http.StatusOK, positions)
}

// POST /api/v1/positions/close
func (h *apiHandler) handleClosePosition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"`
		Symbol    string `json:"symbol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	acct, ok := h.accounts[req.AccountID]
	if !ok {
		writeError(w, http.StatusNotFound, "account not found: "+req.AccountID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Find the position
	positions, err := acct.Exchange.QueryPositions(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query positions: "+err.Error())
		return
	}

	var target *exchange.PositionInfo
	for i, p := range positions {
		if p.Symbol == req.Symbol && p.Quantity > 0 {
			target = &positions[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "no open position for "+req.Symbol)
		return
	}

	// Close it
	closeSide := "sell"
	if target.Side == "short" {
		closeSide = "buy"
	}

	result, err := acct.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
		Symbol:     req.Symbol,
		Side:       closeSide,
		PosSide:    target.Side,
		Type:       "market",
		Price:      target.MarkPrice,
		Quantity:   target.Quantity,
		Leverage:   target.Leverage,
		ReduceOnly: true,
		ClientID:   fmt.Sprintf("webui-close-%s-%d", req.Symbol, time.Now().UnixMilli()),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "place close order: "+err.Error())
		return
	}

	h.logger.Info("webui manual close",
		"account", req.AccountID, "symbol", req.Symbol,
		"side", target.Side, "qty", target.Quantity,
		"status", result.Status, "fill_price", result.FillPrice)

	// Push trade event to WebSocket
	h.hub.broadcast(wsMessage{
		Type: "trade_event",
		Data: map[string]interface{}{
			"action":     "manual_close",
			"account_id": req.AccountID,
			"symbol":     req.Symbol,
			"side":       closeSide,
			"quantity":   target.Quantity,
			"fill_price": result.FillPrice,
			"status":     result.Status,
		},
		TS: time.Now().UnixMilli(),
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     result.Status,
		"fill_price": result.FillPrice,
		"order_id":   result.OrderID,
	})
}

// GET /api/v1/trades?limit=50&since=2024-01-01
func (h *apiHandler) handleTrades(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	type tradeOut struct {
		ID         string  `json:"id"`
		AccountID  string  `json:"account_id"`
		Symbol     string  `json:"symbol"`
		Direction  string  `json:"direction"`
		EntryPrice float64 `json:"entry_price"`
		ExitPrice  float64 `json:"exit_price"`
		Quantity   float64 `json:"quantity"`
		PnL        float64 `json:"pnl"`
		PnLPct     float64 `json:"pnl_pct"`
		EntryTime  string  `json:"entry_time"`
		ExitTime   string  `json:"exit_time"`
		Reason     string  `json:"reason"`
		Strategy   string  `json:"strategy"`
		Leverage   int     `json:"leverage"`
	}

	trades := make([]tradeOut, 0)

	for _, u := range h.qb.Units() {
		records := u.TradeStore.Query(tradestore.Filter{
			UnitID: u.ID,
			Limit:  limit,
		})
		for _, rec := range records {
			if rec.ExitPrice == 0 {
				continue // still open
			}
			exitTime := ""
			if !rec.ExitTime.IsZero() {
				exitTime = rec.ExitTime.Format(time.RFC3339)
			}
			trades = append(trades, tradeOut{
				ID:         rec.ID,
				AccountID:  rec.AccountID,
				Symbol:     rec.Symbol,
				Direction:  string(rec.Direction),
				EntryPrice: rec.EntryPrice,
				ExitPrice:  rec.ExitPrice,
				Quantity:   rec.Quantity,
				PnL:        rec.PnL,
				PnLPct:     rec.PnLPct,
				EntryTime:  rec.EntryTime.Format(time.RFC3339),
				ExitTime:   exitTime,
				Reason:     rec.Reason,
				Strategy:   rec.Strategy,
				Leverage:   rec.Leverage,
			})
		}
	}

	// Sort by exit time desc (most recent first) — trades come from multiple units
	// Simple approach: they're already ordered within each unit query
	writeJSON(w, http.StatusOK, trades)
}

// GET /api/v1/equity-curve?days=1&account=paper-main
// If account is empty or "all", returns sum of all accounts.
func (h *apiHandler) handleEquityCurve(w http.ResponseWriter, r *http.Request) {
	days := 1
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	account := r.URL.Query().Get("account")

	if h.pgStore == nil {
		writeError(w, http.StatusServiceUnavailable, "PG store not available")
		return
	}

	type point struct {
		Time   string  `json:"time"`
		Equity float64 `json:"equity"`
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var query string
	var args []interface{}

	if account != "" && account != "all" {
		// Single account
		query = `
			SELECT created_at, equity
			FROM account_snapshots
			WHERE account_id = $1 AND created_at >= NOW() - $2::interval
			ORDER BY created_at ASC
		`
		args = []interface{}{account, fmt.Sprintf("%d days", days)}
	} else {
		// All accounts aggregated — sum equity per timestamp bucket
		query = `
			SELECT created_at, SUM(equity) as equity
			FROM account_snapshots
			WHERE created_at >= $1::timestamptz
			GROUP BY created_at
			ORDER BY created_at ASC
		`
		args = []interface{}{time.Now().AddDate(0, 0, -days)}
	}

	rows, err := h.pgStore.Pool().Query(ctx, query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query equity curve: "+err.Error())
		return
	}
	defer rows.Close()

	points := make([]point, 0, 500)
	for rows.Next() {
		var t time.Time
		var eq float64
		if err := rows.Scan(&t, &eq); err != nil {
			continue
		}
		points = append(points, point{
			Time:   t.Format(time.RFC3339),
			Equity: eq,
		})
	}

	// Downsample if too many points
	if len(points) > 500 {
		step := len(points) / 500
		sampled := make([]point, 0, 500)
		for i := 0; i < len(points); i += step {
			sampled = append(sampled, points[i])
		}
		if len(sampled) > 0 && sampled[len(sampled)-1] != points[len(points)-1] {
			sampled = append(sampled, points[len(points)-1])
		}
		points = sampled
	}

	writeJSON(w, http.StatusOK, points)
}

// GET /api/v1/accounts — list accounts for equity curve filter
func (h *apiHandler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if h.pgStore == nil {
		// Fall back to in-memory accounts
		ids := make([]string, 0, len(h.accounts))
		for id := range h.accounts {
			ids = append(ids, id)
		}
		writeJSON(w, http.StatusOK, ids)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := h.pgStore.Pool().Query(ctx, `SELECT DISTINCT account_id FROM account_snapshots ORDER BY account_id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query accounts: "+err.Error())
		return
	}
	defer rows.Close()

	ids := make([]string, 0, 10)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	writeJSON(w, http.StatusOK, ids)
}

// GET /api/v1/candles?symbol=BTC-USDT-SWAP&bar=1m&limit=300
func (h *apiHandler) handleCandles(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		writeError(w, http.StatusBadRequest, "symbol is required")
		return
	}

	bar := r.URL.Query().Get("bar")
	validBars := map[string]bool{"1m": true, "5m": true, "15m": true, "1H": true, "4H": true}
	if !validBars[bar] {
		bar = "1m"
	}

	limit := 300
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1500 {
			limit = n
		}
	}

	if h.pgStore == nil {
		writeError(w, http.StatusServiceUnavailable, "PG store not available")
		return
	}

	type candle struct {
		Time  int64   `json:"time"`
		Open  float64 `json:"open"`
		High  float64 `json:"high"`
		Low   float64 `json:"low"`
		Close float64 `json:"close"`
		Vol   float64 `json:"volume"`
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.pgStore.Pool().Query(ctx, `
		SELECT ts, o, h, l, c, vol
		FROM candles
		WHERE inst_id = $1 AND bar = $2
		ORDER BY ts DESC
		LIMIT $3
	`, symbol, bar, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query candles: "+err.Error())
		return
	}
	defer rows.Close()

	candles := make([]candle, 0, limit)
	for rows.Next() {
		var c candle
		if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Vol); err != nil {
			continue
		}
		// ts is in milliseconds, Lightweight Charts needs seconds
		c.Time = c.Time / 1000
		candles = append(candles, c)
	}

	// Reverse to ascending order
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}

	writeJSON(w, http.StatusOK, candles)
}

// GET /api/v1/symbols — list available trading symbols
func (h *apiHandler) handleSymbols(w http.ResponseWriter, r *http.Request) {
	if h.pgStore == nil {
		writeError(w, http.StatusServiceUnavailable, "PG store not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := h.pgStore.Pool().Query(ctx, `
		SELECT DISTINCT inst_id FROM candles ORDER BY inst_id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query symbols: "+err.Error())
		return
	}
	defer rows.Close()

	symbols := make([]string, 0, 50)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			continue
		}
		symbols = append(symbols, s)
	}

	writeJSON(w, http.StatusOK, symbols)
}
