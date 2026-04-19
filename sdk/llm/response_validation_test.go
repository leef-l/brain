package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestMessage_JSONUsesWireFieldNames(t *testing.T) {
	raw, err := json.Marshal(Message{
		Role: "assistant",
		Content: []ContentBlock{
			{
				Type:      "tool_use",
				ToolUseID: "tu-2",
				ToolName:  "browser.open",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"role":"assistant","content":[{"type":"tool_use","tool_use_id":"tu-2","tool_name":"browser.open"}]}` {
		t.Fatalf("json=%s", raw)
	}
}

func TestChatResponse_JSONUsesWireFieldNames(t *testing.T) {
	raw, err := json.Marshal(ChatResponse{
		ID:         "msg-1",
		Model:      "glm-5",
		StopReason: "tool_use",
		Content: []ContentBlock{
			{
				Type:      "tool_use",
				ToolUseID: "tu-3",
				ToolName:  "browser.open",
			},
		},
		Usage:      Usage{InputTokens: 1, OutputTokens: 2},
		FinishedAt: time.Date(2026, 4, 20, 7, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `"stop_reason":"tool_use"`) ||
		!strings.Contains(got, `"tool_use_id":"tu-3"`) ||
		!strings.Contains(got, `"tool_name":"browser.open"`) ||
		!strings.Contains(got, `"finished_at":"2026-04-20T07:00:00Z"`) {
		t.Fatalf("json=%s", raw)
	}
}
