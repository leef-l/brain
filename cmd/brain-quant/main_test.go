package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
)

func TestRunChecksLicenseBeforeStartingSidecar(t *testing.T) {
	started := false

	err := run(
		func(sidecar.BrainHandler) error {
			started = true
			return nil
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-quant" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, errors.New("license denied")
		},
	)
	if err == nil || err.Error() != "license: license denied" {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Fatal("sidecar should not start when license check fails")
	}
}

func TestRunStartsQuantBrainAfterLicenseCheck(t *testing.T) {
	wantErr := errors.New("boom")

	err := run(
		func(handler sidecar.BrainHandler) error {
			if handler.Kind() != agent.KindQuant {
				t.Fatalf("handler kind=%s, want %s", handler.Kind(), agent.KindQuant)
			}
			return wantErr
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-quant" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRuntimeBrainFromEnvRestoresPersistedQuantRuntime(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "brain-quant.json")
	stores, err := persistence.Open("file", dsn)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer stores.Close()

	for _, trace := range []persistence.SignalTrace{
		{TraceID: "pause-trading", Symbol: "control", Outcome: "pause_trading", RejectedStage: "control", Reason: "manual freeze"},
		{TraceID: "pause-symbol", Symbol: "BTC-USDT-SWAP", Outcome: "pause_instrument", RejectedStage: "control", Reason: "vol spike"},
		{TraceID: "recovery", Symbol: "control", Outcome: "recovery_completed", RejectedStage: "recovery", Reason: "accounts=1 recovered=1 failed=0"},
	} {
		trace := trace
		if err := stores.SignalTraceStore.Save(ctx, &trace); err != nil {
			t.Fatalf("SignalTraceStore.Save(%s): %v", trace.TraceID, err)
		}
	}

	t.Setenv("BRAIN_QUANT_PERSIST_DRIVER", "file")
	t.Setenv("BRAIN_QUANT_PERSIST_DSN", dsn)

	brain, closeFn, err := newRuntimeBrainFromEnv(ctx)
	if err != nil {
		t.Fatalf("newRuntimeBrainFromEnv: %v", err)
	}
	defer closeFn()

	result, err := brain.HandleMethod(ctx, "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name: quantcontracts.ToolQuantGlobalPortfolio,
	}))
	if err != nil {
		t.Fatalf("HandleMethod(tools/call): %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(result, &toolResult); err != nil {
		t.Fatalf("copyJSON: %v", err)
	}
	var payload struct {
		Runtime struct {
			TradingPaused     bool              `json:"trading_paused"`
			PausedInstruments map[string]string `json:"paused_instruments"`
			LastRecovery      string            `json:"last_recovery"`
		} `json:"runtime"`
	}
	if err := toolResult.DecodeOutput(&payload); err != nil {
		raw, _ := json.Marshal(result)
		t.Fatalf("DecodeOutput: %v\nraw=%s", err, raw)
	}
	if !payload.Runtime.TradingPaused {
		t.Fatal("expected restored trading pause state")
	}
	if got := payload.Runtime.PausedInstruments["BTC-USDT-SWAP"]; got != "vol spike" {
		t.Fatalf("paused instrument=%q, want vol spike", got)
	}
	if payload.Runtime.LastRecovery != "accounts=1 recovered=1 failed=0" {
		t.Fatalf("last recovery=%q", payload.Runtime.LastRecovery)
	}
}

func mustRawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return append(json.RawMessage(nil), raw...)
}

func copyJSON(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
