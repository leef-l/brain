package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tradestore"
)

type apiHandler struct {
	qb             *quant.QuantBrain
	accounts       map[string]*quant.Account
	accountConfigs map[string]quant.AccountConfig // for initial_equity
	pgStore        *tradestore.PGStore
	fullConfig     *quant.FullConfig
	hub            *Hub
	logger         *slog.Logger
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
		AccountID     string  `json:"account_id"`
		Exchange      string  `json:"exchange"`
		Equity        float64 `json:"equity"`
		Available     float64 `json:"available"`
		Margin        float64 `json:"margin"`
		InitialEquity float64 `json:"initial_equity"`
	}

	type result struct {
		TotalEquity   float64      `json:"total_equity"`
		InitialEquity float64      `json:"initial_equity"`
		TotalMargin   float64      `json:"total_margin"`
		UnrealizedPnL float64      `json:"unrealized_pnl"`
		LockedPnL     float64      `json:"locked_pnl"`
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

	// Deduplicate exchange queries: accounts sharing the same API key
	// point to the same physical OKX account — query only once.
	type exData struct {
		balance   exchange.BalanceInfo
		positions []exchange.PositionInfo
	}
	apiKeyData := make(map[string]*exData)
	accountAPIKey := make(map[string]string)
	for id := range h.accounts {
		if ac, ok := h.accountConfigs[id]; ok && ac.APIKey != "" {
			accountAPIKey[id] = ac.APIKey
		}
	}
	for id, acct := range h.accounts {
		apiKey := accountAPIKey[id]
		effectiveKey := apiKey
		if effectiveKey == "" {
			effectiveKey = "_paper_" + id
		}
		if _, exists := apiKeyData[effectiveKey]; !exists {
			bal, err := acct.Exchange.QueryBalance(ctx)
			if err != nil {
				continue
			}
			positions, _ := acct.Exchange.QueryPositions(ctx)
			apiKeyData[effectiveKey] = &exData{balance: bal, positions: positions}
		}
	}

	// Track emitted positions to avoid duplicates across shared accounts.
	emittedPos := make(map[string]bool) // "key:symbol:side" → done

	for id, acct := range h.accounts {
		apiKey := accountAPIKey[id]
		effectiveKey := apiKey
		if effectiveKey == "" {
			effectiveKey = "_paper_" + id
		}
		ed := apiKeyData[effectiveKey]
		if ed == nil {
			continue
		}
		var initEquity float64
		if ac, ok := h.accountConfigs[id]; ok {
			initEquity = ac.InitialEquity
		}
		displayEquity := ed.balance.Equity
		if initEquity > 0 {
			displayEquity = initEquity
		}
		out.TotalEquity += displayEquity
		out.TotalMargin += ed.balance.Margin
		out.InitialEquity += initEquity
		out.Accounts = append(out.Accounts, accountOut{
			AccountID:     id,
			Exchange:      acct.Exchange.Name(),
			Equity:        displayEquity,
			Available:     ed.balance.Available,
			Margin:        ed.balance.Margin,
			InitialEquity: initEquity,
		})

		for _, p := range ed.positions {
			if p.Quantity <= 0 {
				continue
			}
			pk := effectiveKey + ":" + p.Symbol + ":" + p.Side
			if emittedPos[pk] {
				continue
			}

			// Check ownership: only emit under the account whose unit has a trade record.
			ownerID := ""
			var sl float64
			for _, u := range h.qb.Units() {
				uKey := accountAPIKey[u.Account.ID]
				if uKey == "" {
					uKey = "_paper_" + u.Account.ID
				}
				if uKey != effectiveKey {
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
						ownerID = u.Account.ID
						if tr.StopLoss > 0 {
							sl = tr.StopLoss
						}
						break
					}
				}
				if ownerID != "" {
					break
				}
			}
			if ownerID != "" && ownerID != id {
				continue // owned by a different logical account
			}
			emittedPos[pk] = true

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

			if sl > 0 && p.AvgPrice > 0 {
				var slPnl float64
				if p.Side == "long" {
					slPnl = (sl - p.AvgPrice) * p.Quantity
				} else {
					slPnl = (p.AvgPrice - sl) * p.Quantity
				}
				if slPnl > 0 {
					out.LockedPnL += slPnl
				}
			}
		}
	}
	out.TotalExposure = out.LongExposure + out.ShortExposure

	// Trade stats from all units.
	// DayPnL = today's realized (closed trades) + current unrealized.
	todayStart := time.Now().UTC().Truncate(24 * time.Hour)
	for _, u := range h.qb.Units() {
		todayStats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID, Since: todayStart})
		out.DayPnL += todayStats.TotalPnL

		// Count only closed trades for statistics (match WebSocket behavior).
		allTrades := u.TradeStore.Query(tradestore.Filter{UnitID: u.ID})
		for _, t := range allTrades {
			if t.ExitPrice <= 0 {
				continue // still open
			}
			out.TotalTrades++
			if t.PnL > 0 {
				out.Wins++
				out.AvgWin += t.PnL
			} else if t.PnL < 0 {
				out.Losses++
				out.AvgLoss += -t.PnL
			}
		}
	}
	out.DayPnL += out.UnrealizedPnL // add open position unrealized PnL
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

	// Deduplicate: shared API key accounts see the same positions.
	accountAPIKey := make(map[string]string)
	for id := range h.accounts {
		if ac, ok := h.accountConfigs[id]; ok && ac.APIKey != "" {
			accountAPIKey[id] = ac.APIKey
		}
	}

	type exPositions struct {
		positions []exchange.PositionInfo
	}
	apiKeyPositions := make(map[string]*exPositions)
	emitted := make(map[string]bool) // "key:symbol:side"

	for id, acct := range h.accounts {
		apiKey := accountAPIKey[id]
		effectiveKey := apiKey
		if effectiveKey == "" {
			effectiveKey = "_paper_" + id
		}
		if _, exists := apiKeyPositions[effectiveKey]; !exists {
			posList, err := acct.Exchange.QueryPositions(ctx)
			if err != nil {
				continue
			}
			apiKeyPositions[effectiveKey] = &exPositions{positions: posList}
		}
		ep := apiKeyPositions[effectiveKey]

		for _, p := range ep.positions {
			if p.Quantity <= 0 {
				continue
			}
			pk := effectiveKey + ":" + p.Symbol + ":" + p.Side
			if emitted[pk] {
				continue
			}

			// Find owner by trade record.
			ownerID := ""
			for _, u := range h.qb.Units() {
				uKey := accountAPIKey[u.Account.ID]
				if uKey == "" {
					uKey = "_paper_" + u.Account.ID
				}
				if uKey != effectiveKey {
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
						ownerID = u.Account.ID
						break
					}
				}
				if ownerID != "" {
					break
				}
			}
			if ownerID != "" && ownerID != id {
				continue
			}
			emitted[pk] = true

			mark := p.MarkPrice
			if mark <= 0 {
				mark = p.AvgPrice
			}

			displayID := id
			if ownerID != "" {
				displayID = ownerID
			}

			health := -1.0
			for _, u := range h.qb.Units() {
				if u.Account.ID == displayID {
					if v := h.qb.PositionHealth(u.ID + ":" + p.Symbol); v >= 0 {
						health = v
					}
				}
			}

			positions = append(positions, posOut{
				AccountID: displayID,
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
		Reason    string `json:"reason"` // "manual_close" or "batch_close"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Try the requested account first; if not found or position not there,
	// fall back to any account sharing the same API key. This handles the
	// case where multiple logical accounts share one OKX API key and the
	// WebUI assigned an unstable account_id to the position (Go map order).
	var target *exchange.PositionInfo
	var closeAcct *quant.Account
	var closeAcctID string

	tryClose := func(id string, acct *quant.Account) bool {
		positions, err := acct.Exchange.QueryPositions(ctx)
		if err != nil {
			return false
		}
		for i, p := range positions {
			if p.Symbol == req.Symbol && p.Quantity > 0 {
				target = &positions[i]
				closeAcct = acct
				closeAcctID = id
				return true
			}
		}
		return false
	}

	// 1. Try requested account
	if acct, ok := h.accounts[req.AccountID]; ok {
		tryClose(req.AccountID, acct)
	}

	// 2. Fallback: try all accounts sharing the same API key
	if target == nil {
		reqAPIKey := ""
		if ac, ok := h.accountConfigs[req.AccountID]; ok {
			reqAPIKey = ac.APIKey
		}
		for id, acct := range h.accounts {
			if id == req.AccountID {
				continue
			}
			// Match by same API key, or try all if no key info
			if reqAPIKey != "" {
				if ac, ok := h.accountConfigs[id]; ok && ac.APIKey == reqAPIKey {
					if tryClose(id, acct) {
						break
					}
				}
			}
		}
	}

	// 3. Last resort: try every account
	if target == nil {
		for id, acct := range h.accounts {
			if tryClose(id, acct) {
				break
			}
		}
	}

	if target == nil {
		writeError(w, http.StatusNotFound, "no open position for "+req.Symbol+" on any account")
		return
	}

	// Close it
	closeSide := "sell"
	if target.Side == "short" {
		closeSide = "buy"
	}

	result, err := closeAcct.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
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

	reason := req.Reason
	if reason == "" {
		reason = "manual_close"
	}

	h.logger.Info("webui close position",
		"account", closeAcctID, "symbol", req.Symbol,
		"side", target.Side, "qty", target.Quantity,
		"reason", reason,
		"status", result.Status, "fill_price", result.FillPrice)

	// Push trade event to WebSocket
	h.hub.broadcast(wsMessage{
		Type: "trade_event",
		Data: map[string]interface{}{
			"action":     reason,
			"account_id": closeAcctID,
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

// POST /api/v1/positions/close-all — force close ALL positions on the OKX exchange.
// Queries every account's positions and closes each one. Shared-API-key accounts
// are deduplicated so each physical position is only closed once.
// OKX demo may partially fill market orders, so we retry in a loop until empty.
func (h *apiHandler) handleCloseAllPositions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	type closeResult struct {
		Symbol    string  `json:"symbol"`
		Side      string  `json:"side"`
		Quantity  float64 `json:"quantity"`
		FillPrice float64 `json:"fill_price"`
		Status    string  `json:"status"`
		Error     string  `json:"error,omitempty"`
	}

	var results []closeResult

	// Pick one account per API key for closing.
	type acctRef struct {
		id   string
		acct *quant.Account
	}
	seenKeys := make(map[string]bool)
	var closeAccts []acctRef
	for id, acct := range h.accounts {
		apiKey := ""
		if ac, ok := h.accountConfigs[id]; ok {
			apiKey = ac.APIKey
		}
		ek := apiKey
		if ek == "" {
			ek = "_paper_" + id
		}
		if seenKeys[ek] {
			continue
		}
		seenKeys[ek] = true
		closeAccts = append(closeAccts, acctRef{id, acct})
	}

	// Retry loop: OKX demo partially fills market orders.
	const maxRounds = 30
	for round := 0; round < maxRounds; round++ {
		anyOpen := false
		for _, ar := range closeAccts {
			positions, err := ar.acct.Exchange.QueryPositions(ctx)
			if err != nil {
				continue
			}
			for _, p := range positions {
				if p.Quantity <= 0 {
					continue
				}
				anyOpen = true

				closeSide := "sell"
				if p.Side == "short" {
					closeSide = "buy"
				}

				result, err := ar.acct.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
					Symbol:     p.Symbol,
					Side:       closeSide,
					PosSide:    p.Side,
					Type:       "market",
					Price:      p.MarkPrice,
					Quantity:   p.Quantity,
					Leverage:   p.Leverage,
					ReduceOnly: true,
					ClientID:   fmt.Sprintf("force-close-%s-%d", p.Symbol, time.Now().UnixMilli()),
				})

				cr := closeResult{
					Symbol:   p.Symbol,
					Side:     p.Side,
					Quantity: p.Quantity,
				}
				if err != nil {
					cr.Error = err.Error()
					cr.Status = "failed"
					h.logger.Error("force close failed", "account", ar.id, "symbol", p.Symbol, "err", err)
				} else {
					cr.FillPrice = result.FillPrice
					cr.Status = result.Status
					h.logger.Info("force close ok", "account", ar.id, "symbol", p.Symbol,
						"side", p.Side, "qty", p.Quantity, "fill", result.FillPrice)
				}
				results = append(results, cr)
			}
		}
		if !anyOpen {
			break // all positions closed
		}
		// Brief pause before retrying (OKX demo partial fill).
		select {
		case <-ctx.Done():
			break
		case <-time.After(300 * time.Millisecond):
		}
	}

	// Push trade event
	h.hub.broadcast(wsMessage{
		Type: "trade_event",
		Data: map[string]interface{}{
			"action": "force_close_all",
			"closed": len(results),
		},
		TS: time.Now().UnixMilli(),
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"closed":  len(results),
		"results": results,
	})
}

// POST /api/v1/positions/sync — sync OKX real positions with our trade records.
// For each OKX position that has no matching open trade record, creates one.
// Positions that already have a trade record are left untouched.
func (h *apiHandler) handleSyncPositions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	type syncResult struct {
		Symbol   string `json:"symbol"`
		Side     string `json:"side"`
		Action   string `json:"action"` // "exists" or "created"
		TradeID  string `json:"trade_id,omitempty"`
		UnitID   string `json:"unit_id,omitempty"`
	}

	// Query each physical exchange only once.
	queried := make(map[string]bool) // apiKey → done
	var results []syncResult
	created := 0

	units := h.qb.Units()

	for id, acct := range h.accounts {
		apiKey := ""
		if ac, ok := h.accountConfigs[id]; ok {
			apiKey = ac.APIKey
		}
		if apiKey != "" && queried[apiKey] {
			continue
		}
		if apiKey != "" {
			queried[apiKey] = true
		}

		positions, err := acct.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}

		for _, p := range positions {
			if p.Quantity <= 0 {
				continue
			}

			// Check if any unit already has an open trade for this symbol.
			found := false
			for _, u := range units {
				openTrades := u.TradeStore.Query(tradestore.Filter{
					Symbol:   p.Symbol,
					OpenOnly: true,
					Limit:    1,
				})
				for _, tr := range openTrades {
					if tr.ExitPrice == 0 {
						results = append(results, syncResult{
							Symbol:  p.Symbol,
							Side:    p.Side,
							Action:  "exists",
							TradeID: tr.ID,
							UnitID:  u.ID,
						})
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				continue
			}

			// No trade record found — create one under the first matching unit.
			// Pick the unit whose account shares the same API key (or same account ID).
			var targetUnit *quant.TradingUnit
			for _, u := range units {
				if u.Account.ID == id {
					targetUnit = u
					break
				}
			}
			// Fallback: any unit sharing the same API key.
			if targetUnit == nil {
				for _, u := range units {
					uKey := ""
					if ac, ok := h.accountConfigs[u.Account.ID]; ok {
						uKey = ac.APIKey
					}
					if uKey == apiKey {
						targetUnit = u
						break
					}
				}
			}

			if targetUnit == nil || targetUnit.TradeStore == nil {
				results = append(results, syncResult{
					Symbol: p.Symbol,
					Side:   p.Side,
					Action: "no_unit",
				})
				continue
			}

			dir := strategy.DirectionLong
			if p.Side == "short" {
				dir = strategy.DirectionShort
			}

			tradeID := fmt.Sprintf("sync-%s-%s-%d", p.Symbol, p.Side, time.Now().UnixMilli())
			record := tradestore.TradeRecord{
				ID:         tradeID,
				AccountID:  targetUnit.Account.ID,
				UnitID:     targetUnit.ID,
				Symbol:     p.Symbol,
				Direction:  dir,
				EntryPrice: p.AvgPrice,
				Quantity:   p.Quantity,
				EntryTime:  time.Now(),
				Leverage:   p.Leverage,
				Strategy:   "synced",
			}
			if err := targetUnit.TradeStore.Save(ctx, record); err != nil {
				h.logger.Error("sync: save trade failed", "symbol", p.Symbol, "err", err)
				results = append(results, syncResult{
					Symbol: p.Symbol,
					Side:   p.Side,
					Action: "error",
				})
				continue
			}
			created++
			results = append(results, syncResult{
				Symbol:  p.Symbol,
				Side:    p.Side,
				Action:  "created",
				TradeID: tradeID,
				UnitID:  targetUnit.ID,
			})

			h.logger.Info("sync: created trade record",
				"symbol", p.Symbol, "side", p.Side,
				"unit", targetUnit.ID, "trade_id", tradeID)
		}
	}

	h.hub.broadcast(wsMessage{
		Type: "trade_event",
		Data: map[string]interface{}{
			"action":  "sync_positions",
			"total":   len(results),
			"created": created,
		},
		TS: time.Now().UnixMilli(),
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":   len(results),
		"created": created,
		"results": results,
	})
}

// GET /api/v1/trades?limit=50&since=2024-01-01
// POST /api/v1/trading/pause — pause new trade evaluation.
func (h *apiHandler) handlePause(w http.ResponseWriter, r *http.Request) {
	h.qb.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": true})
}

// POST /api/v1/trading/resume — resume trade evaluation.
func (h *apiHandler) handleResume(w http.ResponseWriter, r *http.Request) {
	h.qb.Resume()
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": false})
}

// GET /api/v1/trading/status
func (h *apiHandler) handleTradingStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": h.qb.IsPaused()})
}

func (h *apiHandler) handleTrades(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	type tradeOut struct {
		ID             string  `json:"id"`
		AccountID      string  `json:"account_id"`
		Symbol         string  `json:"symbol"`
		Direction      string  `json:"direction"`
		EntryPrice     float64 `json:"entry_price"`
		ExitPrice      float64 `json:"exit_price"`
		Quantity       float64 `json:"quantity"`
		PnL            float64 `json:"pnl"`
		PnLPct         float64 `json:"pnl_pct"`
		EntryTime      string  `json:"entry_time"`
		ExitTime       string  `json:"exit_time"`
		Reason         string  `json:"reason"`
		Strategy       string  `json:"strategy"`
		Leverage       int     `json:"leverage"`
		StopLoss     float64 `json:"stop_loss"`
		TakeProfit   float64 `json:"take_profit"`
		OrigStopLoss float64 `json:"orig_stop_loss"`
		Notional     float64 `json:"notional"`
		Margin         float64 `json:"margin"`
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
			notional := rec.Quantity * rec.EntryPrice
			var margin float64
			if rec.Leverage > 0 {
				margin = notional / float64(rec.Leverage)
			}
			trades = append(trades, tradeOut{
				ID:             rec.ID,
				AccountID:      rec.AccountID,
				Symbol:         rec.Symbol,
				Direction:      string(rec.Direction),
				EntryPrice:     rec.EntryPrice,
				ExitPrice:      rec.ExitPrice,
				Quantity:       rec.Quantity,
				PnL:            rec.PnL,
				PnLPct:         rec.PnLPct,
				EntryTime:      rec.EntryTime.Format(time.RFC3339),
				ExitTime:       exitTime,
				Reason:         rec.Reason,
				Strategy:       rec.Strategy,
				Leverage:       rec.Leverage,
				StopLoss:       rec.StopLoss,
				TakeProfit:     rec.TakeProfit,
				OrigStopLoss: rec.OrigStopLoss,
				Notional:       notional,
				Margin:         margin,
			})
		}
	}

	// Sort by exit time desc (most recent first) — trades come from multiple units
	// Simple approach: they're already ordered within each unit query
	writeJSON(w, http.StatusOK, trades)
}

// GET /api/v1/equity-curve?days=1&account=paper-main
//
// Builds the equity curve from two sources:
//  1. trade_records: each closed trade generates a point (cumulative PnL from initial equity)
//  2. account_snapshots: real-time equity snapshots (every ~30s)
//
// The two are merged by time, deduplicated, and downsampled if needed.
// This ensures the curve is meaningful even when the system just started
// and snapshots only cover a few hours.
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

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	since := time.Now().AddDate(0, 0, -days)
	points := make([]equityPoint, 0, 600)

	// --- Source 1: trade_records → cumulative PnL curve ---
	// Get initial equity from config (first snapshot or account config).
	var initialEquity float64
	{
		var ieQuery string
		var ieArgs []interface{}
		if account != "" && account != "all" {
			ieQuery = `SELECT equity FROM account_snapshots WHERE account_id = $1 ORDER BY created_at ASC LIMIT 1`
			ieArgs = []interface{}{account}
		} else {
			ieQuery = `SELECT SUM(equity) FROM account_snapshots GROUP BY created_at ORDER BY created_at ASC LIMIT 1`
			ieArgs = nil
		}
		row := h.pgStore.Pool().QueryRow(ctx, ieQuery, ieArgs...)
		if err := row.Scan(&initialEquity); err != nil || initialEquity <= 0 {
			initialEquity = 10000 // fallback default
		}
	}

	// Query trade PnLs in time range, ordered by exit_time.
	{
		var tQuery string
		var tArgs []interface{}
		if account != "" && account != "all" {
			tQuery = `
				SELECT exit_time, pnl
				FROM trade_records
				WHERE account_id = $1 AND exit_time IS NOT NULL AND exit_time >= $2
				ORDER BY exit_time ASC
			`
			tArgs = []interface{}{account, since}
		} else {
			tQuery = `
				SELECT exit_time, pnl
				FROM trade_records
				WHERE exit_time IS NOT NULL AND exit_time >= $1
				ORDER BY exit_time ASC
			`
			tArgs = []interface{}{since}
		}

		rows, err := h.pgStore.Pool().Query(ctx, tQuery, tArgs...)
		if err == nil {
			defer rows.Close()

			// Get cumulative PnL before the window to set correct baseline.
			var priorPnL float64
			{
				var pQuery string
				var pArgs []interface{}
				if account != "" && account != "all" {
					pQuery = `SELECT COALESCE(SUM(pnl), 0) FROM trade_records WHERE account_id = $1 AND exit_time IS NOT NULL AND exit_time < $2`
					pArgs = []interface{}{account, since}
				} else {
					pQuery = `SELECT COALESCE(SUM(pnl), 0) FROM trade_records WHERE exit_time IS NOT NULL AND exit_time < $1`
					pArgs = []interface{}{since}
				}
				_ = h.pgStore.Pool().QueryRow(ctx, pQuery, pArgs...).Scan(&priorPnL)
			}

			cumPnL := priorPnL
			// Add starting point
			points = append(points, equityPoint{
				Time:   since.Format(time.RFC3339),
				Equity: initialEquity + cumPnL,
				Source: "trade",
			})

			for rows.Next() {
				var t time.Time
				var pnl float64
				if err := rows.Scan(&t, &pnl); err != nil {
					continue
				}
				cumPnL += pnl
				points = append(points, equityPoint{
					Time:   t.Format(time.RFC3339),
					Equity: initialEquity + cumPnL,
					Source: "trade",
				})
			}
			rows.Close()
		}
	}

	// --- Source 2: account_snapshots (real-time) ---
	{
		var sQuery string
		var sArgs []interface{}
		if account != "" && account != "all" {
			sQuery = `
				SELECT created_at, equity
				FROM account_snapshots
				WHERE account_id = $1 AND created_at >= $2
				ORDER BY created_at ASC
			`
			sArgs = []interface{}{account, since}
		} else {
			sQuery = `
				SELECT created_at, SUM(equity) as equity
				FROM account_snapshots
				WHERE created_at >= $1
				GROUP BY created_at
				ORDER BY created_at ASC
			`
			sArgs = []interface{}{since}
		}

		rows, err := h.pgStore.Pool().Query(ctx, sQuery, sArgs...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var t time.Time
				var eq float64
				if err := rows.Scan(&t, &eq); err != nil {
					continue
				}
				points = append(points, equityPoint{
					Time:   t.Format(time.RFC3339),
					Equity: eq,
					Source: "snapshot",
				})
			}
			rows.Close()
		}
	}

	// --- Merge: sort by time, prefer snapshots over trades when close ---
	// Sort all points by time.
	sortPoints(points)

	// Deduplicate: if snapshot and trade points are within 60s, keep snapshot.
	deduped := deduplicatePoints(points)

	// Downsample if too many points (keep under 500).
	if len(deduped) > 500 {
		step := len(deduped) / 500
		sampled := make([]equityPoint, 0, 500)
		for i := 0; i < len(deduped); i += step {
			sampled = append(sampled, deduped[i])
		}
		if len(sampled) > 0 && sampled[len(sampled)-1].Time != deduped[len(deduped)-1].Time {
			sampled = append(sampled, deduped[len(deduped)-1])
		}
		deduped = sampled
	}

	writeJSON(w, http.StatusOK, deduped)
}

