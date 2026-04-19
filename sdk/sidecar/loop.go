package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/toolpolicy"
)

// ExecuteRequest is the payload of a brain/execute RPC call.
type ExecuteRequest struct {
	TaskID      string                         `json:"task_id"`
	Instruction string                         `json:"instruction"`
	Context     json.RawMessage                `json:"context,omitempty"`
	Budget      *ExecuteBudget                 `json:"budget,omitempty"`
	Execution   *executionpolicy.ExecutionSpec `json:"execution,omitempty"`
}

// ExecuteBudget constrains the sidecar Agent Loop.
type ExecuteBudget struct {
	MaxTurns     int     `json:"max_turns,omitempty"`
	MaxCostUSD   float64 `json:"max_cost_usd,omitempty"`
	MaxLLMCalls  int     `json:"max_llm_calls,omitempty"`
	MaxToolCalls int     `json:"max_tool_calls,omitempty"`
	MaxDurationS int     `json:"max_duration_seconds,omitempty"`
}

// ExecuteResult is the response returned after brain/execute completes.
type ExecuteResult struct {
	Status  string `json:"status"` // "completed", "failed", "canceled"
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	Turns   int    `json:"turns"`
}

// --- LLM request/response types for reverse RPC ---

// llmRequest is the payload sent to the Kernel via llm.complete.
type llmRequest struct {
	System    []systemBlock `json:"system,omitempty"`
	Messages  []message     `json:"messages"`
	Tools     []toolSchema  `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type systemBlock struct {
	Text  string `json:"text"`
	Cache bool   `json:"cache,omitempty"`
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type toolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// llmResponse is the payload received from the Kernel via llm.complete.
type llmResponse struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Usage      *llmUsageWire  `json:"usage,omitempty"`
}

// llmUsageWire 是反向 RPC 里可选的 Usage 字段（由 Kernel 侧 llm.complete 填充）。
type llmUsageWire struct {
	InputTokens         int     `json:"input_tokens,omitempty"`
	OutputTokens        int     `json:"output_tokens,omitempty"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// RunAgentLoop 是 sidecar 的 Agent Loop 入口。
//
// 历史实现是手搓的 for-loop，绕开了 sdk/loop.Runner（含 LoopDetector / Budget /
// Sanitizer / CacheBuilder / MessageCompressor 等）。v1.1 起改为适配层：
// 用 sdk/loop.Runner 驱动，把工具执行、死循环检测、成本预算、prompt cache
// 全部统一到主流程实现上。保持公开签名不变，向后兼容四个基础大脑。
//
// 调用方可选地通过 RunAgentLoopWithContext 传入 ExecuteRequest.Context，
// 让中央 ContextEngine 装配的上下文注入到对话起始。
func RunAgentLoop(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, maxTurns int) *ExecuteResult {
	return RunAgentLoopWithContext(ctx, caller, registry, systemPrompt, instruction, maxTurns, nil)
}

// RunAgentLoopWithContext 是 RunAgentLoop 的扩展版本，额外接收 ExecuteRequest.Context
// 做"对话起始上下文注入"。当 extraContext 非空且为合法 JSON 时，会作为一条
// 前置 user message 插入在 instruction 之前。
func RunAgentLoopWithContext(ctx context.Context, caller KernelCaller, registry tool.Registry,
	systemPrompt string, instruction string, maxTurns int, extraContext json.RawMessage) *ExecuteResult {

	if maxTurns <= 0 {
		maxTurns = 10
	}

	provider := NewKernelLLMProvider(caller, "kernel")

	runner := &loop.Runner{
		Provider:     provider,
		ToolRegistry: registry,
		Sanitizer:    loop.NewMemSanitizer(),
		LoopDetector: loop.NewMemLoopDetector(),
		CacheBuilder: loop.NewMemCacheBuilder(),
		ToolObserver: stderrToolObserver{},
	}

	// P3.5: browser brain 每 turn 前按 SequenceRecorder 信号自动切 BrowserStage,
	// 重写本轮曝露给 LLM 的工具 schema 列表。其他 brain 不挂 hook,开销为零。
	brainKindEarly := brainKindFromSystem(systemPrompt)
	if brainKindEarly == "browser" {
		runner.PreTurnHook = newBrowserStageHook(registry)
	}

	budget := loop.Budget{
		MaxTurns:     maxTurns,
		MaxCostUSD:   5.0,
		MaxLLMCalls:  maxTurns * 2,
		MaxToolCalls: maxTurns * 4,
		MaxDuration:  10 * time.Minute,
	}
	runID := fmt.Sprintf("sidecar-%d", time.Now().UnixNano())
	run := loop.NewRun(runID, "sidecar", budget)

	// Task #13: 把本次 run 绑到进程内的 InteractionRecorder。browser 工具装饰器
	// 在每次 Execute 后会追加一条 RecordedAction;loop 结束时通过已注入的
	// sink(通常是 LearningEngine)把序列持久化到 LearningStore,供
	// ui_pattern_learn 聚类。没注入 sink 时该机制整体 no-op。
	brainKind := brainKindEarly
	tool.BindRecorder(ctx, runID, brainKind, instruction)

	// L1+L2: 把 systemPrompt 分两层。仅一层时简化为单块 SystemBlock 且 cache=true，
	// 让 Prompt Cache 在每一轮自动复用。
	system := []llm.SystemBlock{{Text: systemPrompt, Cache: true}}

	// 构造 tool schemas（传给 LLM 看）。
	var tools []llm.ToolSchema
	for _, t := range registry.List() {
		s := t.Schema()
		tools = append(tools, llm.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}

	// 初始 messages：若有 extraContext（中央装配的上下文），插到 instruction 之前。
	var messages []llm.Message
	if len(extraContext) > 0 && json.Valid(extraContext) {
		contextText := summarizeContext(extraContext)
		if contextText != "" {
			messages = append(messages, llm.Message{
				Role: "user",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "Context provided by coordinator:\n" + contextText},
				},
			})
			messages = append(messages, llm.Message{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "Acknowledged. I will use this context when executing the task."},
				},
			})
		}
	}
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: instruction}},
	})

	opts := loop.RunOptions{
		System:     system,
		Tools:      tools,
		ToolChoice: "auto",
		MaxTokens:  4096,
	}

	result, err := runner.Execute(ctx, run, messages, opts)
	if err != nil {
		_ = tool.FinishRecorder(ctx, "failure")
		return &ExecuteResult{
			Status: "failed",
			Error:  fmt.Sprintf("runner.Execute: %v", err),
			Turns:  0,
		}
	}

	// 从 FinalMessages 提取最后一条 assistant 文本作为 summary。
	summary := extractLastAssistantText(result.FinalMessages)

	status := "completed"
	outcome := "success"
	switch result.Run.State {
	case loop.StateFailed:
		status = "failed"
		outcome = "failure"
	case loop.StateCanceled:
		status = "canceled"
		outcome = "failure"
	}
	_ = tool.FinishRecorder(ctx, outcome)

	out := &ExecuteResult{
		Status:  status,
		Summary: summary,
		Turns:   result.Run.Budget.UsedTurns,
	}
	// 如果最后一个 Turn 带错误，回填到 Error 字段。
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Error != nil {
		out.Error = result.Turns[n-1].Error.Error()
	}
	return out
}

