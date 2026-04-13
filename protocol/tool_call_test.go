package protocol

import (
	"encoding/json"
	"testing"
)

func TestToolCallResultCanonicalOutput_PrefersOutput(t *testing.T) {
	result := ToolCallResult{
		Tool:    "browser.screenshot",
		Output:  json.RawMessage(`{"status":"ok"}`),
		Content: []ToolCallContent{{Type: "text", Text: `{"legacy":true}`}},
	}

	if got := string(result.CanonicalOutput()); got != `{"status":"ok"}` {
		t.Fatalf("CanonicalOutput()=%s, want {\"status\":\"ok\"}", got)
	}
}

func TestToolCallResultCanonicalOutput_FallsBackToLegacyContent(t *testing.T) {
	result := ToolCallResult{
		Tool:    "browser.screenshot",
		Content: []ToolCallContent{{Type: "text", Text: `{"status":"ok"}`}},
	}

	if got := string(result.CanonicalOutput()); got != `{"status":"ok"}` {
		t.Fatalf("CanonicalOutput()=%s, want {\"status\":\"ok\"}", got)
	}
}

func TestToolCallResultDecodeOutput(t *testing.T) {
	result := ToolCallResult{
		Tool:   "browser.eval",
		Output: json.RawMessage(`{"title":"OKX"}`),
	}

	var out struct {
		Title string `json:"title"`
	}
	if err := result.DecodeOutput(&out); err != nil {
		t.Fatalf("DecodeOutput: %v", err)
	}
	if out.Title != "OKX" {
		t.Fatalf("out.Title=%q, want OKX", out.Title)
	}
}

func TestToolCallResultOutputOrEnvelope_FallsBackToEnvelope(t *testing.T) {
	result := ToolCallResult{
		Tool:    "browser.eval",
		IsError: true,
		Error:   &ToolCallError{Code: "tool_not_found", Message: "tool not found"},
	}

	raw := result.OutputOrEnvelope()
	if !json.Valid(raw) {
		t.Fatalf("OutputOrEnvelope() returned invalid JSON: %s", raw)
	}
	if string(raw) == "" || string(raw) == "null" {
		t.Fatalf("OutputOrEnvelope()=%s, want non-empty envelope", raw)
	}
}

func TestToolCallResultErrorMessage_PrefersStructuredError(t *testing.T) {
	result := ToolCallResult{
		Error: &ToolCallError{Code: "tool_not_found", Message: "tool not found"},
		Content: []ToolCallContent{
			{Type: "text", Text: "legacy error"},
		},
	}

	if got := result.ErrorMessage(); got != "tool not found" {
		t.Fatalf("ErrorMessage()=%q, want tool not found", got)
	}
}
