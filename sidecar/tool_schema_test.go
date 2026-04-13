package sidecar

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/tool"
)

type schemaTestHandler struct {
	testHandler
	schemas []tool.Schema
}

func (h *schemaTestHandler) ToolSchemas() []tool.Schema { return h.schemas }

func TestToolSpecsForHandler_UsesFullSchemasWhenAvailable(t *testing.T) {
	handler := &schemaTestHandler{
		testHandler: testHandler{kind: agent.KindBrowser, version: "1.0.0", tools: []string{"browser.eval"}},
		schemas: []tool.Schema{{
			Name:         "browser.eval",
			Description:  "Eval JS",
			InputSchema:  json.RawMessage(`{"type":"object"}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"value":{}}}`),
			Brain:        "browser",
		}},
	}

	specs := toolSpecsForHandler(handler)
	if len(specs) != 1 {
		t.Fatalf("len(specs)=%d, want 1", len(specs))
	}
	if specs[0].Description != "Eval JS" {
		t.Fatalf("Description=%q, want Eval JS", specs[0].Description)
	}
	if len(specs[0].InputSchema) == 0 {
		t.Fatal("InputSchema should be present")
	}
	if len(specs[0].OutputSchema) == 0 {
		t.Fatal("OutputSchema should be present")
	}
}

func TestToolSpecsForHandler_FallsBackToNamesOnly(t *testing.T) {
	handler := &testHandler{kind: agent.KindCode, version: "1.0.0", tools: []string{"code.read_file"}}
	specs := toolSpecsForHandler(handler)
	if len(specs) != 1 {
		t.Fatalf("len(specs)=%d, want 1", len(specs))
	}
	if specs[0].Name != "code.read_file" {
		t.Fatalf("Name=%q, want code.read_file", specs[0].Name)
	}
	if len(specs[0].InputSchema) != 0 || len(specs[0].OutputSchema) != 0 {
		t.Fatalf("fallback specs should not contain schemas: %+v", specs[0])
	}
}

func (h *schemaTestHandler) HandleMethod(_ context.Context, method string, params json.RawMessage) (interface{}, error) {
	return h.testHandler.HandleMethod(context.Background(), method, params)
}
