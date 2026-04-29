// Package sidecar implements the quant brain's sidecar interface layer.
//
// It wraps QuantBrain's runtime state as tool.Tool implementations that can
// be called by the Kernel via the specialist.call_tool path, or by the
// central brain via subtask.delegate → brain/execute.
//
// Tool inventory (Doc 37 §13, Doc 35 §5.4):
//
//	quant.global_portfolio   — cross-account portfolio snapshot
//	quant.global_risk_status — global risk guard thresholds & state
//	quant.strategy_weights   — per-strategy weights (regime-aware)
//	quant.daily_pnl          — per-unit daily PnL summary
//	quant.account_status     — single account balance + positions
//	quant.pause_trading      — pause all trading units
//	quant.resume_trading     — resume all trading units
//	quant.account_pause      — pause a single account's units
//	quant.account_resume     — resume a single account's units
//	quant.account_close_all  — close all positions for an account
//	quant.force_close        — force close a specific symbol position
//	quant.trace_query        — query signal audit traces
//	quant.trade_history      — query historical trade records
//	quant.backtest_start     — run a backtest on historical candles
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/backtest"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/strategy"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// quant.global_portfolio
// ---------------------------------------------------------------------------

type globalPortfolioTool struct {
	qb       *quant.QuantBrain
	accounts map[string]*quant.Account
}

func newGlobalPortfolioTool(qb *quant.QuantBrain, accounts map[string]*quant.Account) tool.Tool {
	return &globalPortfolioTool{qb: qb, accounts: accounts}
}

func (t *globalPortfolioTool) Name() string { return "quant.global_portfolio" }
func (t *globalPortfolioTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *globalPortfolioTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.global_portfolio",
		Description: "查询跨账户全局投资组合状态，包含所有账户的总权益、持仓、敞口、日PnL。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *globalPortfolioTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	type positionOut struct {
		AccountID string  `json:"account_id"`
		Symbol    string  `json:"symbol"`
		Side      string  `json:"side"`
		Quantity  float64 `json:"quantity"`
		AvgPrice  float64 `json:"avg_price"`
		MarkPrice float64 `json:"mark_price"`
		Notional  float64 `json:"notional"`
		Margin    float64 `json:"margin"`
		PnL       float64 `json:"unrealized_pnl"`
	}

	type accountSummary struct {
		AccountID  string  `json:"account_id"`
		Exchange   string  `json:"exchange"`
		Equity     float64 `json:"equity"`
		Available  float64 `json:"available"`
		Margin     float64 `json:"margin"`
		PnL        float64 `json:"unrealized_pnl"`
		Positions  int     `json:"positions"`
	}

	var (
		totalEquity    float64
		totalMargin    float64
		totalPnL       float64
		allPositions   []positionOut
		accountSummaries []accountSummary
	)

	for id, acc := range t.accounts {
		bal, err := acc.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		positions, err := acc.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}

		totalEquity += bal.Equity
		totalMargin += bal.Margin
		totalPnL += bal.UnrealizedPL

		accountSummaries = append(accountSummaries, accountSummary{
			AccountID: id,
			Exchange:  acc.Exchange.Name(),
			Equity:    bal.Equity,
			Available: bal.Available,
			Margin:    bal.Margin,
			PnL:       bal.UnrealizedPL,
			Positions: len(positions),
		})

		for _, p := range positions {
			allPositions = append(allPositions, positionOut{
				AccountID: id,
				Symbol:    p.Symbol,
				Side:      p.Side,
				Quantity:  p.Quantity,
				AvgPrice:  p.AvgPrice,
				MarkPrice: p.MarkPrice,
				Notional:  p.Notional,
				Margin:    p.Margin,
				PnL:       p.UnrealizedPL,
			})
		}
	}

	out := map[string]any{
		"total_equity":    totalEquity,
		"total_margin":    totalMargin,
		"total_unrealized_pnl": totalPnL,
		"accounts":        accountSummaries,
		"positions":       allPositions,
		"units":           len(t.qb.Units()),
		"health":          t.qb.Health(),
	}
	return marshalResult(out)
}

// ---------------------------------------------------------------------------
// quant.global_risk_status
// ---------------------------------------------------------------------------

