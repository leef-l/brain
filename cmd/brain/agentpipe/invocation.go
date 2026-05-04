// Package agentpipe 是 chat / run / serve 三种模式的统一 agent 执行管线。
//
// 设计动机:
//   原先 cmd/brain/chat/executor.go 和 cmd/brain/run_executor.go 各自构造 loop.Runner,
//   配置散落,Workdir 透传 / nudge / sanitize 等修复要在三处维护;HTTP serve 模式
//   通过 ExecuteManagedRun 注入复用 run_executor,但 chat 完全独立。
//
//   agentpipe 把三模式的"执行一次 agent 任务"抽成 Invocation 结构体,
//   差异(observer / event / 持久化)通过 hooks 注入。
//
//   后续 PlanOrchestrator 接入也只在此处一处接入,三模式全自动跟随。
//
// 设计原则:
//   - Invocation 是一次性的:构造 → 调 Execute → 拿 Result。不复用。
//   - Hooks 全部可选:nil 时跳过对应行为,适配从最简单的 mock 测试到生产 chat。
//   - 不持有 Runner 状态:Runner 是底层引擎,Invocation 是它的"调用配置 + UI 桥"。

package agentpipe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

// Invocation 描述一次 agent 执行(从 user prompt 到 LLM run 的完整调用)。
//
// 三模式怎么用:
//   - chat:每个 user turn 构造一次 Invocation,EventCh 推 ChatEvent
//   - run :一次 user prompt 构造一次,Audit 写 RunStore,无 EventCh
//   - serve:HTTP 触发一次 run,内部走 run 路径(共享 Audit + EventBus)
type Invocation struct {
	// --- 必填 ---
	Provider     llm.Provider
	Registry     tool.Registry
	BrainID      string
	Messages     []llm.Message // 输入消息(已含 user message)
	SystemPrompt string

	// --- Budget ---
	MaxTurns    int
	MaxDuration time.Duration
	MaxCostUSD  float64 // 0 = 默认 5.0

	// --- 标识 ---
	RunID         string // 不传则自动生成 inv-<time>
	UserUtterance string // 用于 SubtaskContext.UserUtterance
	ParentRunID   string // 用于 SubtaskContext.ParentRunID
	ProjectID     string // 用于 SubtaskContext.ProjectID(MACCS 项目级持久化)
	TurnIndex     int    // chat 模式累计 turn 编号

	// --- 执行模式 flag ---
	Stream           bool
	ChatCentralBrain bool // chat 模式 + central brain → 触发 nudge 重试
	ToolChoice       string

	// --- 可选 hooks (差异点) ---
	ToolObserver      loop.ToolObserver
	StreamConsumer    loop.StreamConsumer
	Sanitizer         loop.ToolResultSanitizer
	LoopDetector      loop.LoopDetector
	CacheBuilder      loop.CacheBuilder
	BatchPlanner      loop.ToolBatchPlanner
	MessageCompressor func(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error)
	TokenBudget       int
	InterruptChecker  loop.RunInterruptChecker
	PreTurnStateHook  func(ctx context.Context, run *loop.Run, turnIndex int) (*loop.PreTurnState, error)

	// AuditSink 是 runtime audit 的接收器(run/serve 写 RunStore)。
	// nil 时不启用 audit。
	AuditSink runtimeaudit.SinkFunc

	// SystemBlocks 允许调用方追加额外 system blocks(例如 chat DispatchHint)。
	// 优先级高于 SystemPrompt(SystemPrompt 作为第一个 block,这些追加在后)。
	SystemBlocks []llm.SystemBlock

	// MaxTokens 是单次 LLM 调用的最大输出 token 数。
	// 默认 0(不传给 provider,让其用模型自身上限,大多数主流模型 8K+)。
	// 之前默认硬编码 4096 经常截断 tool_use JSON 导致工具参数残缺,
	// 不应该由 brain 一刀切设上限,该上限由调用方/模型自身决定。
	MaxTokens int
}

