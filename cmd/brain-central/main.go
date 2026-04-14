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
	"path/filepath"
	"strings"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/internal/central"
	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/reviewrun"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
	"github.com/leef-l/brain/tool"
)

type centralHandler struct {
	caller sidecar.KernelCaller
	svc    central.API
}

type sidecarRunner func(sidecar.BrainHandler) error
type sidecarLicenseChecker func(string, license.VerifyOptions) (*license.Result, error)
type centralHandlerFactory func(context.Context) (*centralHandler, func() error, error)

func run(ctx context.Context, runSidecar sidecarRunner, checkLicense sidecarLicenseChecker) error {
	return runWithFactory(ctx, runSidecar, checkLicense, newRuntimeHandlerFromEnv)
}

func runWithFactory(ctx context.Context, runSidecar sidecarRunner, checkLicense sidecarLicenseChecker, factory centralHandlerFactory) error {
	if _, err := checkLicense("brain-central", license.VerifyOptions{}); err != nil {
		return fmt.Errorf("license: %w", err)
	}
	handler, closeFn, err := factory(ctx)
	if err != nil {
		return fmt.Errorf("init runtime: %w", err)
	}
	if closeFn != nil {
		defer closeFn()
	}
	return runSidecar(handler)
}

func newRuntimeHandlerFromEnv(ctx context.Context) (*centralHandler, func() error, error) {
	driver, dsn, err := centralPersistenceConfigFromEnv()
	if err != nil {
		return nil, nil, err
	}

	stores, err := persistence.Open(driver, dsn)
	if err != nil {
		return nil, nil, err
	}
	if stores.CentralStateStore == nil {
		_ = stores.Close()
		return nil, nil, fmt.Errorf("persistence driver %q does not provide a central state store", driver)
	}

	svc := central.New(central.Config{
		Control: control.Config{
			AutoPauseOnCriticalAlert: true,
			AutoPauseOnAccountError:  true,
			DefaultPauseReason:       "central control",
		},
		StateStore: stores.CentralStateStore,
	})
	if err := svc.RestoreState(ctx); err != nil {
		_ = stores.Close()
		return nil, nil, err
	}
	return &centralHandler{svc: svc}, stores.Close, nil
}

func centralPersistenceConfigFromEnv() (string, string, error) {
	driver := strings.TrimSpace(os.Getenv("BRAIN_CENTRAL_PERSIST_DRIVER"))
	if driver == "" {
		driver = "file"
	}

	dsn := strings.TrimSpace(os.Getenv("BRAIN_CENTRAL_PERSIST_DSN"))
	if dsn != "" || driver != "file" {
		return driver, dsn, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve central persistence home: %w", err)
	}
	return driver, filepath.Join(home, ".brain", "sidecars", "brain-central.json"), nil
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
	schemas := []tool.Schema{
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
		tool.NewEchoTool("central").Schema(),
		tool.NewRejectTaskTool("central", nil).Schema(),
	}
	schemas = append(schemas, quantToolSchemas()...)
	return schemas
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
		case quantcontracts.ToolCentralReviewTrade:
			return h.handleReviewTrade(ctx, req.Arguments)
		case quantcontracts.ToolCentralDataAlert:
			return h.handleQuantControlTool(ctx, req.Name, req.Arguments)
		case quantcontracts.ToolCentralAccountError:
			return h.handleQuantControlTool(ctx, req.Name, req.Arguments)
		case quantcontracts.ToolCentralMacroEvent:
			return h.handleQuantControlTool(ctx, req.Name, req.Arguments)
		default:
			return toolCallFailure(req.Name, "tool_not_found", fmt.Sprintf("tool %s not found", req.Name)), nil
		}
	case "brain/execute":
		return h.handleExecute(ctx, params)
	case "brain/plan":
		return map[string]interface{}{"status": "ok", "brain": "central", "action": "plan"}, nil
	default:
		return nil, sidecar.ErrMethodNotFound
	}
}