type globalRiskStatusTool struct {
	qb       *quant.QuantBrain
	accounts map[string]*quant.Account
}

func newGlobalRiskStatusTool(qb *quant.QuantBrain, accounts map[string]*quant.Account) tool.Tool {
	return &globalRiskStatusTool{qb: qb, accounts: accounts}
}

func (t *globalRiskStatusTool) Name() string { return "quant.global_risk_status" }
func (t *globalRiskStatusTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *globalRiskStatusTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.global_risk_status",
		Description: "查询全局风控状态：跨账户总敞口、同方向敞口、日亏损比例，以及各项阈值。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *globalRiskStatusTool) Execute(ctx context.Context, _ json.RawMessage) (*tool.Result, error) {
	var totalEquity, longExposure, shortExposure, dailyPnL float64
	symbolExposure := make(map[string]float64)

	for _, acc := range t.accounts {
		bal, err := acc.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		totalEquity += bal.Equity

		positions, err := acc.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}
		for _, p := range positions {
			notional := p.Quantity * p.AvgPrice
			symbolExposure[p.Symbol] += notional
			if p.Side == "long" {
				longExposure += notional
			} else {
				shortExposure += notional
			}
		}
	}

	// Daily PnL from trade stores
	for _, u := range t.qb.Units() {
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		dailyPnL += stats.TotalPnL
	}

	totalExposure := longExposure + shortExposure
	exposurePct := 0.0
	longPct := 0.0
	shortPct := 0.0
	dailyPnLPct := 0.0
	if totalEquity > 0 {
		exposurePct = totalExposure / totalEquity * 100
		longPct = longExposure / totalEquity * 100
		shortPct = shortExposure / totalEquity * 100
		dailyPnLPct = dailyPnL / totalEquity * 100
	}

	out := map[string]any{
		"total_equity":     totalEquity,
		"total_exposure":   totalExposure,
		"exposure_pct":     exposurePct,
		"long_exposure":    longExposure,
		"long_pct":         longPct,
		"short_exposure":   shortExposure,
		"short_pct":        shortPct,
		"daily_pnl":        dailyPnL,
		"daily_pnl_pct":    dailyPnLPct,
		"symbol_exposure":  symbolExposure,
	}
	return marshalResult(out)
}

// ---------------------------------------------------------------------------
// quant.strategy_weights
// ---------------------------------------------------------------------------

// StrategyWeight represents a single strategy with its base and regime weights.
type StrategyWeight struct {
	Name         string  `json:"name"`
	BaseWeight   float64 `json:"base_weight"`
	RegimeWeight float64 `json:"regime_weight"`
}

type strategyWeightsTool struct {
	qb *quant.QuantBrain
}

func newStrategyWeightsTool(qb *quant.QuantBrain) tool.Tool {
	return &strategyWeightsTool{qb: qb}
}

func (t *strategyWeightsTool) Name() string { return "quant.strategy_weights" }
func (t *strategyWeightsTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *strategyWeightsTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.strategy_weights",
		Description: "查询当前各策略权重（含市场状态自适应调整后的权重）。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *strategyWeightsTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	type unitWeights struct {
		UnitID     string           `json:"unit_id"`
		Strategies []StrategyWeight `json:"strategies"`
	}

	// Get L1 base weights from weight adapter (if available).
	var globalBaseWeights map[string]float64
	if wa := t.qb.WeightAdapter(); wa != nil {
		globalBaseWeights = wa.Weights()
	}

	var units []unitWeights
	for _, u := range t.qb.Units() {
		// Collect all strategy names from the pool.
		poolStrategies := u.Pool.Strategies()

		// Get effective (regime-applied) weights from the aggregator.
		effectiveWeights := make(map[string]float64)
		if u.Aggregator != nil {
			baseAgg := u.Aggregator.BaseAggregator()
			for name, w := range baseAgg.Weights {
				effectiveWeights[name] = w
			}
		}

		// Build per-strategy weight records.
		strategies := make([]StrategyWeight, 0, len(poolStrategies))
		for _, s := range poolStrategies {
			name := s.Name()
			if name == "" {
				continue
			}

			// Base weight: from L1 adapter, then fallback to effective, then default.
			baseWeight := globalBaseWeights[name]
			if baseWeight == 0 {
				baseWeight = effectiveWeights[name]
			}
			if baseWeight == 0 {
				baseWeight = strategy.DefaultWeights()[name]
			}

			// Regime weight: currently effective weight (post-regime-override).
			regimeWeight := effectiveWeights[name]
			if regimeWeight == 0 {
				regimeWeight = baseWeight
			}

			strategies = append(strategies, StrategyWeight{
				Name:         name,
				BaseWeight:   baseWeight,
				RegimeWeight: regimeWeight,
			})
		}

		units = append(units, unitWeights{
			UnitID:     u.ID,
			Strategies: strategies,
		})
	}

	out := map[string]any{
		"units": units,
	}
	return marshalResult(out)
}

