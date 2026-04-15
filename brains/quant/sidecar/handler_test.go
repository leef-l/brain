package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/quant"
	"github.com/leef-l/brain/brains/quant/exchange"
	"github.com/leef-l/brain/sdk/agent"
)

const totalTools = 14

// buildTestBrain creates a QuantBrain with one paper account and one unit for testing.
func buildTestBrain(t *testing.T) (*quant.QuantBrain, map[string]*quant.Account) {
	t.Helper()

	paper := exchange.NewPaperExchange(exchange.PaperConfig{InitialEquity: 10000})
	accounts := map[string]*quant.Account{
		"paper-test": {
			ID:       "paper-test",
			Exchange: paper,
			Tags:     []string{"test"},
		},
	}

	buffers := ringbuf.NewBufferManager(64)
	qb := quant.New(quant.Config{}, buffers, slog.Default())
	unit := quant.NewTradingUnit(quant.TradingUnitConfig{
		ID:          "unit-1",
		Account:     accounts["paper-test"],
		Symbols:     []string{"BTC-USDT-SWAP"},
		MaxLeverage: 10,
	}, slog.Default())
	qb.AddUnit(unit)

	return qb, accounts
}

func TestHandlerKindAndVersion(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	if h.Kind() != agent.KindQuant {
		t.Errorf("Kind = %v, want %v", h.Kind(), agent.KindQuant)
	}
	if h.Version() == "" {
		t.Error("Version is empty")
	}
}

