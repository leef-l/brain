package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/protocol"
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
				},
				"render_mode": {
					"type": "string",
					"enum": ["headed", "headless"],
					"description": "Optional explicit browser render mode preference for delegated UI tasks"
				}
			},
			"required": ["target_kind", "instruction"]
		}`),
		OutputSchema: json.RawMessage(`true`),
	}
}

func (t *DelegateTool) Risk() tool.Risk { return tool.RiskMedium }

func buildSubtaskContext(ctx context.Context, renderMode string) *protocol.SubtaskContext {
	subtask := kernel.SubtaskContextFromContext(ctx)
	if subtask == nil {
		subtask = &protocol.SubtaskContext{}
	}
	if renderMode != "" {
		subtask.RenderMode = renderMode
	}
	if subtask.UserUtterance == "" && subtask.RenderMode == "" && subtask.ParentRunID == "" && subtask.TurnIndex == 0 {
		return nil
	}
	return subtask
}

func wantsVisibleBrowser(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	needles := []string{
		"我要看到", "给我看", "让我看", "我要能看到", "可见浏览器",
		"可视化", "看得到", "看到操作", "看到你的操作", "看到浏览器", "浏览器窗口", "打开浏览器",
		"visible browser", "not headless", "non-headless", "headed",
		"show me the browser", "watch the browser", "show browser",
	}
	for _, n := range needles {
		if strings.Contains(s, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func resolveBrowserRenderMode(ctx context.Context, targetKind, instruction, renderMode string) string {
	if strings.TrimSpace(renderMode) != "" {
		return renderMode
	}
	if !strings.EqualFold(strings.TrimSpace(targetKind), "browser") {
		return ""
	}
	subtask := kernel.SubtaskContextFromContext(ctx)
	if subtask != nil && wantsVisibleBrowser(subtask.UserUtterance) {
		return "headed"
	}
	if wantsVisibleBrowser(instruction) {
		return "headed"
	}
	return ""
}

func (t *DelegateTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		TargetKind  string          `json:"target_kind"`
		Instruction string          `json:"instruction"`
		Context     json.RawMessage `json:"context,omitempty"`
		RenderMode  string          `json:"render_mode,omitempty"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %s"`, err.Error())),
			IsError: true,
		}, nil
	}

	if input.Instruction == "" {
		return &tool.Result{
			Output:  json.RawMessage(`"instruction is required"`),
			IsError: true,
		}, nil
	}

	renderMode := resolveBrowserRenderMode(ctx, input.TargetKind, input.Instruction, input.RenderMode)
	req := &kernel.DelegateRequest{
		TaskID:      fmt.Sprintf("delegate-%s", input.TargetKind),
		TargetKind:  agent.Kind(input.TargetKind),
		Instruction: input.Instruction,
		Context:     input.Context,
		Subtask:     buildSubtaskContext(ctx, renderMode),
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

	if strings.EqualFold(input.TargetKind, "browser") {
		if sanitized, failed, failMsg := sanitizeBrowserDelegateOutput(result.Output, input.Instruction); failed {
			return &tool.Result{
				Output:  json.RawMessage(fmt.Sprintf(`"subtask failed: %s"`, failMsg)),
				IsError: true,
			}, nil
		} else if sanitized != nil {
			return &tool.Result{Output: sanitized}, nil
		}
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

func sanitizeBrowserDelegateOutput(raw json.RawMessage, instruction string) (json.RawMessage, bool, string) {
	target := extractInstructionURL(instruction)
	targetHost := hostOfURL(target)
	if targetHost == "" || len(raw) == 0 {
		return raw, false, ""
	}
	var out struct {
		Status  string `json:"status"`
		Summary string `json:"summary,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return raw, false, ""
	}
	summaryHost := hostOfURL(extractSummaryURL(out.Summary))
	if summaryHost != "" && summaryHost != targetHost {
		return nil, true, fmt.Sprintf("browser result host mismatch: expected %s, got %s", targetHost, summaryHost)
	}
	return raw, false, ""
}

func extractSummaryURL(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		if strings.HasPrefix(line, "URL: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
		}
	}
	return ""
}

func extractInstructionURL(instruction string) string {
	for _, field := range strings.Fields(instruction) {
		if strings.HasPrefix(strings.ToLower(field), "http://") || strings.HasPrefix(strings.ToLower(field), "https://") {
			return strings.Trim(field, `"'<>`)
		}
	}
	return ""
}

func hostOfURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