// ---------------------------------------------------------------------------
// quant.daily_pnl
// ---------------------------------------------------------------------------

type dailyPnLTool struct {
	qb *quant.QuantBrain
}

func newDailyPnLTool(qb *quant.QuantBrain) tool.Tool {
	return &dailyPnLTool{qb: qb}
}

func (t *dailyPnLTool) Name() string { return "quant.daily_pnl" }
func (t *dailyPnLTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *dailyPnLTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.daily_pnl",
		Description: "查询各交易单元的当日损益汇总（交易次数、胜率、盈亏）。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *dailyPnLTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	type unitPnL struct {
		UnitID      string  `json:"unit_id"`
		AccountID   string  `json:"account_id"`
		TotalTrades int     `json:"total_trades"`
		Wins        int     `json:"wins"`
		Losses      int     `json:"losses"`
		WinRate     float64 `json:"win_rate"`
		TotalPnL    float64 `json:"total_pnl"`
		AvgWin      float64 `json:"avg_win"`
		AvgLoss     float64 `json:"avg_loss"`
	}

	var totalPnL float64
	var allUnits []unitPnL
	for _, u := range t.qb.Units() {
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		totalPnL += stats.TotalPnL
		allUnits = append(allUnits, unitPnL{
			UnitID:      u.ID,
			AccountID:   u.Account.ID,
			TotalTrades: stats.TotalTrades,
			Wins:        stats.Wins,
			Losses:      stats.Losses,
			WinRate:     stats.WinRate,
			TotalPnL:    stats.TotalPnL,
			AvgWin:      stats.AvgWin,
			AvgLoss:     stats.AvgLoss,
		})
	}

	out := map[string]any{
		"date":      time.Now().UTC().Format("2006-01-02"),
		"total_pnl": totalPnL,
		"units":     allUnits,
	}
	return marshalResult(out)
}

// ---------------------------------------------------------------------------
// quant.account_status
// ---------------------------------------------------------------------------

type accountStatusTool struct {
	accounts map[string]*quant.Account
}

func newAccountStatusTool(accounts map[string]*quant.Account) tool.Tool {
	return &accountStatusTool{accounts: accounts}
}

