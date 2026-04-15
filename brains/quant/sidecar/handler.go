package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/brains/quant/tracer"
	"github.com/leef-l/brain/brains/quant/tradestore"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"

	brain "github.com/leef-l/brain"
)

// quantHandler implements sidecar.BrainHandler, sidecar.RichBrainHandler,
// and sidecar.ToolSchemaProvider for the quant brain sidecar.
type quantHandler struct {
	qb       *quant.QuantBrain
	accounts map[string]*quant.Account
	registry tool.Registry
	caller   sidecar.KernelCaller
	logger   *slog.Logger
}

// NewHandler creates a quant sidecar handler.
// qb must already have TradingUnits registered and be ready to Start.
func NewHandler(qb *quant.QuantBrain, accounts map[string]*quant.Account, logger *slog.Logger) *quantHandler {
	if logger == nil {
		logger = slog.Default()
	}

	reg := tool.NewMemRegistry()
	// Phase 2: account query tools
	reg.Register(newGlobalPortfolioTool(qb, accounts))
	reg.Register(newGlobalRiskStatusTool(qb, accounts))
	reg.Register(newStrategyWeightsTool(qb))
	reg.Register(newDailyPnLTool(qb))
	reg.Register(newAccountStatusTool(accounts))
	reg.Register(newPauseTradingTool(qb))
	reg.Register(newResumeTradingTool(qb))
	// Phase 3: per-account operations + audit + history
	reg.Register(newAccountPauseTool(qb))
	reg.Register(newAccountResumeTool(qb))
	reg.Register(newAccountCloseAllTool(qb, accounts))
	reg.Register(newForceCloseTool(accounts))
	reg.Register(newTraceQueryTool(qb))
	reg.Register(newTradeHistoryTool(qb))
	// Phase 4: backtest
	reg.Register(newBacktestStartTool(qb))

	return &quantHandler{
		qb:       qb,
		accounts: accounts,
		registry: reg,
		logger:   logger,
	}
}

// ---------------------------------------------------------------------------
// sidecar.BrainHandler
// ---------------------------------------------------------------------------

func (h *quantHandler) Kind() agent.Kind { return agent.KindQuant }
func (h *quantHandler) Version() string  { return brain.SDKVersion }
func (h *quantHandler) Tools() []string  { return sidecar.RegistryToolNames(h.registry) }

// ---------------------------------------------------------------------------
// sidecar.ToolSchemaProvider
// ---------------------------------------------------------------------------

func (h *quantHandler) ToolSchemas() []tool.Schema {
	return sidecar.RegistryToolSchemas(h.registry)
}

// ---------------------------------------------------------------------------
// sidecar.RichBrainHandler
// ---------------------------------------------------------------------------

// SetKernelCaller injects the reverse-RPC caller and wires up the
// KernelReviewer so QuantBrain can request LLM trade review from the
// central brain via specialist.call_tool → central.review_trade.
func (h *quantHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller

	reviewer := quant.NewKernelReviewer(
		func(ctx context.Context, instruction string, payload []byte, timeoutSec int) ([]byte, error) {
			var result json.RawMessage
			err := caller.CallKernel(ctx, "specialist.call_tool", map[string]any{
				"tool":      "central.review_trade",
				"arguments": json.RawMessage(payload),
			}, &result)
			if err != nil {
				return nil, err
			}
			return result, nil
		},
		quant.DefaultReviewConfig(),
		h.logger,
	)
	h.qb.SetReviewer(reviewer)
	h.logger.Info("kernel caller injected, LLM reviewer wired")
}

// ---------------------------------------------------------------------------
// HandleMethod
// ---------------------------------------------------------------------------

func (h *quantHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		return sidecar.DispatchToolCall(ctx, params, h.registry, nil)
	case "brain/execute":
		return h.handleExecute(ctx, params)
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleExecute dispatches brain/execute requests by instruction field,
// matching the routing defined in Doc 35 §5.4.
func (h *quantHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	switch req.Instruction {
	case "global_portfolio":
		return h.execGlobalPortfolio(ctx)
	case "daily_pnl":
		return h.execDailyPnL(ctx)
	case "strategy_weights":
		return h.execStrategyWeights()
	case "pause_trading":
		return h.execPauseTrading()
	case "resume_trading":
		return h.execResumeTrading()
	case "trace_query":
		return h.execTraceQuery(ctx, req.Context)
	case "force_close":
		return h.execForceClose(ctx, req.Context)
	default:
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unknown instruction: %s", req.Instruction),
		}, nil
	}
}

// ---------------------------------------------------------------------------
// brain/execute instruction handlers
// ---------------------------------------------------------------------------

func (h *quantHandler) execGlobalPortfolio(ctx context.Context) (*sidecar.ExecuteResult, error) {
	type accountInfo struct {
		ID        string  `json:"id"`
		Exchange  string  `json:"exchange"`
		Equity    float64 `json:"equity"`
		Available float64 `json:"available"`
		Positions int     `json:"positions"`
	}

	var totalEquity float64
	var accts []accountInfo
	for id, acc := range h.accounts {
		bal, err := acc.Exchange.QueryBalance(ctx)
		if err != nil {
			continue
		}
		positions, err := acc.Exchange.QueryPositions(ctx)
		if err != nil {
			continue
		}
		totalEquity += bal.Equity
		accts = append(accts, accountInfo{
			ID:        id,
			Exchange:  acc.Exchange.Name(),
			Equity:    bal.Equity,
			Available: bal.Available,
			Positions: len(positions),
		})
	}

	data, _ := json.Marshal(map[string]any{
		"total_equity": totalEquity,
		"accounts":     accts,
		"health":       h.qb.Health(),
	})

	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(data),
	}, nil
}

