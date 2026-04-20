package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

type DelegateTool struct {
	Orchestrator *kernel.Orchestrator
	Env          *env.Environment
	Available    []string
}

func NewDelegateTool(orch *kernel.Orchestrator, e *env.Environment) *DelegateTool {
	kinds := orch.AvailableKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return &DelegateTool{
		Orchestrator: orch,
		Env:          e,
		Available:    names,
	}
}

func (t *DelegateTool) Name() string { return "central.delegate" }

func (t *DelegateTool) Schema() tool.Schema {
	desc := "Delegate a subtask to a specialist brain. " +
		"Use this when a task requires specialized capabilities. "
	if len(t.Available) > 0 {
		desc += fmt.Sprintf("Available specialists: %v. ", t.Available)
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
					"description": "The specialist brain to delegate to (e.g. code, browser, verifier)"
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

func (t *DelegateTool) Risk() tool.Risk { return tool.RiskMedium }

func (t *DelegateTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
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
		Execution:   t.Env.ExecutionSpec(),
	}
	if deadline, ok := ctx.Deadline(); ok {
		timeout := time.Until(deadline)
		if timeout > 0 {
			req.Budget = &kernel.SubtaskBudget{Timeout: timeout}
		}
	}

	// 实时进度标记:delegate 到专家大脑的调用通常 20-60 秒,中间没有流式
	// 输出,用户看到就是"Run: central.delegate ... [长时间无响应] ... Done"。
	// 先打一行可见的"正在委托给 X 大脑"让用户知道系统在动。
	fmt.Printf("\033[2m    → delegating to %s brain (may take 20-60s)...\033[0m\n", input.TargetKind)

	result, err := t.Orchestrator.Delegate(ctx, req)
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

	if result.Output != nil {
		return &tool.Result{Output: result.Output}, nil
	}
	return &tool.Result{
		Output: json.RawMessage(`"subtask completed successfully"`),
	}, nil
}

var _ tool.Tool = (*DelegateTool)(nil)

func RegisterDelegateToolIfAvailable(reg tool.Registry, orch *kernel.Orchestrator, e *env.Environment) {
	if reg == nil || orch == nil || len(orch.AvailableKinds()) == 0 {
		return
	}
	_ = reg.Register(NewDelegateTool(orch, e))
}

func RegisterDelegateToolForEnvironment(reg tool.Registry, orch *kernel.Orchestrator, e *env.Environment) {
	if e != nil && !e.AllowsDelegation() {
		return
	}
	RegisterDelegateToolIfAvailable(reg, orch, e)
}