func (t *accountStatusTool) Name() string { return "quant.account_status" }
func (t *accountStatusTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *accountStatusTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.account_status",
		Description: "查询指定账户的余额和持仓。account_id 为空时返回所有账户。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {
					"type": "string",
					"description": "账户ID，为空返回所有账户"
				}
			}
		}`),
	}
}

func (t *accountStatusTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &input)
	}

	type posOut struct {
		Symbol    string  `json:"symbol"`
		Side      string  `json:"side"`
		Quantity  float64 `json:"quantity"`
		AvgPrice  float64 `json:"avg_price"`
		MarkPrice float64 `json:"mark_price"`
		Notional  float64 `json:"notional"`
		Margin    float64 `json:"margin"`
		PnL       float64 `json:"unrealized_pnl"`
		Leverage  int     `json:"leverage"`
	}

	type acctOut struct {
		AccountID string   `json:"account_id"`
		Exchange  string   `json:"exchange"`
		Equity    float64  `json:"equity"`
		Available float64  `json:"available"`
		Margin    float64  `json:"margin"`
		PnL       float64  `json:"unrealized_pnl"`
		Currency  string   `json:"currency"`
		IsOpen    bool     `json:"is_open"`
		Positions []posOut `json:"positions"`
	}

	query := t.accounts
	if input.AccountID != "" {
		acc, ok := t.accounts[input.AccountID]
		if !ok {
			return errorResult(fmt.Sprintf("account not found: %s", input.AccountID))
		}
		query = map[string]*quant.Account{input.AccountID: acc}
	}

	var results []acctOut
	for id, acc := range query {
		bal, err := acc.Exchange.QueryBalance(ctx)
		if err != nil {
			results = append(results, acctOut{
				AccountID: id,
				Exchange:  acc.Exchange.Name(),
			})
			continue
		}

		positions, err := acc.Exchange.QueryPositions(ctx)
		if err != nil {
			positions = nil
		}

		posOuts := make([]posOut, 0, len(positions))
		for _, p := range positions {
			posOuts = append(posOuts, posOut{
				Symbol:    p.Symbol,
				Side:      p.Side,
				Quantity:  p.Quantity,
				AvgPrice:  p.AvgPrice,
				MarkPrice: p.MarkPrice,
				Notional:  p.Notional,
				Margin:    p.Margin,
				PnL:       p.UnrealizedPL,
				Leverage:  p.Leverage,
			})
		}

		results = append(results, acctOut{
			AccountID: id,
			Exchange:  acc.Exchange.Name(),
			Equity:    bal.Equity,
			Available: bal.Available,
			Margin:    bal.Margin,
			PnL:       bal.UnrealizedPL,
			Currency:  bal.Currency,
			IsOpen:    acc.Exchange.IsOpen(),
			Positions: posOuts,
		})
	}

	return marshalResult(results)
}

// ---------------------------------------------------------------------------
// quant.pause_trading
// ---------------------------------------------------------------------------

type pauseTradingTool struct {
	qb *quant.QuantBrain
}

func newPauseTradingTool(qb *quant.QuantBrain) tool.Tool {
	return &pauseTradingTool{qb: qb}
}

func (t *pauseTradingTool) Name() string { return "quant.pause_trading" }
func (t *pauseTradingTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *pauseTradingTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.pause_trading",
		Description: "暂停所有交易单元的开新仓（已有持仓的止损/止盈继续生效）。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *pauseTradingTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	paused := 0
	for _, u := range t.qb.Units() {
		if u.Enabled {
			u.Enabled = false
			paused++
		}
	}
	return marshalResult(map[string]any{
		"paused":      paused,
		"total_units": len(t.qb.Units()),
		"status":      "all_paused",
	})
}

// ---------------------------------------------------------------------------
// quant.resume_trading
// ---------------------------------------------------------------------------

type resumeTradingTool struct {
	qb *quant.QuantBrain
}

func newResumeTradingTool(qb *quant.QuantBrain) tool.Tool {
	return &resumeTradingTool{qb: qb}
}

func (t *resumeTradingTool) Name() string { return "quant.resume_trading" }
func (t *resumeTradingTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *resumeTradingTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.resume_trading",
		Description: "恢复所有交易单元的开新仓。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (t *resumeTradingTool) Execute(_ context.Context, _ json.RawMessage) (*tool.Result, error) {
	resumed := 0
	for _, u := range t.qb.Units() {
		if !u.Enabled {
			u.Enabled = true
			resumed++
		}
	}
	return marshalResult(map[string]any{
		"resumed":     resumed,
		"total_units": len(t.qb.Units()),
		"status":      "all_resumed",
	})
}

// ---------------------------------------------------------------------------
// quant.account_pause
// ---------------------------------------------------------------------------

type accountPauseTool struct {
	qb *quant.QuantBrain
}

func newAccountPauseTool(qb *quant.QuantBrain) tool.Tool {
	return &accountPauseTool{qb: qb}
}

func (t *accountPauseTool) Name() string { return "quant.account_pause" }
func (t *accountPauseTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *accountPauseTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.account_pause",
		Description: "暂停指定账户的所有交易单元（已有持仓的止损/止盈继续生效）。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {"type": "string", "description": "要暂停的账户ID"}
			},
			"required": ["account_id"]
		}`),
	}
}