// GET /api/v1/accounts — list accounts for equity curve filter.
// Always includes in-memory accounts (from config) and merges with
// any additional accounts found in account_snapshots history.
func (h *apiHandler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	// Start with in-memory accounts (always available).
	idSet := make(map[string]bool, len(h.accounts))
	for id := range h.accounts {
		idSet[id] = true
	}

	// Merge accounts from PG snapshots (may include historical accounts).
	if h.pgStore != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		rows, err := h.pgStore.Pool().Query(ctx, `SELECT DISTINCT account_id FROM account_snapshots ORDER BY account_id`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					continue
				}
				idSet[id] = true
			}
			rows.Close()
		}
	}

	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
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

// GET /api/v1/symbols — list trading symbols ranked by 24h amplitude (Top20)
//
// Returns [{symbol, change_pct, rank}] sorted by 24h amplitude descending.
// Uses 1H candles from the last 24h to compute (max_high - min_low) / first_open.
func (h *apiHandler) handleSymbols(w http.ResponseWriter, r *http.Request) {
	if h.pgStore == nil {
		writeError(w, http.StatusServiceUnavailable, "PG store not available")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Calculate 24h amplitude from 1H candles for each symbol.
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	rows, err := h.pgStore.Pool().Query(ctx, `
		SELECT inst_id,
		       MAX(h) AS high_24h,
		       MIN(l) AS low_24h,
		       (array_agg(c ORDER BY ts DESC))[1] AS last_price,
		       (array_agg(o ORDER BY ts ASC))[1]  AS open_24h
		FROM candles
		WHERE bar = '1H' AND ts >= $1
		GROUP BY inst_id
		HAVING COUNT(*) >= 2
	`, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query symbols: "+err.Error())
		return
	}
	defer rows.Close()

	type symbolInfo struct {
		Symbol    string  `json:"symbol"`
		ChangePct float64 `json:"change_pct"` // 24h amplitude %
		PricePct  float64 `json:"price_pct"`  // 24h price change %
		Rank      int     `json:"rank"`
	}

	var symbols []symbolInfo
	for rows.Next() {
		var instID string
		var high24h, low24h, lastPrice, open24h float64
		if err := rows.Scan(&instID, &high24h, &low24h, &lastPrice, &open24h); err != nil {
			continue
		}
		if low24h <= 0 || open24h <= 0 {
			continue
		}
		amplitude := (high24h - low24h) / low24h * 100
		priceChange := (lastPrice - open24h) / open24h * 100
		symbols = append(symbols, symbolInfo{
			Symbol:    instID,
			ChangePct: amplitude,
			PricePct:  priceChange,
		})
	}

	// Sort by amplitude descending (most volatile first).
	sort.Slice(symbols, func(i, j int) bool {
		return symbols[i].ChangePct > symbols[j].ChangePct
	})

	// Assign rank and cap at top 50.
	limit := 50
	if len(symbols) < limit {
		limit = len(symbols)
	}
	result := symbols[:limit]
	for i := range result {
		result[i].Rank = i + 1
	}

	writeJSON(w, http.StatusOK, result)
}

// --- equity curve helpers ---

type equityPoint struct {
	Time   string  `json:"time"`
	Equity float64 `json:"equity"`
	Source string  `json:"source,omitempty"`
}

func sortPoints(pts []equityPoint) {
	sort.Slice(pts, func(i, j int) bool {
		return pts[i].Time < pts[j].Time
	})
}

// deduplicatePoints merges trade-derived and snapshot points.
// When a snapshot and trade point are within 60s, keep the snapshot
// (it's more accurate as it includes unrealized PnL).
func deduplicatePoints(pts []equityPoint) []equityPoint {
	if len(pts) == 0 {
		return pts
	}

	result := make([]equityPoint, 0, len(pts))
	result = append(result, pts[0])

	for i := 1; i < len(pts); i++ {
		prev := result[len(result)-1]
		cur := pts[i]

		// Parse times for proximity check
		tPrev, _ := time.Parse(time.RFC3339, prev.Time)
		tCur, _ := time.Parse(time.RFC3339, cur.Time)

		if tCur.Sub(tPrev).Abs() < 60*time.Second {
			// Within 60s — prefer snapshot over trade
			if cur.Source == "snapshot" && prev.Source == "trade" {
				result[len(result)-1] = cur
			}
			// Otherwise keep the existing one
			continue
		}
		result = append(result, cur)
	}
	return result
}

// GET /api/v1/strategy-info — returns strategy params with Chinese descriptions
func (h *apiHandler) handleStrategyInfo(w http.ResponseWriter, r *http.Request) {
	type paramInfo struct {
		Key     string      `json:"key"`
		Name    string      `json:"name"`
		Desc    string      `json:"desc"`
		Value   interface{} `json:"value"`
		Default interface{} `json:"default"`
	}
	type strategyInfo struct {
		Name       string      `json:"name"`
		NameZH     string      `json:"name_zh"`
		Desc       string      `json:"desc"`
		Timeframes []string    `json:"timeframes"`
		Params     []paramInfo `json:"params"`
	}

	// Read actual params from the first unit's pool, or use defaults.
	var tf, mr, bm, of interface{}
	units := h.qb.Units()
	if len(units) > 0 {
		for _, s := range units[0].Pool.Strategies() {
			switch v := s.(type) {
			case strategy.TrendFollower:
				tf = v.Params
			case strategy.MeanReversion:
				mr = v.Params
			case strategy.BreakoutMomentum:
				bm = v.Params
			case strategy.OrderFlow:
				of = v.Params
			}
		}
	}
	tfp, _ := tf.(strategy.TrendFollowerParams)
	mrp, _ := mr.(strategy.MeanReversionParams)
	bmp, _ := bm.(strategy.BreakoutMomentumParams)
	ofp, _ := of.(strategy.OrderFlowParams)

	// Defaults for comparison
	dtf := strategy.DefaultTrendFollowerParams()
	dmr := strategy.DefaultMeanReversionParams()
	dbm := strategy.DefaultBreakoutMomentumParams()
	dof := strategy.DefaultOrderFlowParams()

	// Use actual unit timeframe instead of hardcoded values.
	actualTF := "1H"
	if len(units) > 0 {
		actualTF = units[0].Timeframe
		if actualTF == "" {
			actualTF = "1H"
		}
	}
	htf := actualTF
	switch actualTF {
	case "1m":
		htf = "5m"
	case "5m":
		htf = "15m"
	case "15m":
		htf = "1H"
	case "1H":
		htf = "4H"
	case "4H":
		htf = "1D"
	}

	infos := []strategyInfo{
		{
			Name: "TrendFollower", NameZH: "趋势跟踪",
			Desc:       "基于 EMA 多周期排列 + ADX 趋势强度 + MACD 确认的趋势跟随策略。适合单边行情，在 EMA9>EMA21>EMA55 且 ADX 确认趋势时开仓。",
			Timeframes: []string{actualTF, htf},
			Params: []paramInfo{
				{Key: "adx_threshold", Name: "ADX 阈值", Desc: "最低 ADX 值，低于此值视为无趋势（ADX 范围 0~1，归一化后）", Value: tfp.ADXThreshold, Default: dtf.ADXThreshold},
			},
		},
		{
			Name: "MeanReversion", NameZH: "均值回归",
			Desc:       "顺势回调入场策略：读取高一级周期趋势方向，等价格回踩布林带支撑/阻力位后顺势进场。上涨趋势中买回调，下跌趋势中卖反弹。",
			Timeframes: []string{actualTF, htf},
			Params: []paramInfo{
				{Key: "bb_oversold", Name: "BB 超卖线", Desc: "布林带位置低于此值判定超卖（0=下轨, 0.5=中轨, 1=上轨）", Value: mrp.BBOversold, Default: dmr.BBOversold},
				{Key: "bb_overbought", Name: "BB 超买线", Desc: "布林带位置高于此值判定超买", Value: mrp.BBOverbought, Default: dmr.BBOverbought},
				{Key: "max_volume_ratio", Name: "最大量比", Desc: "成交量/均量比不超过此值才触发（排除放量突破行情）", Value: mrp.MaxVolumeRatio, Default: dmr.MaxVolumeRatio},
			},
		},
		{
			Name: "BreakoutMomentum", NameZH: "突破动量",
			Desc:       "基于价格突破近期高低点 + 量能扩张 + OBV 确认的突破策略。抓住放量突破后的短期冲击波，结合动量强度分级确认。",
			Timeframes: []string{actualTF, htf},
			Params: []paramInfo{
				{Key: "volume_ratio_threshold", Name: "量比阈值", Desc: "成交量/均量比超过此值视为量能扩张", Value: bmp.VolumeRatioThreshold, Default: dbm.VolumeRatioThreshold},
				{Key: "momentum_threshold", Name: "动量阈值", Desc: "10根K线价格变化率（百分比），低于此值时需量能配合", Value: bmp.MomentumThreshold, Default: dbm.MomentumThreshold},
				{Key: "strong_momentum", Name: "强动量阈值", Desc: "超过此值时单独触发（无需量能确认），代表极强单边行情", Value: bmp.StrongMomentum, Default: dbm.StrongMomentum},
			},
		},
		{
			Name: "OrderFlow", NameZH: "订单流",
			Desc:       "基于订单簿失衡、成交流毒性、大单比例、买卖比的微观结构策略。适合短线快进快出，通过多维度打分判断主力资金方向。",
			Timeframes: []string{"tick"},
			Params: []paramInfo{
				{Key: "imbalance_threshold", Name: "失衡阈值", Desc: "订单簿买卖失衡绝对值超过此值才计分（-1~1，正=买压大）", Value: ofp.ImbalanceThreshold, Default: dof.ImbalanceThreshold},
				{Key: "toxicity_threshold", Name: "毒性阈值", Desc: "成交流毒性超过此值才计分（0~1，高=知情交易者占比大）", Value: ofp.ToxicityThreshold, Default: dof.ToxicityThreshold},
				{Key: "flow_score_threshold", Name: "流向分阈值", Desc: "多维度加权评分超过此值才触发信号（越高越严格）", Value: ofp.FlowScoreThreshold, Default: dof.FlowScoreThreshold},
			},
		},
	}

	writeJSON(w, http.StatusOK, infos)
}

// ────────────────────────────────────────────────────────────────
// Configuration management APIs
// ────────────────────────────────────────────────────────────────

// configResponse is the JSON shape returned by GET /api/v1/config.
// It flattens FullConfig into a UI-friendly structure with field descriptions.
type configResponse struct {
	ConfigPath string                `json:"config_path"`
	CanSave    bool                  `json:"can_save"`
	Accounts   []accountConfigView   `json:"accounts"`
	Units      []unitConfigView      `json:"units"`
	Strategy   strategyConfigView    `json:"strategy"`
	Risk       riskConfigView        `json:"risk"`
	SignalExit signalExitConfigView  `json:"signal_exit"`
	Trailing   trailingConfigView    `json:"trailing_stop"`
}

type accountConfigView struct {
	ID            string   `json:"id"`
	Exchange      string   `json:"exchange"`
	APIKey        string   `json:"api_key"`
	SecretKey     string   `json:"secret_key"`
	Passphrase    string   `json:"passphrase"`
	BaseURL       string   `json:"base_url"`
	Simulated     bool     `json:"simulated"`
	InitialEquity float64  `json:"initial_equity"`
	SlippageBps   float64  `json:"slippage_bps"`
	FeeBps        float64  `json:"fee_bps"`
	Tags          []string `json:"tags"`
}

type unitConfigView struct {
	ID          string              `json:"id"`
	AccountID   string              `json:"account_id"`
	Symbols     []string            `json:"symbols"`
	Timeframe   string              `json:"timeframe"`
	MaxLeverage int                 `json:"max_leverage"`
	Enabled     bool                `json:"enabled"`
	Strategy    *strategyConfigView `json:"strategy,omitempty"`
	Risk        *riskConfigView     `json:"risk,omitempty"`
}

type strategyConfigView struct {
	Weights              map[string]float64 `json:"weights"`
	LongThreshold        float64            `json:"long_threshold"`
	ShortThreshold       float64            `json:"short_threshold"`
	DominanceFactor      float64            `json:"dominance_factor"`
	MinActiveStrategies  int                `json:"min_active_strategies"`
	HighConfidenceBypass float64            `json:"high_confidence_bypass"`

	TrendFollower    strategy.TrendFollowerParams     `json:"trend_follower"`
	MeanReversion    strategy.MeanReversionParams     `json:"mean_reversion"`
	BreakoutMomentum strategy.BreakoutMomentumParams  `json:"breakout_momentum"`
	OrderFlow        strategy.OrderFlowParams         `json:"order_flow"`
}

type riskConfigView struct {
	Guard quant.GuardConfig `json:"guard"`
	Sizer quant.SizerConfig `json:"position_sizer"`
}

type signalExitConfigView struct {
	Enabled              bool    `json:"enabled"`
	MinConfidence        float64 `json:"min_confidence"`
	RequireMultiStrategy int     `json:"require_multi_strategy"`
	MinHoldDurationSec   float64 `json:"min_hold_duration_sec"`
	CooldownAfterExitSec float64 `json:"cooldown_after_exit_sec"`
}

type trailingConfigView struct {
	Enabled                bool    `json:"enabled"`
	ActivationPct          float64 `json:"activation_pct"`
	CallbackPct            float64 `json:"callback_pct"`
	StepPct                float64 `json:"step_pct"`
	MaxLossWithoutTrailing float64 `json:"max_loss_without_trailing"`
}

func strategyToView(sc quant.StrategyConfig) strategyConfigView {
	return strategyConfigView{
		Weights:              sc.Weights,
		LongThreshold:        sc.LongThreshold,
		ShortThreshold:       sc.ShortThreshold,
		DominanceFactor:      sc.DominanceFactor,
		MinActiveStrategies:  sc.MinActiveStrategies,
		HighConfidenceBypass: sc.HighConfidenceBypass,
		TrendFollower:        sc.TrendFollower,
		MeanReversion:        sc.MeanReversion,
		BreakoutMomentum:     sc.BreakoutMomentum,
		OrderFlow:            sc.OrderFlow,
	}
}

func riskToView(rc quant.RiskConfig) riskConfigView {
	return riskConfigView{Guard: rc.Guard, Sizer: rc.Sizer}
}

func viewToStrategy(v strategyConfigView) quant.StrategyConfig {
	return quant.StrategyConfig{
		Weights:              v.Weights,
		LongThreshold:        v.LongThreshold,
		ShortThreshold:       v.ShortThreshold,
		DominanceFactor:      v.DominanceFactor,
		MinActiveStrategies:  v.MinActiveStrategies,
		HighConfidenceBypass: v.HighConfidenceBypass,
		TrendFollower:        v.TrendFollower,
		MeanReversion:        v.MeanReversion,
		BreakoutMomentum:     v.BreakoutMomentum,
		OrderFlow:            v.OrderFlow,
	}
}

func viewToRisk(v riskConfigView) quant.RiskConfig {
	return quant.RiskConfig{Guard: v.Guard, Sizer: v.Sizer}
}

// maskSecret replaces all but the last 4 characters with asterisks.
func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

// GET /api/v1/config
func (h *apiHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if h.fullConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "config not available")
		return
	}
	fc := h.fullConfig

	resp := configResponse{
		ConfigPath: fc.ConfigPath,
		CanSave:    fc.ConfigPath != "",
	}

	// Accounts — mask secrets
	for _, ac := range fc.Accounts {
		resp.Accounts = append(resp.Accounts, accountConfigView{
			ID:            ac.ID,
			Exchange:      ac.Exchange,
			APIKey:        maskSecret(ac.APIKey),
			SecretKey:     maskSecret(ac.SecretKey),
			Passphrase:    maskSecret(ac.Passphrase),
			BaseURL:       ac.BaseURL,
			Simulated:     ac.Simulated,
			InitialEquity: ac.InitialEquity,
			SlippageBps:   ac.SlippageBps,
			FeeBps:        ac.FeeBps,
			Tags:          ac.Tags,
		})
	}

	// Units
	for _, uc := range fc.Units {
		uv := unitConfigView{
			ID:          uc.ID,
			AccountID:   uc.AccountID,
			Symbols:     uc.Symbols,
			Timeframe:   uc.Timeframe,
			MaxLeverage: uc.MaxLeverage,
			Enabled:     uc.Enabled,
		}
		if uc.Strategy != nil {
			sv := strategyToView(*uc.Strategy)
			uv.Strategy = &sv
		}
		if uc.Risk != nil {
			rv := riskToView(*uc.Risk)
			uv.Risk = &rv
		}
		resp.Units = append(resp.Units, uv)
	}

	// Global strategy/risk
	resp.Strategy = strategyToView(fc.Strategy)
	resp.Risk = riskToView(fc.Risk)

	resp.SignalExit = signalExitConfigView{
		Enabled:              fc.SignalExit.Enabled,
		MinConfidence:        fc.SignalExit.MinConfidence,
		RequireMultiStrategy: fc.SignalExit.RequireMultiStrategy,
		MinHoldDurationSec:   fc.SignalExit.MinHoldDuration.Seconds(),
		CooldownAfterExitSec: fc.SignalExit.CooldownAfterExit.Seconds(),
	}
	resp.Trailing = trailingConfigView{
		Enabled:                fc.TrailingStop.Enabled,
		ActivationPct:          fc.TrailingStop.ActivationPct,
		CallbackPct:            fc.TrailingStop.CallbackPct,
		StepPct:                fc.TrailingStop.StepPct,
		MaxLossWithoutTrailing: fc.TrailingStop.MaxLossWithoutTrailing,
	}

	writeJSON(w, http.StatusOK, resp)
}

