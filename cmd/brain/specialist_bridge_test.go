package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRegisterSpecialistBridgeTools_QuantAvailable(t *testing.T) {
	orch := kernel.NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindQuant {
			return "/bin/sh", nil
		}
		return "", nil
	})

	reg := tool.NewMemRegistry()
	registerSpecialistBridgeTools(reg, orch)

	// Should have all 14 quant tools.
	var quantCount int
	for _, tt := range reg.List() {
		if tt.Schema().Brain == "quant" {
			quantCount++
		}
	}
	if quantCount != len(quantToolDefs) {
		t.Errorf("quant tools = %d, want %d", quantCount, len(quantToolDefs))
	}

	// No data tools (data not available).
	for _, tt := range reg.List() {
		if tt.Schema().Brain == "data" {
			t.Errorf("unexpected data tool: %s", tt.Name())
		}
	}
}

func TestRegisterSpecialistBridgeTools_BothAvailable(t *testing.T) {
	orch := kernel.NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindQuant || kind == agent.KindData {
			return "/bin/sh", nil
		}
		return "", nil
	})

	reg := tool.NewMemRegistry()
	registerSpecialistBridgeTools(reg, orch)

	total := len(reg.List())
	expected := len(quantToolDefs) + len(dataToolDefs)
	if total != expected {
		t.Errorf("total bridge tools = %d, want %d", total, expected)
	}
}

func TestRegisterSpecialistBridgeTools_NoneAvailable(t *testing.T) {
	orch := kernel.NewOrchestrator(nil, nil, nil)

	reg := tool.NewMemRegistry()
	registerSpecialistBridgeTools(reg, orch)

	if len(reg.List()) != 0 {
		t.Errorf("expected 0 tools, got %d", len(reg.List()))
	}
}

func TestRegisterSpecialistBridgeTools_NilOrch(t *testing.T) {
	reg := tool.NewMemRegistry()
	registerSpecialistBridgeTools(reg, nil)

	if len(reg.List()) != 0 {
		t.Errorf("expected 0 tools, got %d", len(reg.List()))
	}
}

func TestBridgeTool_UnavailableKind(t *testing.T) {
	// When the sidecar kind is not available, Execute should return a graceful
	// error result (not panic).
	orch := kernel.NewOrchestrator(nil, nil, nil)

	bt := &bridgeTool{
		Sch: tool.Schema{
			Name:        "quant.global_portfolio",
			Description: "test",
			Brain:       "quant",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Rsk:  tool.RiskSafe,
		Kind: agent.KindQuant,
		Orch: orch,
	}

	result, err := bt.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for unavailable kind")
	}
	t.Logf("error output: %s", result.Output)
}

func TestBridgeTool_UnavailableSidecar(t *testing.T) {
	orch := kernel.NewOrchestrator(nil, nil, nil)

	bt := &bridgeTool{
		Sch:  tool.Schema{Name: "data.get_snapshot", Brain: "data"},
		Rsk:  tool.RiskSafe,
		Kind: agent.KindData,
		Orch: orch,
	}

	result, err := bt.Execute(context.Background(), json.RawMessage(`{"instrument_id":"BTC"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for unavailable sidecar")
	}
	t.Logf("error output: %s", result.Output)
}

func TestBridgeTool_SchemaAndRisk(t *testing.T) {
	orch := kernel.NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		return "/bin/sh", nil
	})

	reg := tool.NewMemRegistry()
	registerSpecialistBridgeTools(reg, orch)

	// Check a few tool properties.
	for _, name := range []string{"quant.global_portfolio", "quant.force_close", "data.get_snapshot"} {
		tt, ok := reg.Lookup(name)
		if !ok {
			t.Errorf("tool %s not found", name)
			continue
		}
		s := tt.Schema()
		if s.Name != name {
			t.Errorf("schema name = %q, want %q", s.Name, name)
		}
		if len(s.InputSchema) == 0 {
			t.Errorf("tool %s: InputSchema empty", name)
		}
	}

	// Verify risk levels.
	if tt, ok := reg.Lookup("quant.global_portfolio"); ok {
		if tt.Risk() != tool.RiskSafe {
			t.Errorf("global_portfolio risk = %v, want safe", tt.Risk())
		}
	}
	if tt, ok := reg.Lookup("quant.force_close"); ok {
		if tt.Risk() != tool.RiskCritical {
			t.Errorf("force_close risk = %v, want critical", tt.Risk())
		}
	}
	if tt, ok := reg.Lookup("quant.pause_trading"); ok {
		if tt.Risk() != tool.RiskMedium {
			t.Errorf("pause_trading risk = %v, want medium", tt.Risk())
		}
	}
}

// Compile-time check: bridgeTool must implement tool.Tool.
var _ tool.Tool = (*bridgeTool)(nil)

// Verify protocol import is used correctly.
var _ = protocol.SpecialistToolCallRequest{}