func (t *accountPauseTool) Execute(_ context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	paused := 0
	found := false
	for _, u := range t.qb.Units() {
		if u.Account.ID == input.AccountID {
			found = true
			if u.Enabled {
				u.Enabled = false
				paused++
			}
		}
	}
	if !found {
		return errorResult(fmt.Sprintf("no units found for account: %s", input.AccountID))
	}
	return marshalResult(map[string]any{
		"account_id": input.AccountID,
		"paused":     paused,
		"status":     "paused",
	})
}

// ---------------------------------------------------------------------------
// quant.account_resume
// ---------------------------------------------------------------------------

type accountResumeTool struct {
	qb *quant.QuantBrain
}

func newAccountResumeTool(qb *quant.QuantBrain) tool.Tool {
	return &accountResumeTool{qb: qb}
}

func (t *accountResumeTool) Name() string { return "quant.account_resume" }
func (t *accountResumeTool) Risk() tool.Risk { return tool.RiskMedium }
func (t *accountResumeTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.account_resume",
		Description: "恢复指定账户的所有交易单元。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {"type": "string", "description": "要恢复的账户ID"}
			},
			"required": ["account_id"]
		}`),
	}
}

func (t *accountResumeTool) Execute(_ context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	resumed := 0
	found := false
	for _, u := range t.qb.Units() {
		if u.Account.ID == input.AccountID {
			found = true
			if !u.Enabled {
				u.Enabled = true
				resumed++
			}
		}
	}
	if !found {
		return errorResult(fmt.Sprintf("no units found for account: %s", input.AccountID))
	}
	return marshalResult(map[string]any{
		"account_id": input.AccountID,
		"resumed":    resumed,
		"status":     "resumed",
	})
}

// ---------------------------------------------------------------------------
// quant.account_close_all
// ---------------------------------------------------------------------------

type accountCloseAllTool struct {
	qb       *quant.QuantBrain
	accounts map[string]*quant.Account
}

func newAccountCloseAllTool(qb *quant.QuantBrain, accounts map[string]*quant.Account) tool.Tool {
	return &accountCloseAllTool{qb: qb, accounts: accounts}
}

func (t *accountCloseAllTool) Name() string { return "quant.account_close_all" }
func (t *accountCloseAllTool) Risk() tool.Risk { return tool.RiskCritical }
func (t *accountCloseAllTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.account_close_all",
		Description: "平仓指定账户的所有持仓（市价单），同时暂停该账户所有交易单元。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {"type": "string", "description": "要平仓的账户ID"}
			},
			"required": ["account_id"]
		}`),
	}
}

func (t *accountCloseAllTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	acc, ok := t.accounts[input.AccountID]
	if !ok {
		return errorResult(fmt.Sprintf("account not found: %s", input.AccountID))
	}

	// Pause all units for this account first
	for _, u := range t.qb.Units() {
		if u.Account.ID == input.AccountID {
			u.Enabled = false
		}
	}

	positions, err := acc.Exchange.QueryPositions(ctx)
	if err != nil {
		return errorResult("query positions failed: " + err.Error())
	}

	type closeResult struct {
		Symbol  string `json:"symbol"`
		Side    string `json:"side"`
		Qty     float64 `json:"quantity"`
		Status  string `json:"status"`
		OrderID string `json:"order_id,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	var results []closeResult
	for _, p := range positions {
		// Close by placing an opposite market order.
		// Must include MarkPrice so PaperExchange can fill the order
		// (paper backend has no external price feed — it needs a reference price).
		closeSide := "sell"
		closePosSide := "long"
		if p.Side == "short" {
			closeSide = "buy"
			closePosSide = "short"
		}

		// Use MarkPrice as reference; fall back to AvgPrice.
		refPrice := p.MarkPrice
		if refPrice <= 0 {
			refPrice = p.AvgPrice
		}

		result, err := acc.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
			Symbol:     p.Symbol,
			Side:       closeSide,
			PosSide:    closePosSide,
			Type:       "market",
			Price:      refPrice,
			Quantity:   p.Quantity,
			ReduceOnly: true,
			ClientID:   fmt.Sprintf("close-%s-%d", p.Symbol, time.Now().UnixMilli()),
		})
		if err != nil {
			results = append(results, closeResult{
				Symbol: p.Symbol,
				Side:   p.Side,
				Qty:    p.Quantity,
				Status: "failed",
				Error:  err.Error(),
			})
			continue
		}
		results = append(results, closeResult{
			Symbol:  p.Symbol,
			Side:    p.Side,
			Qty:     p.Quantity,
			Status:  result.Status,
			OrderID: result.OrderID,
		})
	}

	return marshalResult(map[string]any{
		"account_id":     input.AccountID,
		"positions_closed": len(positions),
		"results":        results,
	})
}

// ---------------------------------------------------------------------------
// quant.force_close
// ---------------------------------------------------------------------------

type forceCloseTool struct {
	accounts map[string]*quant.Account
}

func newForceCloseTool(accounts map[string]*quant.Account) tool.Tool {
	return &forceCloseTool{accounts: accounts}
}

func (t *forceCloseTool) Name() string { return "quant.force_close" }
func (t *forceCloseTool) Risk() tool.Risk { return tool.RiskCritical }
func (t *forceCloseTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.force_close",
		Description: "强制平仓指定账户的指定品种持仓（市价单）。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {"type": "string", "description": "账户ID"},
				"symbol":     {"type": "string", "description": "品种标识，如 BTC-USDT-SWAP"}
			},
			"required": ["account_id", "symbol"]
		}`),
	}
}