func (h *centralHandler) handleReviewTrade(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var req quantcontracts.ReviewTradeRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return toolCallFailure(quantcontracts.ToolCentralReviewTrade, "invalid_arguments", "invalid review_trade arguments: "+err.Error()), nil
		}
	}
	decision, err := h.service().ReviewTrade(ctx, req)
	if err != nil {
		return toolCallFailure(quantcontracts.ToolCentralReviewTrade, "review_failed", "review failed: "+err.Error()), nil
	}
	return toolCallSuccess(quantcontracts.ToolCentralReviewTrade, decision), nil
}

func (h *centralHandler) handleQuantControlTool(ctx context.Context, name string, args json.RawMessage) (interface{}, error) {
	switch name {
	case quantcontracts.ToolCentralDataAlert:
		var alert quantcontracts.DataAlert
		if err := json.Unmarshal(rawOrNull(args), &alert); err != nil {
			return toolCallFailure(name, "invalid_arguments", "invalid data_alert arguments: "+err.Error()), nil
		}
		result, err := h.service().DataAlert(ctx, alert)
		if err != nil {
			return toolCallFailure(name, "control_failed", "data_alert failed: "+err.Error()), nil
		}
		return toolCallSuccess(name, result), nil
	case quantcontracts.ToolCentralAccountError:
		var req quantcontracts.AccountError
		if err := json.Unmarshal(rawOrNull(args), &req); err != nil {
			return toolCallFailure(name, "invalid_arguments", "invalid account_error arguments: "+err.Error()), nil
		}
		result, err := h.service().AccountError(ctx, req)
		if err != nil {
			return toolCallFailure(name, "control_failed", "account_error failed: "+err.Error()), nil
		}
		return toolCallSuccess(name, result), nil
	case quantcontracts.ToolCentralMacroEvent:
		var event quantcontracts.MacroEvent
		if err := json.Unmarshal(rawOrNull(args), &event); err != nil {
			return toolCallFailure(name, "invalid_arguments", "invalid macro_event arguments: "+err.Error()), nil
		}
		result, err := h.service().MacroEvent(ctx, event)
		if err != nil {
			return toolCallFailure(name, "control_failed", "macro_event failed: "+err.Error()), nil
		}
		return toolCallSuccess(name, result), nil
	default:
		return toolCallFailure(name, "tool_not_found", fmt.Sprintf("tool %s not found", name)), nil
	}
}

func (h *centralHandler) handleExecute(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req sidecar.ExecuteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("parse request: %v", err),
		}, nil
	}

	switch req.Instruction {
	case quantcontracts.InstructionHealthCheck:
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: mustJSONString(h.service().Health()),
			Turns:   0,
		}, nil
	case quantcontracts.InstructionDailyReviewRun:
		result, err := h.runDailyReview(ctx, req.Context)
		if err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  fmt.Sprintf("daily review failed: %v", err),
			}, nil
		}
		return &sidecar.ExecuteResult{
			Status:  "completed",
			Summary: mustJSONString(result),
			Turns:   0,
		}, nil
	case quantcontracts.InstructionEmergencyAction,
		quantcontracts.InstructionUpdateConfig:
		result, err := h.handleCentralInstruction(ctx, req.Instruction, req.Context)
		if err != nil {
			return &sidecar.ExecuteResult{
				Status: "failed",
				Error:  err.Error(),
				Turns:  0,
			}, nil
		}
		return &sidecar.ExecuteResult{
			Status: "completed",
			Summary: mustJSONString(map[string]any{
				"instruction": req.Instruction,
				"status":      "applied",
				"result":      result,
				"health":      h.service().Health(),
			}),
			Turns: 0,
		}, nil
	default:
		return &sidecar.ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("unknown central instruction: %s", req.Instruction),
		}, nil
	}
}

func (h *centralHandler) service() central.API {
	if h != nil && h.svc != nil {
		return h.svc
	}
	return defaultCentralService()
}