func (h *quantHandler) execDailyPnL(ctx context.Context) (*sidecar.ExecuteResult, error) {
	type unitPnL struct {
		UnitID  string  `json:"unit_id"`
		PnL     float64 `json:"pnl"`
		Trades  int     `json:"trades"`
		WinRate float64 `json:"win_rate"`
	}

	var total float64
	var units []unitPnL
	for _, u := range h.qb.Units() {
		stats := u.TradeStore.Stats(tradestore.Filter{UnitID: u.ID})
		total += stats.TotalPnL
		units = append(units, unitPnL{
			UnitID:  u.ID,
			PnL:     stats.TotalPnL,
			Trades:  stats.TotalTrades,
			WinRate: stats.WinRate,
		})
	}

	data, _ := json.Marshal(map[string]any{
		"date":      time.Now().UTC().Format("2006-01-02"),
		"total_pnl": total,
		"units":     units,
	})

	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(data),
	}, nil
}

func (h *quantHandler) execStrategyWeights() (*sidecar.ExecuteResult, error) {
	type unitInfo struct {
		UnitID     string   `json:"unit_id"`
		Strategies []string `json:"strategies"`
	}

	var units []unitInfo
	for _, u := range h.qb.Units() {
		var names []string
		for _, s := range u.Pool.Strategies() {
			names = append(names, s.Name())
		}
		units = append(units, unitInfo{
			UnitID:     u.ID,
			Strategies: names,
		})
	}

	data, _ := json.Marshal(map[string]any{"units": units})
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(data),
	}, nil
}

func (h *quantHandler) execPauseTrading() (*sidecar.ExecuteResult, error) {
	paused := 0
	for _, u := range h.qb.Units() {
		if u.Enabled {
			u.Enabled = false
			paused++
		}
	}
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: fmt.Sprintf("paused %d units", paused),
	}, nil
}

func (h *quantHandler) execResumeTrading() (*sidecar.ExecuteResult, error) {
	resumed := 0
	for _, u := range h.qb.Units() {
		if !u.Enabled {
			u.Enabled = true
			resumed++
		}
	}
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: fmt.Sprintf("resumed %d units", resumed),
	}, nil
}

func (h *quantHandler) execTraceQuery(ctx context.Context, rawCtx json.RawMessage) (*sidecar.ExecuteResult, error) {
	store := h.qb.TraceStore()
	if store == nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "trace store not configured",
		}, nil
	}

	var filter tracer.TraceFilter
	filter.Limit = 50
	if len(rawCtx) > 0 {
		_ = json.Unmarshal(rawCtx, &filter)
	}

	traces, err := store.Query(ctx, filter)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  err.Error(),
		}, nil
	}

	data, _ := json.Marshal(map[string]any{
		"count":  len(traces),
		"traces": traces,
	})
	return &sidecar.ExecuteResult{
		Status:  "completed",
		Summary: string(data),
	}, nil
}

func (h *quantHandler) execForceClose(ctx context.Context, rawCtx json.RawMessage) (*sidecar.ExecuteResult, error) {
	var input struct {
		AccountID string `json:"account_id"`
		Symbol    string `json:"symbol"`
	}
	if len(rawCtx) > 0 {
		_ = json.Unmarshal(rawCtx, &input)
	}
	if input.AccountID == "" || input.Symbol == "" {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "account_id and symbol are required in context",
		}, nil
	}

	acc, ok := h.accounts[input.AccountID]
	if !ok {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("account not found: %s", input.AccountID),
		}, nil
	}

	positions, err := acc.Exchange.QueryPositions(ctx)
	if err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  "query positions: " + err.Error(),
		}, nil
	}

	for _, p := range positions {
		if p.Symbol != input.Symbol {
			continue
		}
		closeSide := "sell"
		closePosSide := "long"
		if p.Side == "short" {
			closeSide = "buy"
			closePosSide = "short"
		}
		result, err := acc.Exchange.PlaceOrder(ctx, exchange.PlaceOrderParams{
			Symbol:   input.Symbol,
			Side:     closeSide,
			PosSide:  closePosSide,
			Type:     "market",
			Quantity: p.Quantity,
			ClientID: fmt.Sprintf("force-%s-%d", input.Symbol, time.Now().UnixMilli()),
		})
		if err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  err.Error(),
			}, nil
		}
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: fmt.Sprintf("closed %s %s qty=%.4f order=%s", input.Symbol, p.Side, p.Quantity, result.OrderID),
		}, nil
	}

	return &sidecar.ExecuteResult{
		Status: "failed",
		Error:  fmt.Sprintf("no position for %s on %s", input.Symbol, input.AccountID),
	}, nil
}