func TestToolsList(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	tools := h.Tools()
	if len(tools) != totalTools {
		t.Errorf("Tools count = %d, want %d", len(tools), totalTools)
	}

	expected := map[string]bool{
		"quant.global_portfolio":   false,
		"quant.global_risk_status": false,
		"quant.strategy_weights":   false,
		"quant.daily_pnl":          false,
		"quant.account_status":     false,
		"quant.pause_trading":      false,
		"quant.resume_trading":     false,
		"quant.account_pause":      false,
		"quant.account_resume":     false,
		"quant.account_close_all":  false,
		"quant.force_close":        false,
		"quant.trace_query":        false,
		"quant.trade_history":      false,
		"quant.backtest_start":     false,
	}
	for _, name := range tools {
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestToolSchemas(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	schemas := h.ToolSchemas()
	if len(schemas) != totalTools {
		t.Errorf("ToolSchemas count = %d, want %d", len(schemas), totalTools)
	}
	for _, s := range schemas {
		if s.Brain != "quant" {
			t.Errorf("tool %s: brain = %q, want %q", s.Name, s.Brain, "quant")
		}
		if len(s.InputSchema) == 0 {
			t.Errorf("tool %s: InputSchema is empty", s.Name)
		}
	}
}

func TestGlobalPortfolioTool(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.global_portfolio",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	data, _ := json.Marshal(result)
	if len(data) == 0 {
		t.Fatal("empty result")
	}
	t.Logf("global_portfolio result: %s", data)
}

func TestAccountStatusTool(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// Query specific account
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_status",
		"arguments": {"account_id": "paper-test"}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	data, _ := json.Marshal(result)
	if len(data) == 0 {
		t.Fatal("empty result")
	}
	t.Logf("account_status result: %s", data)

	// Query non-existent account
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_status",
		"arguments": {"account_id": "nonexistent"}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	data, _ = json.Marshal(result)
	t.Logf("account_status (nonexistent): %s", data)
}

func TestPauseResumeTool(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// Pause
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.pause_trading",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("pause result: %s", data)

	for _, u := range qb.Units() {
		if u.Enabled {
			t.Error("unit should be disabled after pause")
		}
	}

	// Resume
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.resume_trading",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	data, _ = json.Marshal(result)
	t.Logf("resume result: %s", data)

	for _, u := range qb.Units() {
		if !u.Enabled {
			t.Error("unit should be enabled after resume")
		}
	}
}

func TestAccountPauseResume(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// Pause specific account
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_pause",
		"arguments": {"account_id": "paper-test"}
	}`))
	if err != nil {
		t.Fatalf("account_pause: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("account_pause: %s", data)

	for _, u := range qb.Units() {
		if u.Account.ID == "paper-test" && u.Enabled {
			t.Error("unit should be disabled after account_pause")
		}
	}

	// Resume specific account
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_resume",
		"arguments": {"account_id": "paper-test"}
	}`))
	if err != nil {
		t.Fatalf("account_resume: %v", err)
	}
	data, _ = json.Marshal(result)
	t.Logf("account_resume: %s", data)

	for _, u := range qb.Units() {
		if u.Account.ID == "paper-test" && !u.Enabled {
			t.Error("unit should be enabled after account_resume")
		}
	}

	// Non-existent account
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_pause",
		"arguments": {"account_id": "nonexistent"}
	}`))
	if err != nil {
		t.Fatalf("account_pause nonexistent: %v", err)
	}
	data, _ = json.Marshal(result)
	t.Logf("account_pause (nonexistent): %s", data)
}

func TestAccountCloseAll(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// No positions to close, should succeed with 0 closed
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_close_all",
		"arguments": {"account_id": "paper-test"}
	}`))
	if err != nil {
		t.Fatalf("account_close_all: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("account_close_all: %s", data)

	// Non-existent account
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.account_close_all",
		"arguments": {"account_id": "nonexistent"}
	}`))
	if err != nil {
		t.Fatalf("account_close_all nonexistent: %v", err)
	}
	data, _ = json.Marshal(result)
	t.Logf("account_close_all (nonexistent): %s", data)
}

func TestForceClose(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// No position exists
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.force_close",
		"arguments": {"account_id": "paper-test", "symbol": "BTC-USDT-SWAP"}
	}`))
	if err != nil {
		t.Fatalf("force_close: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("force_close (no position): %s", data)
}

func TestTraceQuery(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.trace_query",
		"arguments": {"limit": 10}
	}`))
	if err != nil {
		t.Fatalf("trace_query: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("trace_query: %s", data)
}

func TestTradeHistory(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.trade_history",
		"arguments": {"unit_id": "unit-1", "limit": 10}
	}`))
	if err != nil {
		t.Fatalf("trade_history: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("trade_history: %s", data)
}

func TestBacktestStart(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	// Too few candles should return error
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "quant.backtest_start",
		"arguments": {"symbol": "BTC-USDT-SWAP", "candles": []}
	}`))
	if err != nil {
		t.Fatalf("backtest_start empty: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("backtest_start (empty candles): %s", data)
}

func TestBrainExecute(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	for _, instruction := range []string{
		"global_portfolio",
		"daily_pnl",
		"strategy_weights",
		"pause_trading",
		"resume_trading",
		"trace_query",
	} {
		t.Run(instruction, func(t *testing.T) {
			params, _ := json.Marshal(map[string]any{
				"instruction": instruction,
			})
			result, err := h.HandleMethod(context.Background(), "brain/execute", params)
			if err != nil {
				t.Fatalf("brain/execute %s: %v", instruction, err)
			}
			data, _ := json.Marshal(result)
			t.Logf("%s: %s", instruction, data)
		})
	}
}

func TestBrainExecuteForceClose(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	params, _ := json.Marshal(map[string]any{
		"instruction": "force_close",
		"context":     map[string]any{"account_id": "paper-test", "symbol": "BTC-USDT-SWAP"},
	})
	result, err := h.HandleMethod(context.Background(), "brain/execute", params)
	if err != nil {
		t.Fatalf("brain/execute force_close: %v", err)
	}
	data, _ := json.Marshal(result)
	t.Logf("force_close: %s", data)
}

func TestUnknownMethod(t *testing.T) {
	qb, accounts := buildTestBrain(t)
	h := NewHandler(qb, accounts, nil)

	_, err := h.HandleMethod(context.Background(), "unknown/method", nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}