// brainKindFromSystem 尝试从 systemPrompt 里识别本次 run 属于哪个基础大脑。
// 返回 "browser"/"code"/"data"/"verifier"/"fault"/"central",否则 ""。
// 用于 InteractionSequence.BrainKind 字段。
func brainKindFromSystem(systemPrompt string) string {
	lp := strings.ToLower(systemPrompt)
	switch {
	case strings.Contains(lp, "browser brain"), strings.Contains(lp, "browser agent"),
		strings.Contains(lp, "browser.snapshot"), strings.Contains(lp, "browser.open"):
		return "browser"
	case strings.Contains(lp, "code brain"), strings.Contains(lp, "code agent"):
		return "code"
	case strings.Contains(lp, "data brain"):
		return "data"
	case strings.Contains(lp, "verifier brain"):
		return "verifier"
	case strings.Contains(lp, "fault brain"):
		return "fault"
	case strings.Contains(lp, "central brain"):
		return "central"
	}
	return ""
}

// summarizeContext 把 ExecuteRequest.Context 的 JSON 简化为纯文本。
// 支持几种常见形态：
//   - string（直接返回）
//   - {"text": "..."} / {"summary": "..."}（提取该字段）
//   - 其它 → JSON 文本化
func summarizeContext(raw json.RawMessage) string {
	// 尝试直接解析为字符串
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	// 尝试解析为 map，找 "text" 或 "summary" 字段
	var asMap map[string]interface{}
	if err := json.Unmarshal(raw, &asMap); err == nil {
		if s, ok := asMap["text"].(string); ok && s != "" {
			return s
		}
		if s, ok := asMap["summary"].(string); ok && s != "" {
			return s
		}
	}
	// 退化：直接用 JSON 文本
	return string(raw)
}

func extractLastAssistantText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		out := ""
		for _, b := range messages[i].Content {
			if b.Type == "text" && b.Text != "" {
				if out != "" {
					out += "\n"
				}
				out += b.Text
			}
		}
		return out
	}
	return ""
}