// Execute 运行一次 agent 任务,返回完整 RunResult。
// 三模式都通过此接口调用 loop.Runner,任何 Runner 配置变化只需改这一处。
func (inv *Invocation) Execute(ctx context.Context) (*loop.RunResult, error) {
	if inv.Provider == nil {
		return nil, fmt.Errorf("agentpipe: Provider is required")
	}
	if inv.Registry == nil {
		return nil, fmt.Errorf("agentpipe: Registry is required")
	}
	if inv.BrainID == "" {
		return nil, fmt.Errorf("agentpipe: BrainID is required")
	}

	// 注入 SubtaskContext - workflow / delegate / project memory 链路靠它透传
	subtask := &protocol.SubtaskContext{
		UserUtterance: inv.UserUtterance,
		ParentRunID:   inv.ParentRunID,
		ProjectID:     inv.ProjectID,
		TurnIndex:     inv.TurnIndex,
	}
	ctx = kernel.WithSubtaskContext(ctx, subtask)

	// 注入 Audit Sink(run/serve 用,chat 默认不启)
	if inv.AuditSink != nil {
		ctx = runtimeaudit.WithSink(ctx, inv.AuditSink)
	}

	// 构造 Run
	runID := inv.RunID
	if runID == "" {
		runID = fmt.Sprintf("inv-%s", time.Now().UTC().Format("20060102T150405Z"))
	}
	maxCostUSD := inv.MaxCostUSD
	if maxCostUSD <= 0 {
		maxCostUSD = 5.0
	}
	maxDuration := inv.MaxDuration
	if maxDuration <= 0 {
		maxDuration = 5 * time.Minute
	}

	run := loop.NewRun(runID, inv.BrainID, loop.Budget{
		MaxTurns:     inv.MaxTurns,
		MaxCostUSD:   maxCostUSD,
		MaxLLMCalls:  inv.MaxTurns * 2,
		MaxToolCalls: inv.MaxTurns * 4,
		MaxDuration:  maxDuration,
	})

	// 构造 Runner — 单一构造点,三模式自动跟随
	runner := &loop.Runner{
		Provider:          inv.Provider,
		ToolRegistry:      inv.Registry,
		ToolObserver:      inv.ToolObserver,
		StreamConsumer:    inv.StreamConsumer,
		Sanitizer:         orDefaultSanitizer(inv.Sanitizer),
		LoopDetector:      orDefaultLoopDetector(inv.LoopDetector),
		CacheBuilder:      orDefaultCacheBuilder(inv.CacheBuilder),
		BatchPlanner:      inv.BatchPlanner,
		MessageCompressor: inv.MessageCompressor,
		TokenBudget:       inv.TokenBudget,
		InterruptChecker:  inv.InterruptChecker,
		PreTurnStateHook:  inv.PreTurnStateHook,
	}
	// Phase 6 — 装配 IntentChain + Clarifier 兜底(详细动机见 sdk/loop/wiring.go)。
	// chat 中央大脑可能跑在 deepseek/mimo 上,Native passthrough + JSONCodeBlock
	// 等 parser 能从非原生输出救回意图;Clarifier 替代单次 nudge 给针对性诊断。
	loop.AttachDefaultRecovery(runner)

	// 构造 RunOptions
	system := []llm.SystemBlock{}
	if inv.SystemPrompt != "" {
		system = append(system, llm.SystemBlock{Text: inv.SystemPrompt, Cache: true})
	}
	system = append(system, inv.SystemBlocks...)

	opts := loop.RunOptions{
		System:           system,
		Tools:            buildToolSchemas(inv.Registry),
		ToolChoice:       orDefault(inv.ToolChoice, "auto"),
		MaxTokens:        inv.MaxTokens,
		Stream:           inv.Stream,
		ChatCentralBrain: inv.ChatCentralBrain,
	}

	return runner.Execute(ctx, run, inv.Messages, opts)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orDefaultSanitizer(s loop.ToolResultSanitizer) loop.ToolResultSanitizer {
	if s == nil {
		return loop.NewMemSanitizer()
	}
	return s
}

func orDefaultLoopDetector(d loop.LoopDetector) loop.LoopDetector {
	if d == nil {
		return loop.NewMemLoopDetector()
	}
	return d
}

func orDefaultCacheBuilder(b loop.CacheBuilder) loop.CacheBuilder {
	if b == nil {
		return loop.NewMemCacheBuilder()
	}
	return b
}

// buildToolSchemas 把 registry 中所有 tool 的 schema 转成 LLM 协议格式。
// 三模式共用此构造逻辑,以前散落在各 executor 里的 buildToolSchemas 收编到这里。
func buildToolSchemas(reg tool.Registry) []llm.ToolSchema {
	if reg == nil {
		return nil
	}
	tools := reg.List()
	out := make([]llm.ToolSchema, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema()
		out = append(out, llm.ToolSchema{
			Name:        schema.Name,
			Description: schema.Description,
			InputSchema: schema.InputSchema,
		})
	}
	return out
}

// AuditEventToJSON 是 audit sink 常用的 event marshal helper。
// chat / run / serve 各自构造 SinkFunc 时若需要 JSON 序列化可复用。
func AuditEventToJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
