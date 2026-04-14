package data

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/internal/data/model"
	"github.com/leef-l/brain/internal/data/provider"
	"github.com/leef-l/brain/internal/data/service"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
)

func TestBrainHandleMethod_ToolAndExecute(t *testing.T) {
	brain := NewBrain(service.Config{RingCapacity: 8, DefaultTimeframe: "1m"})
	if err := brain.Service().RegisterProvider(provider.NewStaticProvider("okx")); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	brain.Service().StoreSnapshot(model.MarketSnapshot{
		Provider:  "fixture",
		Topic:     "trade",
		Symbol:    "BTC-USDT-SWAP",
		Timestamp: 1234567890,
		Price:     101.25,
		Volume:    12,
	})

	resp, err := brain.HandleMethod(context.Background(), "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name:      quantcontracts.ToolDataGetSnapshot,
		Arguments: json.RawMessage(`{"symbol":"BTC-USDT-SWAP"}`),
	}))
	if err != nil {
		t.Fatalf("HandleMethod tools/call: %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(resp, &toolResult); err != nil {
		t.Fatalf("copyJSON tool result: %v", err)
	}
	var output quantcontracts.SnapshotQueryResult
	if err := toolResult.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput: %v", err)
	}
	if output.Snapshot == nil || output.Snapshot.Symbol != "BTC-USDT-SWAP" {
		t.Fatalf("unexpected snapshot output: %+v", output)
	}

	execResp, err := brain.HandleMethod(context.Background(), "brain/execute", mustRawJSON(sidecar.ExecuteRequest{
		Instruction: quantcontracts.InstructionHealthCheck,
	}))
	if err != nil {
		t.Fatalf("HandleMethod brain/execute: %v", err)
	}
	var execResult sidecar.ExecuteResult
	if err := copyJSON(execResp, &execResult); err != nil {
		t.Fatalf("copyJSON execute result: %v", err)
	}
	if execResult.Status != "completed" {
		t.Fatalf("execute status=%q, want completed", execResult.Status)
	}
}

func TestBrainHandleMethod_HealthCheckFailsWithoutProviders(t *testing.T) {
	brain := NewBrain(service.Config{RingCapacity: 8, DefaultTimeframe: "1m"})

	execResp, err := brain.HandleMethod(context.Background(), "brain/execute", mustRawJSON(sidecar.ExecuteRequest{
		Instruction: quantcontracts.InstructionHealthCheck,
	}))
	if err != nil {
		t.Fatalf("HandleMethod brain/execute: %v", err)
	}
	var execResult sidecar.ExecuteResult
	if err := copyJSON(execResp, &execResult); err != nil {
		t.Fatalf("copyJSON execute result: %v", err)
	}
	if execResult.Status != "failed" {
		t.Fatalf("execute status=%q, want failed", execResult.Status)
	}
	if execResult.Error == "" {
		t.Fatal("health check should explain why data brain is not ready")
	}
}

func TestBrainHandleMethod_ReportsToolArgumentAndInstructionErrors(t *testing.T) {
	brain := NewBrain(service.Config{RingCapacity: 8, DefaultTimeframe: "1m"})

	resp, err := brain.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"data.get_snapshot",
		"arguments":"bad"
	}`))
	if err != nil {
		t.Fatalf("HandleMethod tools/call: %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(resp, &toolResult); err != nil {
		t.Fatalf("copyJSON tool result: %v", err)
	}
	if !toolResult.IsError || toolResult.Error == nil || toolResult.Error.Code != "invalid_arguments" {
		t.Fatalf("unexpected tool error: %+v", toolResult)
	}

	execResp, err := brain.HandleMethod(context.Background(), "brain/execute", mustRawJSON(sidecar.ExecuteRequest{
		Instruction: "unknown",
	}))
	if err != nil {
		t.Fatalf("HandleMethod brain/execute: %v", err)
	}
	var execResult sidecar.ExecuteResult
	if err := copyJSON(execResp, &execResult); err != nil {
		t.Fatalf("copyJSON execute result: %v", err)
	}
	if execResult.Status != "failed" {
		t.Fatalf("execute status=%q, want failed", execResult.Status)
	}
}

func TestBrainDoesNotExposeUnsupportedTools(t *testing.T) {
	brain := NewBrain(service.Config{RingCapacity: 8, DefaultTimeframe: "1m"})

	for _, name := range brain.Tools() {
		if name == model.ToolGetSimilar {
			t.Fatalf("unexpected unsupported tool exposure: %s", name)
		}
	}
	for _, schema := range brain.ToolSchemas() {
		if schema.Name == model.ToolGetSimilar {
			t.Fatalf("unexpected unsupported tool schema: %s", schema.Name)
		}
	}

	resp, err := brain.HandleMethod(context.Background(), "tools/call", mustRawJSON(protocol.ToolCallRequest{
		Name: model.ToolGetSimilar,
	}))
	if err != nil {
		t.Fatalf("HandleMethod tools/call: %v", err)
	}
	var toolResult protocol.ToolCallResult
	if err := copyJSON(resp, &toolResult); err != nil {
		t.Fatalf("copyJSON tool result: %v", err)
	}
	if !toolResult.IsError || toolResult.Error == nil || toolResult.Error.Code != "tool_not_found" {
		t.Fatalf("unexpected tool result: %+v", toolResult)
	}
}

func mustRawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func copyJSON(src any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
