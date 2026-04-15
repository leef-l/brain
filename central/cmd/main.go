// Command brain-central is the CentralBrain sidecar binary.
//
// CentralBrain is the orchestrator brain that owns the plan, delegates
// subtasks to specialist brains, and manages the overall task lifecycle.
// See 02-BrainKernel设计.md §3.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/central/llm"
	cquant "github.com/leef-l/brain/central/quant"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/sidecar"
	"github.com/leef-l/brain/sdk/tool"
)

type centralHandler struct {
	caller       sidecar.KernelCaller
	quantHandler *cquant.Handler
}

func (h *centralHandler) Kind() agent.Kind { return agent.KindCentral }
func (h *centralHandler) Version() string  { return brain.SDKVersion }
func (h *centralHandler) Tools() []string {
	schemas := h.ToolSchemas()
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	return names
}

func (h *centralHandler) ToolSchemas() []tool.Schema {
	return []tool.Schema{
		{
			Name:        "central.plan_create",
			Description: "Create a new plan skeleton or plan request in the central brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "action": { "const": "plan_create" },
    "request": {}
  },
  "required": ["status", "action"]
}`),
		},
		{
			Name:        "central.plan_update",
			Description: "Update an existing plan or submit a plan patch request in the central brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": { "type": "string" },
    "action": { "const": "plan_update" },
    "request": {}
  },
  "required": ["status", "action"]
}`),
		},
		{
			Name:        "central.delegate",
			Description: "Delegate a subtask to a specialist brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "target_kind": { "type": "string" },
    "instruction": { "type": "string" },
    "context": { "type": "object" }
  },
  "required": ["target_kind", "instruction"]
}`),
			OutputSchema: json.RawMessage(`true`),
		},
		{
			Name:        "central.review_trade",
			Description: "Review a trade decision via LLM analysis. Called by quant brain when NeedsReview is triggered.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "signal": { "type": "object" },
    "portfolio": { "type": "object" },
    "market": { "type": "object" },
    "reason": { "type": "string" }
  },
  "required": ["signal", "portfolio", "market"]
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": { "type": "boolean" },
    "size_factor": { "type": "number", "minimum": 0, "maximum": 1 },
    "reason": { "type": "string" }
  },
  "required": ["approved", "size_factor", "reason"]
}`),
		},
		{
			Name:        "central.daily_review",
			Description: "Run end-of-day analysis: collect trading statistics, generate LLM insights, and suggest strategy adjustments.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "date": { "type": "string" },
    "accounts": { "type": "array" },
    "strategy_stats": { "type": "array" },
    "total_trades": { "type": "integer" },
    "total_pnl": { "type": "number" }
  }
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "assessment": { "type": "string" },
    "strategy_notes": { "type": "string" },
    "risk_notes": { "type": "string" },
    "actions": { "type": "array" }
  }
}`),
		},
		{
			Name:        "central.data_alert",
			Description: "Receive and process data quality alerts from the data brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "level": { "type": "string", "enum": ["warning", "critical"] },
    "alert_type": { "type": "string" },
    "symbol": { "type": "string" },
    "detail": { "type": "string" },
    "event_ts": { "type": "integer" }
  },
  "required": ["level", "alert_type"]
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "received": { "type": "boolean" },
    "action": { "type": "string" },
    "description": { "type": "string" }
  }
}`),
		},
		tool.NewEchoTool("central").Schema(),
		tool.NewRejectTaskTool("central", nil).Schema(),
	}
}

// SetKernelCaller implements sidecar.RichBrainHandler.
func (h *centralHandler) SetKernelCaller(caller sidecar.KernelCaller) {
	h.caller = caller
}