// configUpdateRequest is the JSON body for PUT /api/v1/config.
type configUpdateRequest struct {
	Accounts   *[]accountConfigUpdate `json:"accounts,omitempty"`
	Units      *[]unitConfigUpdate    `json:"units,omitempty"`
	Strategy   *strategyConfigView    `json:"strategy,omitempty"`
	Risk       *riskConfigView        `json:"risk,omitempty"`
	SignalExit *signalExitConfigView  `json:"signal_exit,omitempty"`
	Trailing   *trailingConfigView    `json:"trailing_stop,omitempty"`
}

type accountConfigUpdate struct {
	ID            string   `json:"id"`
	Exchange      string   `json:"exchange"`
	APIKey        string   `json:"api_key"`
	SecretKey     string   `json:"secret_key"`
	Passphrase    string   `json:"passphrase"`
	BaseURL       string   `json:"base_url"`
	Simulated     bool     `json:"simulated"`
	InitialEquity float64  `json:"initial_equity"`
	SlippageBps   float64  `json:"slippage_bps"`
	FeeBps        float64  `json:"fee_bps"`
	Tags          []string `json:"tags"`
}

type unitConfigUpdate struct {
	ID          string              `json:"id"`
	AccountID   string              `json:"account_id"`
	Symbols     []string            `json:"symbols"`
	Timeframe   string              `json:"timeframe"`
	MaxLeverage int                 `json:"max_leverage"`
	Enabled     bool                `json:"enabled"`
	Strategy    *strategyConfigView `json:"strategy,omitempty"`
	Risk        *riskConfigView     `json:"risk,omitempty"`
}

