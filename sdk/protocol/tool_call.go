package protocol

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/executionpolicy"
)

// ToolCallRequest is the direct tool invocation payload sent to a sidecar.
// execution is optional; when present, the sidecar MUST rebuild its tool
// registry under the provided execution boundary before invoking the tool.
type ToolCallRequest struct {
	Name      string                         `json:"name"`
	Arguments json.RawMessage                `json:"arguments,omitempty"`
	Execution *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
}

// ToolCallContent is the minimal structured payload returned by tools/call.
type ToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCallError is the machine-readable error envelope for tools/call and
// specialist.call_tool. It complements the compatibility content[] text.
type ToolCallError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ToolCallResult is the normalized result returned by a sidecar tools/call.
type ToolCallResult struct {
	Tool    string            `json:"tool,omitempty"`
	Output  json.RawMessage   `json:"output,omitempty"`
	Content []ToolCallContent `json:"content,omitempty"`
	IsError bool              `json:"isError,omitempty"`
	Error   *ToolCallError    `json:"error,omitempty"`
}

// CanonicalOutput returns the machine-readable JSON payload for this tool
// result. It prefers Output and falls back to a single JSON text content item
// for compatibility with older sidecars.
func (r ToolCallResult) CanonicalOutput() json.RawMessage {
	if len(r.Output) > 0 {
		return append(json.RawMessage(nil), r.Output...)
	}
	if len(r.Content) == 1 && r.Content[0].Type == "text" {
		raw := json.RawMessage(r.Content[0].Text)
		if json.Valid(raw) {
			return append(json.RawMessage(nil), raw...)
		}
	}
	return nil
}

// OutputOrEnvelope returns the canonical JSON output when available; otherwise
// it serializes the full ToolCallResult envelope so callers still have a stable
// JSON payload to propagate.
func (r ToolCallResult) OutputOrEnvelope() json.RawMessage {
	if raw := r.CanonicalOutput(); len(raw) > 0 {
		return raw
	}
	raw, _ := json.Marshal(r)
	return raw
}

// DecodeOutput decodes the canonical JSON output into dst.
func (r ToolCallResult) DecodeOutput(dst interface{}) error {
	raw := r.CanonicalOutput()
	if len(raw) == 0 {
		return fmt.Errorf("tool %s returned no decodable output", r.Tool)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode output for tool %s: %w", r.Tool, err)
	}
	return nil
}

// ErrorMessage returns the best available human-readable error message.
func (r ToolCallResult) ErrorMessage() string {
	if r.Error != nil && strings.TrimSpace(r.Error.Message) != "" {
		return strings.TrimSpace(r.Error.Message)
	}
	if len(r.Content) == 1 && r.Content[0].Type == "text" {
		return strings.TrimSpace(r.Content[0].Text)
	}
	return ""
}

// SpecialistToolCallRequest asks the Kernel to route a direct tool call to a
// target specialist brain without going through brain/execute.
type SpecialistToolCallRequest struct {
	TargetKind agent.Kind                     `json:"target_kind"`
	ToolName   string                         `json:"tool_name"`
	Arguments  json.RawMessage                `json:"arguments,omitempty"`
	Execution  *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
}