func (h *centralHandler) HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "tools/call":
		var req struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return toolCallFailure("", "parse_request", "parse error: "+err.Error()), nil
		}
		switch req.Name {
		case "central.delegate":
			return h.handleDelegate(ctx, req.Arguments)
		case "central.echo":
			return executeLocalTool(ctx, req.Name, tool.NewEchoTool("central"), req.Arguments)
		case "central.reject_task":
			return executeLocalTool(ctx, req.Name, tool.NewRejectTaskTool("central", nil), req.Arguments)
		case "central.review_trade":
			return h.handleQuantTool(ctx, req.Name, h.quantHandler.HandleReviewTrade, req.Arguments)
		case "central.daily_review":
			return h.handleQuantTool(ctx, req.Name, h.quantHandler.HandleDailyReview, req.Arguments)
		case "central.data_alert":
			return h.handleQuantTool(ctx, req.Name, h.quantHandler.HandleDataAlert, req.Arguments)
		case "central.plan_create":
			return toolCallSuccess(req.Name, map[string]interface{}{
				"status":  "ok",
				"action":  "plan_create",
				"request": rawOrNull(req.Arguments),
			}), nil
		case "central.plan_update":
			return toolCallSuccess(req.Name, map[string]interface{}{
				"status":  "ok",
				"action":  "plan_update",
				"request": rawOrNull(req.Arguments),
			}), nil
		default:
			return toolCallFailure(req.Name, "tool_not_found", fmt.Sprintf("tool %s not found", req.Name)), nil
		}
	case "brain/execute":
		return map[string]interface{}{"status": "ok", "brain": "central"}, nil
	case "brain/plan":
		return map[string]interface{}{"status": "ok", "brain": "central", "action": "plan"}, nil
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

// handleDelegate delegates a subtask to a specialist brain via the Kernel's
// subtask.delegate reverse RPC.
func (h *centralHandler) handleDelegate(ctx context.Context, args json.RawMessage) (interface{}, error) {
	if h.caller == nil {
		return toolCallFailure("central.delegate", "delegate_unavailable", "delegate unavailable: no KernelCaller (running in solo mode?)"), nil
	}

	// Parse delegate arguments.
	var delegateArgs struct {
		TargetKind  string          `json:"target_kind"`
		Instruction string          `json:"instruction"`
		Context     json.RawMessage `json:"context,omitempty"`
	}
	if err := json.Unmarshal(args, &delegateArgs); err != nil {
		return toolCallFailure("central.delegate", "invalid_arguments", "invalid delegate arguments: "+err.Error()), nil
	}

	if delegateArgs.TargetKind == "" {
		return toolCallFailure("central.delegate", "invalid_arguments", "target_kind is required"), nil
	}
	if delegateArgs.Instruction == "" {
		return toolCallFailure("central.delegate", "invalid_arguments", "instruction is required"), nil
	}

	// Build subtask.delegate request.
	delegateReq := map[string]interface{}{
		"task_id":     fmt.Sprintf("delegate-%s", delegateArgs.TargetKind),
		"target_kind": delegateArgs.TargetKind,
		"instruction": delegateArgs.Instruction,
	}
	if delegateArgs.Context != nil {
		delegateReq["context"] = delegateArgs.Context
	}

	// Call subtask.delegate via reverse RPC to the Kernel.
	var result json.RawMessage
	if err := h.caller.CallKernel(ctx, protocol.MethodSubtaskDelegate, delegateReq, &result); err != nil {
		return toolCallFailure("central.delegate", "delegate_failed", fmt.Sprintf("delegate to %s failed: %v", delegateArgs.TargetKind, err)), nil
	}

	// Parse and relay the result.
	var subtaskResult struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	if err := json.Unmarshal(result, &subtaskResult); err != nil {
		return toolCallSuccessRaw("central.delegate", result), nil
	}

	if subtaskResult.Status == "rejected" {
		return toolCallFailure("central.delegate", "delegation_rejected", fmt.Sprintf("delegation rejected: %s", subtaskResult.Error)), nil
	}
	if subtaskResult.Status == "failed" {
		return toolCallFailure("central.delegate", "subtask_failed", fmt.Sprintf("subtask failed: %s", subtaskResult.Error)), nil
	}

	if subtaskResult.Output != nil {
		return toolCallSuccessRaw("central.delegate", subtaskResult.Output), nil
	}
	return toolCallSuccess("central.delegate", map[string]interface{}{
		"status":      "completed",
		"target_kind": delegateArgs.TargetKind,
	}), nil
}