func (t *forceCloseTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
		Symbol    string `json:"symbol"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	acc, ok := t.accounts[input.AccountID]
	if !ok {
		return errorResult(fmt.Sprintf("account not found: %s", input.AccountID))
	}

	positions, err := acc.Exchange.QueryPositions(ctx)
	if err != nil {
		return errorResult("query positions failed: " + err.Error())
	}

	// Find position for the symbol
	var target *exchange.PositionInfo
	for i, p := range positions {
		if p.Symbol == input.Symbol {
			target = &positions[i]
			break
		}
	}
	if target == nil {
		return errorResult(fmt.Sprintf("no position found for %s on account %s", input.Symbol, input.AccountID))
	}

	closeSide := "sell"
	closePosSide := "long"
	if target.Side == "short" {
		closeSide = "buy"
		closePosSide = "short"
	}

	// Use MarkPrice as reference so PaperExchange can fill the order.
	refPrice := target.MarkPrice
	if refPrice <= 0 {
		refPrice = target.AvgPrice
	}

	result, err := acc.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
		Symbol:     input.Symbol,
		Side:       closeSide,
		PosSide:    closePosSide,
		Type:       "market",
		Price:      refPrice,
		Quantity:   target.Quantity,
		ReduceOnly: true,
		ClientID:   fmt.Sprintf("force-%s-%d", input.Symbol, time.Now().UnixMilli()),
	})
	if err != nil {
		return errorResult("place close order failed: " + err.Error())
	}

	return marshalResult(map[string]any{
		"account_id": input.AccountID,
		"symbol":     input.Symbol,
		"side":       target.Side,
		"quantity":   target.Quantity,
		"status":     result.Status,
		"order_id":   result.OrderID,
		"fill_price": result.FillPrice,
	})
}

// ---------------------------------------------------------------------------
// quant.trace_query
// ---------------------------------------------------------------------------

type traceQueryTool struct {
	qb *quant.QuantBrain
}

func newTraceQueryTool(qb *quant.QuantBrain) tool.Tool {
	return &traceQueryTool{qb: qb}
}

func (t *traceQueryTool) Name() string { return "quant.trace_query" }
func (t *traceQueryTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *traceQueryTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.trace_query",
		Description: "查询信号审计追踪记录。可按品种、结果、时间范围过滤。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"symbol":  {"type": "string", "description": "品种过滤，为空返回全部"},
				"outcome": {"type": "string", "description": "结果过滤: executed/rejected_risk/rejected_global/needs_review"},
				"since":   {"type": "string", "description": "起始时间 (RFC3339)，为空返回全部"},
				"limit":   {"type": "integer", "description": "最大返回条数，默认50"}
			}
		}`),
	}
}

