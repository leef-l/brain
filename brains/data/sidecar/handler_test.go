package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	data "github.com/leef-l/brain/brains/data"
	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/sdk/agent"
)

const totalTools = 9

// buildTestDataBrain creates a DataBrain with no store (test mode),
// and writes a sample snapshot into its ring buffer.
func buildTestDataBrain(t *testing.T) *data.DataBrain {
	t.Helper()

	cfg := data.Config{
		ActiveList: data.ActiveListConfig{
			MinVolume24h:   10_000_000,
			MaxInstruments: 10,
			UpdateInterval: 24 * time.Hour,
			AlwaysInclude:  []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"},
		},
		Validation: data.ValidationConfig{
			MaxPriceJump: 0.10,
		},
	}

	db := data.New(cfg, nil, slog.Default())

	// Write a test snapshot into the ring buffer
	db.Buffers().Write("BTC-USDT-SWAP", ringbuf.MarketSnapshot{
		SeqNum:             1,
		InstID:             "BTC-USDT-SWAP",
		Timestamp:          time.Now().UnixMilli(),
		CurrentPrice:       65000.0,
		BidPrice:           64999.5,
		AskPrice:           65000.5,
		FundingRate:        0.0001,
		OpenInterest:       1000000,
		Volume24h:          50000000,
		OrderBookImbalance: 0.15,
		Spread:             0.00002,
		TradeFlowToxicity:  0.3,
		BigBuyRatio:        0.1,
		BigSellRatio:       0.05,
		TradeDensityRatio:  2.5,
		BuySellRatio:       0.55,
		MLSource:           "fallback",
		MLReady:            false,
		MarketRegime:       "trend",
		AnomalyLevel:       0.1,
		VolPercentile:      0.6,
	})

	return db
}

func TestHandlerKindAndVersion(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	if h.Kind() != agent.KindData {
		t.Errorf("Kind = %v, want %v", h.Kind(), agent.KindData)
	}
	if h.Version() == "" {
		t.Error("Version is empty")
	}
}

func TestToolsList(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	tools := h.Tools()
	if len(tools) != totalTools {
		t.Errorf("Tools count = %d, want %d", len(tools), totalTools)
	}

	expected := map[string]bool{
		"data.get_candles":        false,
		"data.get_snapshot":       false,
		"data.get_feature_vector": false,
		"data.provider_health":    false,
		"data.validation_stats":   false,
		"data.backfill_status":    false,
		"data.active_instruments": false,
		"data.replay_start":       false,
		"data.replay_stop":        false,
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
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	schemas := h.ToolSchemas()
	if len(schemas) != totalTools {
		t.Errorf("ToolSchemas count = %d, want %d", len(schemas), totalTools)
	}
	for _, s := range schemas {
		if s.Brain != "data" {
			t.Errorf("tool %s: brain = %q, want %q", s.Name, s.Brain, "data")
		}
		if len(s.InputSchema) == 0 {
			t.Errorf("tool %s: InputSchema is empty", s.Name)
		}
	}
}

func TestGetSnapshotTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	// Existing instrument
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.get_snapshot",
		"arguments": {"instrument_id": "BTC-USDT-SWAP"}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	if len(d) == 0 {
		t.Fatal("empty result")
	}
	t.Logf("get_snapshot: %s", d)

	// Non-existent instrument
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.get_snapshot",
		"arguments": {"instrument_id": "NONEXISTENT"}
	}`))
	if err != nil {
		t.Fatalf("tools/call nonexistent: %v", err)
	}
	d, _ = json.Marshal(result)
	t.Logf("get_snapshot (nonexistent): %s", d)
}

func TestGetCandlesTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	// No candles exist in test mode
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.get_candles",
		"arguments": {"instrument_id": "BTC-USDT-SWAP", "timeframe": "1m"}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("get_candles (empty): %s", d)
}

func TestGetFeatureVectorTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.get_feature_vector",
		"arguments": {"instrument_id": "BTC-USDT-SWAP"}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	if len(d) == 0 {
		t.Fatal("empty result")
	}
	t.Logf("get_feature_vector: %s", d)
}

func TestProviderHealthTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.provider_health",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("provider_health: %s", d)
}

func TestValidationStatsTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.validation_stats",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("validation_stats: %s", d)
}

func TestActiveInstrumentsTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.active_instruments",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("active_instruments: %s", d)
}

func TestBackfillStatusTool(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	// No store configured → should return error
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.backfill_status",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("backfill_status (no store): %s", d)
}

func TestReplayTools(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	// replay_start
	result, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.replay_start",
		"arguments": {"instrument_ids": ["BTC-USDT-SWAP"], "from_ts": 0}
	}`))
	if err != nil {
		t.Fatalf("replay_start: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("replay_start: %s", d)

	// replay_stop
	result, err = h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name": "data.replay_stop",
		"arguments": {}
	}`))
	if err != nil {
		t.Fatalf("replay_stop: %v", err)
	}
	d, _ = json.Marshal(result)
	t.Logf("replay_stop: %s", d)
}

func TestBrainExecute(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	for _, instruction := range []string{
		"health",
		"active_instruments",
	} {
		t.Run(instruction, func(t *testing.T) {
			params, _ := json.Marshal(map[string]any{
				"instruction": instruction,
			})
			result, err := h.HandleMethod(context.Background(), "brain/execute", params)
			if err != nil {
				t.Fatalf("brain/execute %s: %v", instruction, err)
			}
			d, _ := json.Marshal(result)
			t.Logf("%s: %s", instruction, d)
		})
	}
}

func TestBrainExecuteSnapshot(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	params, _ := json.Marshal(map[string]any{
		"instruction": "snapshot",
		"context":     map[string]any{"instrument_id": "BTC-USDT-SWAP"},
	})
	result, err := h.HandleMethod(context.Background(), "brain/execute", params)
	if err != nil {
		t.Fatalf("brain/execute snapshot: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("snapshot: %s", d)
}

func TestBrainExecuteFeatureVector(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	params, _ := json.Marshal(map[string]any{
		"instruction": "feature_vector",
		"context":     map[string]any{"instrument_id": "BTC-USDT-SWAP"},
	})
	result, err := h.HandleMethod(context.Background(), "brain/execute", params)
	if err != nil {
		t.Fatalf("brain/execute feature_vector: %v", err)
	}
	d, _ := json.Marshal(result)
	t.Logf("feature_vector: %s", d)
}

func TestUnknownMethod(t *testing.T) {
	db := buildTestDataBrain(t)
	h := NewHandler(db, nil)

	_, err := h.HandleMethod(context.Background(), "unknown/method", nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}
