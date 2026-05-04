package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
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

	// failsMu / failCount 跟踪同 (target_kind, instruction-hash) 失败次数。
	// LLM 看到 fail_count + STOP retrying hint 才会换 args 或停手,否则会
	// 反复调相同 args 触发 LoopDetector 整个 run 死掉(用户日志真实场景)。
	failsMu   sync.Mutex
	failCount map[string]int
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
		failCount:    make(map[string]int),
	}
}

func (t *DelegateTool) bumpFailCount(key string) int {
	t.failsMu.Lock()
	defer t.failsMu.Unlock()
	t.failCount[key]++
	return t.failCount[key]
}

func (t *DelegateTool) peekFailCount(key string) int {
	t.failsMu.Lock()
	defer t.failsMu.Unlock()
	return t.failCount[key]
}

func sha256Short(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:8]
}

func (t *DelegateTool) Name() string { return "central.delegate" }

func (t *DelegateTool) Schema() tool.Schema {
	desc := "Delegate ONE subtask to a specialist brain — this is how you ACTUALLY do work. " +
		"You (central) cannot write/edit/delete files or run shell — you must delegate those. " +
		"Pass user-supplied values (credentials, URLs, queries) verbatim, never as $placeholders. "
	if len(t.Available) > 0 {
		desc += fmt.Sprintf("Specialists: %v. ", t.Available)
	}
	desc += "For multi-step / multi-brain work, prefer central.submit_workflow."

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
	subtask := buildSubtaskContext(ctx, renderMode)
	req := &kernel.DelegateRequest{
		TaskID:      fmt.Sprintf("delegate-%s", input.TargetKind),
		TargetKind:  agent.Kind(input.TargetKind),
		Instruction: input.Instruction,
		Context:     input.Context,
		Subtask:     subtask,
		Execution:   t.Env.ExecutionSpec(),
		Workdir:     t.Env.Workdir, // workdir 端到端：host 显式告诉 sidecar 用哪个工作目录
	}
	// MACCS Wave 7+ 项目级持久化:从 SubtaskContext 透传 ProjectID 到 DelegateRequest,
	// 让 Orchestrator.delegateOnce 的 Assemble 自动加载项目历史 + 项目记忆。
	// chat 模式 SubtaskContext.ProjectID 由 chat/executor.go::runChatTurn 填充。
	if subtask != nil && subtask.ProjectID != "" {
		req.ProjectID = subtask.ProjectID
	}
	// 自适应 budget：用 ComplexityEstimator 按 instruction 内容估算
	// 实际所需 turn / LLM call / tool call。委派任务粒度差异很大（"读个
	// 文件"和"写一个 800 行的 HTML"完全不同），用估计器比硬编码合理。
	//
	// estimator 不可用时退化为保守基线（25 turn）—— 比 sidecar 默认 10
	// 多得多，避免 turns_exhausted。
	estimated := estimateDelegateTurns(input.TargetKind, input.Instruction)
	req.Budget = &kernel.SubtaskBudget{
		MaxTurns: estimated,
	}
	if deadline, ok := ctx.Deadline(); ok {
		timeout := time.Until(deadline)
		if timeout > 0 {
			req.Budget.Timeout = timeout
		}
	}

	// 默认静默：spinner 行会显示"委派 <kind> 大脑"反映进度。
	// /verbose 模式由 chat 包通过 VerbosePrint hook 接管。
	if VerbosePrint != nil {
		VerbosePrint(fmt.Sprintf("\033[2m    → delegating to %s brain (may take 20-60s)...\033[0m\n", input.TargetKind))
	}

	result, err := t.Orchestrator.Delegate(ctx, req)

	// MACCS 闭环：失败原因若是 turns_exhausted，重估并重试一次。
	// retry 用更激进的 turn 预算（重估 + 50% 安全 margin）。
	// 仅 turns_exhausted 才重试，其他错误（rejected / 网络断 / 业务异常）不应该重试。
	if err == nil && result != nil && result.Status == "failed" &&
		strings.Contains(result.Error, "turns_exhausted") {
		newEstimate := estimateDelegateTurnsForRetry(input.TargetKind, input.Instruction, req.Budget.MaxTurns)
		if newEstimate > req.Budget.MaxTurns {
			req.Budget.MaxTurns = newEstimate
			result2, err2 := t.Orchestrator.Delegate(ctx, req)
			if err2 == nil && result2 != nil {
				result = result2
				err = nil
			}
		}
	}

	// 失败计数:同一 (target_kind, instruction) 短哈希连续失败 N 次后,
	// retry_hint 强制告诉 LLM "STOP RETRYING SAME ARGS",避免 LoopDetector
	// 触发整个 run 死掉。
	failKey := fmt.Sprintf("%s|%x", input.TargetKind, sha256Short(input.Instruction))
	prevFails := t.peekFailCount(failKey)
	// 硬上限:同 args 已失败 3 次,直接返回 IsError=false 的"已停止"信号,
	// 避免 LLM 还在试触发 LoopDetector。这是"工具自我保护",不依赖 LLM 听话。
	if prevFails >= 3 {
		return &tool.Result{
			Output: json.RawMessage(fmt.Sprintf(
				`{"error":"delegate hard-stopped after %d identical-args failures","retry_hint":"DO NOT call delegate with these same args again. Either change target_kind / instruction substantially, or report failure to user. This tool will keep returning this stop message until you change args.","fail_count":%d,"hard_stopped":true}`,
				prevFails, prevFails)),
			IsError: true,
		}, nil
	}

	if err != nil {
		hint := "call delegate again with a DIFFERENT target_kind or DIFFERENT instruction. Do NOT retry the same args — same args = same failure."
		if prevFails >= 2 {
			hint = "STOP retrying delegate. Already failed " + fmt.Sprint(prevFails) + " times with these args. Tell the user the failure and ask how to proceed."
		}
		return &tool.Result{
			Output: json.RawMessage(fmt.Sprintf(
				`{"error":"delegation error: %s","retry_hint":%q,"fail_count":%d}`,
				escapeForJSON(err.Error()), hint, prevFails)),
			IsError: true,
		}, nil
	}

	if result.Status == "rejected" {
		hint := "the target brain refused. Try DIFFERENT target_kind or rephrase instruction. Do NOT retry same args."
		if prevFails >= 2 {
			hint = "STOP retrying. Brain refused " + fmt.Sprint(prevFails) + " times. Report to user."
		}
		return &tool.Result{
			Output: json.RawMessage(fmt.Sprintf(
				`{"error":"delegation rejected: %s","retry_hint":%q,"fail_count":%d}`,
				escapeForJSON(result.Error), hint, prevFails)),
			IsError: true,
		}, nil
	}

	if result.Status == "failed" {
		hint := "call delegate with a CLEARER instruction or DIFFERENT brain. Do NOT silently announce a workaround. Do NOT retry same args."
		if prevFails >= 2 {
			hint = "STOP retrying. Subtask failed " + fmt.Sprint(prevFails) + " times. Report to user and ask how to proceed."
		}
		return &tool.Result{
			Output: json.RawMessage(fmt.Sprintf(
				`{"error":"subtask failed: %s","retry_hint":%q,"fail_count":%d}`,
				escapeForJSON(result.Error), hint, prevFails)),
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
	_ = reg.Register(tool.WrapWithFailureLog(NewDelegateTool(orch, e)))
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

// estimateDelegateTurns 用 ComplexityEstimator 估算单次 delegate 的 turn 需求。
//
// 调 kernel.ComplexityEstimator 路径：包装一个临时 PlanSubTask，跑 Estimate，
// 拿 EstimatedTurns。无 estimator（包级单例可由上层注入，目前未注入时返回
// 25 作为保守基线 —— 比 sidecar 默认 10 多得多，单步派发不容易 turns_exhausted）。
//
// estimator 看 instruction 内容里的关键词 + brain kind 做启发式打分，比硬编码
// 合理，但不要求 LLM 自己估（LLM 不知道自己每个 turn 多大）。
func estimateDelegateTurns(targetKind, instruction string) int {
	probe := kernel.PlanSubTask{
		Name:        instruction,
		Instruction: instruction,
		Kind:        agent.Kind(targetKind),
	}
	if currentDelegateEstimator != nil {
		est := currentDelegateEstimator.Estimate(probe)
		if est.EstimatedTurns > 0 {
			return est.EstimatedTurns
		}
	}
	return 25 // 保守基线
}

// currentDelegateEstimator 是包级单例，由上层（chat init / serve init）注入。
// nil 时退化为保守 baseline。
var currentDelegateEstimator *kernel.ComplexityEstimator

// SetDelegateEstimator 由上层注入 ComplexityEstimator（chat / serve 启动时调）。
func SetDelegateEstimator(e *kernel.ComplexityEstimator) {
	currentDelegateEstimator = e
}

// estimateDelegateTurnsForRetry 在 turns_exhausted 失败后给出更激进的新估计。
// 原则：把"刚才用尽的 budget"+"estimator 现给的新估计"取大值，再加 50% safety margin，
// 上限 100（再多基本是任务设计问题，应该让中央拆得更细而不是无限加 budget）。
func estimateDelegateTurnsForRetry(targetKind, instruction string, prevBudget int) int {
	probe := kernel.PlanSubTask{
		Name:        instruction,
		Instruction: instruction,
		Kind:        agent.Kind(targetKind),
		// 把 prevBudget 作为提示传给 estimator（estimateFromLearning 可看 task.EstimatedTurns）
		EstimatedTurns: prevBudget,
	}
	candidate := prevBudget
	if currentDelegateEstimator != nil {
		est := currentDelegateEstimator.Estimate(probe)
		if est.EstimatedTurns > candidate {
			candidate = est.EstimatedTurns
		}
	}
	// 50% safety margin
	candidate = int(float64(candidate) * 1.5)
	if candidate > 100 {
		candidate = 100
	}
	if candidate <= prevBudget {
		// 至少加 20，避免 estimator 给出和 prev 一样导致再次失败
		candidate = prevBudget + 20
	}
	return candidate
}

// escapeForJSON 把字符串转义成可嵌入 JSON 字符串值的形式(去掉反斜杠/引号/换行)。
// 用于构造 IsError result 的 JSON output,避免 LLM 错误信息中的特殊字符破坏 JSON。
func escapeForJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	// json.Marshal 字符串返回带引号的形式 "xxx",去掉两端引号。
	return string(b[1 : len(b)-1])
}
