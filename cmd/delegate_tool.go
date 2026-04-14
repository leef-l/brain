package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/tool"
)

// delegateTool is a tool.Tool that delegates subtasks to specialist brains
// via the Orchestrator. It is registered in the main process Agent Loop
// when specialist sidecar binaries are available.
type delegateTool struct {
	orchestrator *kernel.Orchestrator
	env          *executionEnvironment
	available    []string // human-readable list of available specialist kinds
}

func newDelegateTool(orch *kernel.Orchestrator, env *executionEnvironment) *delegateTool {
	kinds := orch.AvailableKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return &delegateTool{
		orchestrator: orch,
		env:          env,
		available:    names,
	}
}

func (t *delegateTool) Name() string { return "central.delegate" }

func (t *delegateTool) Schema() tool.Schema {
	desc := "Delegate a subtask to a specialist brain. " +
		"Use this when a task requires specialized capabilities. "
	if len(t.available) > 0 {
		desc += fmt.Sprintf("Available specialists: %v. ", t.available)
	}
	desc += "The specialist will execute the task independently and return results."

	return tool.Schema{
		Name:        "central.delegate",
		Description: desc,
		Brain:       "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"target_kind": {
					"type": "string",
					"description": "The specialist brain to delegate to (e.g. data, quant, code, browser, verifier)"
				},
				"instruction": {
					"type": "string",
					"description": "Clear, detailed task description for the specialist brain"
				},
				"context": {
					"type": "object",
					"description": "Optional structured context (file paths, prior results, etc.)"
				}
			},
			"required": ["target_kind", "instruction"]
		}`),
		OutputSchema: json.RawMessage(`true`),
	}
}

func (t *delegateTool) Risk() tool.Risk { return tool.RiskMedium }

func (t *delegateTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		TargetKind  string          `json:"target_kind"`
		Instruction string          `json:"instruction"`
		Context     json.RawMessage `json:"context,omitempty"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %s"`, err.Error())),
			IsError: true,
		}, nil
	}

	if input.TargetKind == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"target_kind is required"`),
			IsError: true,
		}, nil
	}
	if input.Instruction == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"instruction is required"`),
			IsError: true,
		}, nil
	}

	req := &kernel.SubtaskRequest{
		TaskID:      fmt.Sprintf("delegate-%s", input.TargetKind),
		TargetKind:  agent.Kind(input.TargetKind),
		Instruction: input.Instruction,
		Context:     input.Context,
		Execution:   t.env.executionSpec(),
	}
	if deadline, ok := ctx.Deadline(); ok {
		timeout := time.Until(deadline)
		if timeout > 0 {
			req.Budget = &kernel.SubtaskBudget{Timeout: timeout}
		}
	}

	result, err := t.orchestrator.Delegate(ctx, req)
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"delegation error: %s"`, err.Error())),
			IsError: true,
		}, nil
	}

	if result.Status == "rejected" {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"delegation rejected: %s — handle the task yourself"`, result.Error)),
			IsError: true,
		}, nil
	}

	if result.Status == "failed" {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"subtask failed: %s"`, result.Error)),
			IsError: true,
		}, nil
	}

	// Return the specialist's output.
	if result.Output != nil {
		return &tool.Result{Output: result.Output}, nil
	}
	return &tool.Result{
		Output: json.RawMessage(`"subtask completed successfully"`),
	}, nil
}

// Compile-time assertion.
var _ tool.Tool = (*delegateTool)(nil)
