package llm

import (
	"encoding/json"
	"errors"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

func TestValidateToolUseResponse_RejectsMissingToolName(t *testing.T) {
	err := ValidateToolUseResponse("test", &ChatResponse{
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: "tool_use", ToolUseID: "tu-1"},
		},
	})
	if err == nil {
		t.Fatal("want error for missing tool_name")
	}

	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("err=%T, want *BrainError", err)
	}
	if be.ErrorCode != brainerrors.CodeLLMUpstream5xx {
		t.Fatalf("code=%q, want %q", be.ErrorCode, brainerrors.CodeLLMUpstream5xx)
	}
}

func TestValidateToolUseResponse_RejectsMissingToolUseBlock(t *testing.T) {
	err := ValidateToolUseResponse("test", &ChatResponse{
		StopReason: "tool_use",
		Content: []ContentBlock{
			{Type: "text", Text: "thinking"},
		},
	})
	if err == nil {
		t.Fatal("want error for missing tool_use block")
	}
}

func TestContentBlock_JSONUsesWireToolFieldNames(t *testing.T) {
	raw, err := json.Marshal(ContentBlock{
		Type:      "tool_use",
		ToolUseID: "tu-1",
		ToolName:  "browser.open",
		Input:     json.RawMessage(`{"url":"https://example.com"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"tool_use","tool_use_id":"tu-1","tool_name":"browser.open","input":{"url":"https://example.com"}}` {
		t.Fatalf("json=%s", raw)
	}
}