func (h *centralHandler) runDailyReview(ctx context.Context, raw json.RawMessage) (reviewrun.Result, error) {
	var req reviewrun.Request
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return reviewrun.Result{}, fmt.Errorf("parse context: %w", err)
		}
	}
	return h.service().RunReview(ctx, req)
}

func (h *centralHandler) handleCentralInstruction(ctx context.Context, instruction string, raw json.RawMessage) (control.Result, error) {
	switch instruction {
	case quantcontracts.InstructionEmergencyAction:
		var req control.EmergencyActionRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				return control.Result{}, fmt.Errorf("parse emergency action: %w", err)
			}
		}
		result, err := h.service().EmergencyAction(ctx, req)
		if err != nil {
			return control.Result{}, fmt.Errorf("emergency action failed: %w", err)
		}
		if !result.OK {
			return control.Result{}, fmt.Errorf("emergency action rejected: %s", result.Reason)
		}
		return result, nil
	case quantcontracts.InstructionUpdateConfig:
		var req control.ConfigUpdateRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				return control.Result{}, fmt.Errorf("parse update config: %w", err)
			}
		}
		result, err := h.service().UpdateConfig(ctx, req)
		if err != nil {
			return control.Result{}, fmt.Errorf("update config failed: %w", err)
		}
		if !result.OK {
			return control.Result{}, fmt.Errorf("update config rejected: %s", result.Reason)
		}
		return result, nil
	default:
		return control.Result{}, fmt.Errorf("unsupported central instruction: %s", instruction)
	}
}

func defaultCentralService() central.API {
	return central.New(central.Config{
		Control: control.Config{
			AutoPauseOnCriticalAlert: true,
			AutoPauseOnAccountError:  true,
			DefaultPauseReason:       "central control",
		},
	})
}

func quantToolSchemas() []tool.Schema {
	return []tool.Schema{
		{
			Name:        quantcontracts.ToolCentralReviewTrade,
			Description: "Review a structured trade proposal and return an approval decision for the quant brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" },
    "symbol": { "type": "string" },
    "direction": { "type": "string" },
    "snapshot": { "type": "object" },
    "candidates": { "type": "array", "minItems": 1 },
    "reason": { "type": "string" }
  },
  "required": ["trace_id", "symbol", "direction", "candidates"]
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": { "type": "boolean" },
    "reason": { "type": "string" },
    "reason_code": { "type": "string" },
    "size_factor": { "type": "number" },
    "actions": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["approved", "size_factor"]
}`),
		},
		{
			Name:        quantcontracts.ToolCentralDataAlert,
			Description: "Accept a structured data alert from the data brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(`{
  "type":"object",
  "properties":{
    "action":{"type":"string"},
    "ok":{"type":"boolean"},
    "reason":{"type":"string"},
    "state":{"type":"object"}
  },
  "required":["action","ok","state"]
}`),
		},
		{
			Name:        quantcontracts.ToolCentralAccountError,
			Description: "Accept a structured account executor error from the quant brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(`{
  "type":"object",
  "properties":{
    "action":{"type":"string"},
    "ok":{"type":"boolean"},
    "reason":{"type":"string"},
    "state":{"type":"object"}
  },
  "required":["action","ok","state"]
}`),
		},
		{
			Name:        quantcontracts.ToolCentralMacroEvent,
			Description: "Accept a structured macro event from the data brain.",
			Brain:       "central",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(`{
  "type":"object",
  "properties":{
    "action":{"type":"string"},
    "ok":{"type":"boolean"},
    "reason":{"type":"string"},
    "state":{"type":"object"}
  },
  "required":["action","ok","state"]
}`),
		},
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

func mustJSONString(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal","detail":%q}`, err.Error())
	}
	return string(raw)
}

func main() {
	if err := run(context.Background(), sidecar.Run, license.CheckSidecar); err != nil {
		fmt.Fprintf(os.Stderr, "brain-central: %v\n", err)
		os.Exit(1)
	}
}
