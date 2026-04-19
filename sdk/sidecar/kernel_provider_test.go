package sidecar

import (
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/llm"
)

func TestChatRequestToWire_PreservesModelAndToolChoice(t *testing.T) {
	wire := chatRequestToWire(&llm.ChatRequest{
		Model:      "glm-5",
		ToolChoice: "browser.open",
		MaxTokens:  2048,
		System:     []llm.SystemBlock{{Text: "system"}},
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: "open baidu"}},
		}},
		Tools: []llm.ToolSchema{{
			Name:        "browser.open",
			Description: "open page",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})

	if wire.Model != "glm-5" {
		t.Fatalf("model=%q, want glm-5", wire.Model)
	}
	if wire.ToolChoice != "browser.open" {
		t.Fatalf("tool_choice=%q, want browser.open", wire.ToolChoice)
	}
	if wire.MaxTokens != 2048 {
		t.Fatalf("max_tokens=%d, want 2048", wire.MaxTokens)
	}
}