// handleQuantTool routes a tool call to one of the quant handler methods.
func (h *centralHandler) handleQuantTool(
	ctx context.Context,
	name string,
	fn func(context.Context, json.RawMessage) (json.RawMessage, error),
	args json.RawMessage,
) (protocol.ToolCallResult, error) {
	if h.quantHandler == nil {
		return toolCallFailure(name, "not_configured", "quant handler not configured (LLM_API_KEY not set)"), nil
	}
	result, err := fn(ctx, args)
	if err != nil {
		return toolCallFailure(name, "handler_error", err.Error()), nil
	}
	return toolCallSuccessRaw(name, result), nil
}

func executeLocalTool(ctx context.Context, name string, builtin tool.Tool, args json.RawMessage) (protocol.ToolCallResult, error) {
	result, err := builtin.Execute(ctx, args)
	if err != nil {
		return toolCallFailure(name, "tool_execution_failed", err.Error()), nil
	}
	if result == nil {
		return toolCallFailure(name, "tool_execution_failed", "tool returned no result"), nil
	}
	if result.IsError {
		return protocol.ToolCallResult{
			Tool:    name,
			Output:  append(json.RawMessage(nil), result.Output...),
			IsError: true,
			Error: &protocol.ToolCallError{
				Code:    "tool_execution_failed",
				Message: rawText(result.Output),
			},
			Content: []protocol.ToolCallContent{{Type: "text", Text: rawText(result.Output)}},
		}, nil
	}
	return toolCallSuccessRaw(name, result.Output), nil
}

func toolCallSuccess(name string, output interface{}) protocol.ToolCallResult {
	raw, _ := json.Marshal(output)
	return toolCallSuccessRaw(name, raw)
}

func toolCallSuccessRaw(name string, raw json.RawMessage) protocol.ToolCallResult {
	return protocol.ToolCallResult{
		Tool:   name,
		Output: append(json.RawMessage(nil), raw...),
		Content: []protocol.ToolCallContent{{
			Type: "text",
			Text: rawText(raw),
		}},
	}
}

func toolCallFailure(name, code, msg string) protocol.ToolCallResult {
	return protocol.ToolCallResult{
		Tool:    name,
		IsError: true,
		Error: &protocol.ToolCallError{
			Code:    code,
			Message: msg,
		},
		Content: []protocol.ToolCallContent{{Type: "text", Text: msg}},
	}
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func main() {
	if _, err := license.CheckSidecar("brain-central", license.VerifyOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "brain-central: license: %v\n", err)
		os.Exit(1)
	}

	handler := &centralHandler{}

	// Initialize LLM client for quant handlers if API key is set.
	// Supports DeepSeek V3.2, Claude, HunYuan, or any OpenAI-compatible API.
	// Env vars: LLM_API_KEY, LLM_BASE_URL (default: DeepSeek), LLM_MODEL
	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey != "" {
		cfg := llm.DefaultConfig()
		cfg.APIKey = apiKey
		if baseURL := os.Getenv("LLM_BASE_URL"); baseURL != "" {
			cfg.BaseURL = baseURL
		}
		if model := os.Getenv("LLM_MODEL"); model != "" {
			cfg.Model = model
		}
		client := llm.New(cfg)
		handler.quantHandler = cquant.NewHandler(client, nil)
		fmt.Fprintf(os.Stderr, "brain-central: LLM enabled (model=%s, base=%s)\n", cfg.Model, cfg.BaseURL)
	} else {
		fmt.Fprintln(os.Stderr, "brain-central: LLM disabled (set LLM_API_KEY to enable trade review)")
	}

	if err := sidecar.Run(handler); err != nil {
		fmt.Fprintf(os.Stderr, "brain-central: %v\n", err)
		os.Exit(1)
	}
}