// PUT /api/v1/config
func (h *apiHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if h.fullConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "config not available")
		return
	}

	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	fc := h.fullConfig

	// Update accounts
	if req.Accounts != nil {
		newAccounts := make([]quant.AccountConfig, 0, len(*req.Accounts))
		for _, au := range *req.Accounts {
			ac := quant.AccountConfig{
				ID:            au.ID,
				Exchange:      au.Exchange,
				BaseURL:       au.BaseURL,
				Simulated:     au.Simulated,
				InitialEquity: au.InitialEquity,
				SlippageBps:   au.SlippageBps,
				FeeBps:        au.FeeBps,
				Tags:          au.Tags,
			}
			// Preserve existing secrets if masked values are sent back.
			existing := findAccount(fc.Accounts, au.ID)
			if strings.Contains(au.APIKey, "*") && existing != nil {
				ac.APIKey = existing.APIKey
			} else {
				ac.APIKey = au.APIKey
			}
			if strings.Contains(au.SecretKey, "*") && existing != nil {
				ac.SecretKey = existing.SecretKey
			} else {
				ac.SecretKey = au.SecretKey
			}
			if strings.Contains(au.Passphrase, "*") && existing != nil {
				ac.Passphrase = existing.Passphrase
			} else {
				ac.Passphrase = au.Passphrase
			}
			newAccounts = append(newAccounts, ac)
		}
		fc.Accounts = newAccounts
	}

	// Update units
	if req.Units != nil {
		newUnits := make([]quant.UnitConfig, 0, len(*req.Units))
		for _, uu := range *req.Units {
			uc := quant.UnitConfig{
				ID:          uu.ID,
				AccountID:   uu.AccountID,
				Symbols:     uu.Symbols,
				Timeframe:   uu.Timeframe,
				MaxLeverage: uu.MaxLeverage,
				Enabled:     uu.Enabled,
			}
			if uu.Strategy != nil {
				sc := viewToStrategy(*uu.Strategy)
				uc.Strategy = &sc
			}
			if uu.Risk != nil {
				rc := viewToRisk(*uu.Risk)
				uc.Risk = &rc
			}
			newUnits = append(newUnits, uc)
		}
		fc.Units = newUnits
	}

	// Update global strategy
	if req.Strategy != nil {
		fc.Strategy = viewToStrategy(*req.Strategy)
	}

	// Update global risk
	if req.Risk != nil {
		fc.Risk = viewToRisk(*req.Risk)
	}

	// Update signal exit
	if req.SignalExit != nil {
		fc.SignalExit.Enabled = req.SignalExit.Enabled
		fc.SignalExit.MinConfidence = req.SignalExit.MinConfidence
		fc.SignalExit.RequireMultiStrategy = req.SignalExit.RequireMultiStrategy
		fc.SignalExit.MinHoldDuration = time.Duration(req.SignalExit.MinHoldDurationSec * float64(time.Second))
		fc.SignalExit.CooldownAfterExit = time.Duration(req.SignalExit.CooldownAfterExitSec * float64(time.Second))
		h.qb.SetSignalExitConfig(fc.SignalExit)
	}

	// Update trailing stop
	if req.Trailing != nil {
		fc.TrailingStop.Enabled = req.Trailing.Enabled
		fc.TrailingStop.ActivationPct = req.Trailing.ActivationPct
		fc.TrailingStop.CallbackPct = req.Trailing.CallbackPct
		fc.TrailingStop.StepPct = req.Trailing.StepPct
		fc.TrailingStop.MaxLossWithoutTrailing = req.Trailing.MaxLossWithoutTrailing
		h.qb.SetTrailingStopConfig(fc.TrailingStop)
	}

	// Save to file
	if err := fc.SaveConfig(); err != nil {
		h.logger.Warn("config save failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"saved":   false,
			"message": "配置已更新到内存，但保存文件失败: " + err.Error(),
		})
		return
	}

	h.logger.Info("config updated and saved", "path", fc.ConfigPath)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"saved":   true,
		"message": "配置已保存",
	})
}