func (t *traceQueryTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		Symbol  string `json:"symbol"`
		Outcome string `json:"outcome"`
		Since   string `json:"since"`
		Limit   int    `json:"limit"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &input)
	}
	if input.Limit <= 0 {
		input.Limit = 50
	}

	store := t.qb.TraceStore()
	if store == nil {
		return errorResult("trace store not configured")
	}

	filter := tracer.TraceFilter{
		Symbol:  input.Symbol,
		Outcome: input.Outcome,
		Limit:   input.Limit,
	}
	if input.Since != "" {
		if ts, err := time.Parse(time.RFC3339, input.Since); err == nil {
			filter.Since = ts
		}
	}

	traces, err := store.Query(ctx, filter)
	if err != nil {
		return errorResult("query traces failed: " + err.Error())
	}

	return marshalResult(map[string]any{
		"count":  len(traces),
		"traces": traces,
	})
}

// ---------------------------------------------------------------------------
// quant.trade_history
// ---------------------------------------------------------------------------

type tradeHistoryTool struct {
	qb *quant.QuantBrain
}

func newTradeHistoryTool(qb *quant.QuantBrain) tool.Tool {
	return &tradeHistoryTool{qb: qb}
}

func (t *tradeHistoryTool) Name() string { return "quant.trade_history" }
func (t *tradeHistoryTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *tradeHistoryTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.trade_history",
		Description: "查询历史交易记录。可按账户、单元、品种、方向过滤。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"account_id": {"type": "string", "description": "账户ID过滤，为空返回全部"},
				"unit_id":    {"type": "string", "description": "交易单元ID，为空返回全部"},
				"symbol":     {"type": "string", "description": "品种过滤"},
				"direction":  {"type": "string", "description": "方向过滤: long/short"},
				"since":      {"type": "string", "description": "起始时间 (RFC3339)"},
				"limit":      {"type": "integer", "description": "最大返回条数，默认100"}
			}
		}`),
	}
}

func (t *tradeHistoryTool) Execute(_ context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		AccountID string `json:"account_id"`
		UnitID    string `json:"unit_id"`
		Symbol    string `json:"symbol"`
		Direction string `json:"direction"`
		Since     string `json:"since"`
		Limit     int    `json:"limit"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &input)
	}
	if input.Limit <= 0 {
		input.Limit = 100
	}

	// Collect trades from all units (or specific unit/account)
	var allRecords []tradestore.TradeRecord
	for _, u := range t.qb.Units() {
		if input.UnitID != "" && u.ID != input.UnitID {
			continue
		}
		if input.AccountID != "" && u.Account.ID != input.AccountID {
			continue
		}
		filter := tradestore.Filter{
			AccountID: input.AccountID,
			UnitID:    u.ID,
			Symbol:    input.Symbol,
			Limit:     input.Limit,
		}
		if input.Direction != "" {
			filter.Direction = dirFromString(input.Direction)
		}
		if input.Since != "" {
			if ts, err := time.Parse(time.RFC3339, input.Since); err == nil {
				filter.Since = ts
			}
		}
		records := u.TradeStore.Query(filter)
		allRecords = append(allRecords, records...)
	}

	// Trim to limit
	if len(allRecords) > input.Limit {
		allRecords = allRecords[:input.Limit]
	}

	// Also collect aggregate stats
	var totalStats tradestore.Stats
	for _, u := range t.qb.Units() {
		if input.UnitID != "" && u.ID != input.UnitID {
			continue
		}
		s := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		totalStats.TotalTrades += s.TotalTrades
		totalStats.Wins += s.Wins
		totalStats.Losses += s.Losses
		totalStats.TotalPnL += s.TotalPnL
	}
	if totalStats.TotalTrades > 0 {
		totalStats.WinRate = float64(totalStats.Wins) / float64(totalStats.TotalTrades)
	}

	return marshalResult(map[string]any{
		"count":  len(allRecords),
		"trades": allRecords,
		"stats":  totalStats,
	})
}

// ---------------------------------------------------------------------------
// quant.backtest_start
// ---------------------------------------------------------------------------

type backtestStartTool struct {
	qb *quant.QuantBrain
}

func newBacktestStartTool(qb *quant.QuantBrain) tool.Tool {
	return &backtestStartTool{qb: qb}
}

func (t *backtestStartTool) Name() string { return "quant.backtest_start" }
func (t *backtestStartTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *backtestStartTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "quant.backtest_start",
		Description: "对历史K线数据运行回测，返回绩效报告（收益率、胜率、夏普比、最大回撤等）。需提供K线数据。",
		Brain:       "quant",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"symbol":         {"type": "string", "description": "品种标识"},
				"timeframe":      {"type": "string", "description": "时间框架，默认 1H"},
				"initial_equity": {"type": "number", "description": "初始资金，默认 10000"},
				"max_leverage":   {"type": "integer", "description": "最大杠杆，默认 1"},
				"slippage_bps":   {"type": "number", "description": "滑点(基点)，默认 5"},
				"fee_bps":        {"type": "number", "description": "手续费(基点)，默认 4"},
				"candles": {
					"type": "array",
					"description": "K线数据数组",
					"items": {
						"type": "object",
						"properties": {
							"timestamp": {"type": "integer"},
							"open":      {"type": "number"},
							"high":      {"type": "number"},
							"low":       {"type": "number"},
							"close":     {"type": "number"},
							"volume":    {"type": "number"}
						}
					}
				}
			},
			"required": ["symbol", "candles"]
		}`),
	}
}