// ---------------------------------------------------------------------------
// stderrToolObserver：把工具执行生命周期打到 stderr，和旧版行为一致。
// ---------------------------------------------------------------------------

type stderrToolObserver struct{}

func (stderrToolObserver) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, _ json.RawMessage) {
	fmt.Fprintf(os.Stderr, "  [sidecar] executing %s\n", toolName)
}

func (stderrToolObserver) OnToolEnd(ctx context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, _ json.RawMessage) {
	if !ok {
		fmt.Fprintf(os.Stderr, "  [sidecar] tool %s FAILED\n", toolName)
	}
	// P3.5:把工具级别的结果也喂给 SequenceRecorder 的 turn 窗口。
	// pattern_exec 已在自身 Execute 里写过一次;这里补齐其他工具的失败
	// 信号,让 toolpolicy.DecideBrowserStage 能看到"连续 N turn 无进展"。
	// 只记 error——成功工具太多,写进去会把错误窗口冲掉。
	if !ok && toolName != "browser.pattern_exec" {
		tool.RecordTurnOutcome(ctx, "error")
	}
}

// ---------------------------------------------------------------------------
// 旧版辅助（供 kernel_provider.go 复用）
// ---------------------------------------------------------------------------

// marshalTextContent builds a safe JSON content array with a single text block.
func marshalTextContent(text string) json.RawMessage {
	escaped, _ := json.Marshal(text)
	return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, escaped))
}

// marshalToolOutput wraps a tool's raw output in a content array.
func marshalToolOutput(output json.RawMessage) json.RawMessage {
	if json.Valid(output) {
		return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%s}]`, output))
	}
	return marshalTextContent(string(output))
}

// newBrowserStageHook 返回一个 loop.PreTurnHook,它按 P3.5 规则在每 turn 前
// 自动决定 BrowserStage,并据此过滤本轮曝露给 LLM 的工具 schema 列表。
//
// 触发输入来自 SequenceRecorder 的三条信号:
//   - 最近 pattern_match top score(browser.pattern_match 写入)
//   - 最近 turn outcome 窗口("error" 由 stderrToolObserver 在非 pattern_exec
//     工具失败时写入;pattern_exec 自身在执行器里写)
//   - PendingApprovalClass(保留口子,当前 hook 不预判下一步动作 class,
//     所以恒为 "")
//
// 决策返回空字符串("保持上一轮")时,hook 沿用前一轮缓存的 stage 继续过滤,
// 避免抖动。第一轮没有上一轮——按 DecideBrowserStage 的规则 4:无 pattern_match
// 数据 → new_page。
//
// 全量 registry 不变;仅重写返回给 Runner 的 tools schema。这样即便 hook
// 过滤错了,dispatchTools 仍能按名字找到工具执行。
func newBrowserStageHook(registry tool.Registry) func(ctx context.Context, run *loop.Run, turnIndex int) ([]llm.ToolSchema, error) {
	// profiles 预构好一份 Config,避免每 turn 重建。
	cfg := &toolpolicy.Config{}
	toolpolicy.MergeBrowserStageProfiles(cfg)

	// scope 是 run.browser.<stage>,对应 DefaultBrowserStageProfiles 的 active_tools
	// 绑定(即 run.browser.new_page → browser_new_page profile)。
	lastStage := ""

	return func(ctx context.Context, _ *loop.Run, _ int) ([]llm.ToolSchema, error) {
		in := toolpolicy.DecisionInput{
			RecentPatternScores: tool.RecentPatternScores(ctx),
			RecentTurnOutcomes:  tool.RecentTurnOutcomes(ctx),
			// PendingApprovalClass 目前 hook 无法可靠预判下一步 action
			// 的审批级别(那是 LLM 在本轮决定的)。保留字段以便未来
			// 接入 AdaptiveToolPolicy 的 RecordOutcome 链路时扩展。
		}
		stage := toolpolicy.DecideBrowserStage(in, toolpolicy.DecisionThresholds{})
		if stage == "" {
			stage = lastStage
		}
		if stage == "" {
			// 没有上一轮也没有任何信号 → 沿用 opts.Tools。
			return nil, nil
		}
		lastStage = stage

		scopes := toolpolicy.ToolScopesForBrowserStage(stage)
		if len(scopes) == 0 {
			return nil, nil
		}
		filtered := toolpolicy.FilterRegistry(registry, cfg, scopes...)
		if filtered == nil {
			return nil, nil
		}

		tools := make([]llm.ToolSchema, 0, len(filtered.List()))
		for _, t := range filtered.List() {
			s := t.Schema()
			tools = append(tools, llm.ToolSchema{
				Name:        s.Name,
				Description: s.Description,
				InputSchema: s.InputSchema,
			})
		}
		return tools, nil
	}
}