// GET /api/v1/config/defaults — returns default values for all config sections.
func (h *apiHandler) handleConfigDefaults(w http.ResponseWriter, r *http.Request) {
	type defaults struct {
		Strategy strategyConfigView `json:"strategy"`
		Risk     riskConfigView     `json:"risk"`
	}
	resp := defaults{
		Strategy: strategyConfigView{
			Weights:          strategy.DefaultWeights(),
			LongThreshold:    0.28,
			ShortThreshold:   0.28,
			DominanceFactor:  1.3,
			TrendFollower:    strategy.DefaultTrendFollowerParams(),
			MeanReversion:    strategy.DefaultMeanReversionParams(),
			BreakoutMomentum: strategy.DefaultBreakoutMomentumParams(),
			OrderFlow:        strategy.DefaultOrderFlowParams(),
		},
		Risk: riskConfigView{
			Guard: quant.GuardConfig{
				MaxSinglePositionPct:   5,
				MaxLeverage:            20,
				MinStopDistanceATR:     0.5,
				MaxStopDistancePct:     10,
				MaxConcurrentPositions: 10,
				MaxTotalExposurePct:    40,
				MaxSameDirectionPct:    20,
				StopNewTradesLossPct:   3,
				LiquidateAllLossPct:    5,
			},
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func findAccount(accounts []quant.AccountConfig, id string) *quant.AccountConfig {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}