func (t *backtestStartTool) Execute(_ context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		Symbol        string           `json:"symbol"`
		Timeframe     string           `json:"timeframe"`
		InitialEquity float64          `json:"initial_equity"`
		MaxLeverage   int              `json:"max_leverage"`
		SlippageBps   float64          `json:"slippage_bps"`
		FeeBps        float64          `json:"fee_bps"`
		Candles       []backtest.Candle `json:"candles"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}

	if len(input.Candles) == 0 {
		return errorResult("candles array is required and must not be empty")
	}

	cfg := backtest.Config{
		Symbol:      input.Symbol,
		Timeframe:   input.Timeframe,
		MaxLeverage: input.MaxLeverage,
		SlippageBps: input.SlippageBps,
		FeeBps:      input.FeeBps,
	}
	if cfg.SlippageBps == 0 {
		cfg.SlippageBps = 5
	}
	if cfg.FeeBps == 0 {
		cfg.FeeBps = 4
	}

	engine := backtest.NewEngine(cfg)
	report, err := engine.Run(nil, input.Candles, input.InitialEquity, time.Time{}, time.Time{})
	if err != nil {
		return errorResult("backtest failed: " + err.Error())
	}

	// Return summary (exclude raw trades for brevity unless few)
	tradeCount := len(report.Trades)
	var tradeSummary any
	if tradeCount <= 20 {
		tradeSummary = report.Trades
	} else {
		// Return top/bottom 5 trades
		sorted := report.SortTradesByPnL()
		top5 := sorted[:5]
		bottom5 := sorted[len(sorted)-5:]
		tradeSummary = map[string]any{
			"top_5":    top5,
			"bottom_5": bottom5,
		}
	}

	return marshalResult(map[string]any{
		"symbol":         report.Symbol,
		"timeframe":      report.Timeframe,
		"bars":           report.Bars,
		"total_trades":   tradeCount,
		"total_return":   report.TotalReturn,
		"win_rate":       report.WinRate,
		"avg_win":        report.AvgWin,
		"avg_loss":       report.AvgLoss,
		"profit_factor":  report.ProfitFactor,
		"max_drawdown":   report.MaxDrawdown,
		"sharpe_ratio":   report.SharpeRatio,
		"initial_equity": report.InitialEquity,
		"final_equity":   report.FinalEquity,
		"duration_ms":    report.Duration.Milliseconds(),
		"trades":         tradeSummary,
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func dirFromString(s string) strategy.Direction {
	return strategy.Direction(s)
}

func marshalResult(v any) (*tool.Result, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return &tool.Result{Output: data}, nil
}

func errorResult(msg string) (*tool.Result, error) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	return &tool.Result{Output: data, IsError: true}, nil
}
