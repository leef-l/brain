package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

// Runner is the Agent Loop execution engine that drives a Run through a
// sequence of Turns. Each Turn assembles a ChatRequest, calls the LLM
// Provider, dispatches any tool_use blocks, sanitizes results, updates the
// Budget, checks for stuck-loop patterns, and decides the next State.
//
// All optional fields are nil-safe: when nil the corresponding stage is
// skipped. See 22-Agent-Loop规格.md §2 (Architecture).
type Runner struct {
	// Provider is the LLM provider to call. Required.
	Provider llm.Provider

	// ToolRegistry is the tool catalog for resolving tool_use blocks. Required.
	ToolRegistry tool.Registry

	// Sanitizer sanitizes tool results before feeding them back to the LLM.
	// When nil, tool results are passed through with a minimal text wrapper.
	Sanitizer ToolResultSanitizer

	// StreamConsumer receives streaming callbacks when Stream is enabled.
	// When nil, streaming events are consumed silently.
	StreamConsumer StreamConsumer

	// ToolObserver receives tool execution lifecycle callbacks while the
	// Runner dispatches tool_use blocks. When nil, tool events are ignored.
	ToolObserver ToolObserver

	// LoopDetector observes per-Turn events and detects stuck-loop patterns.
	// When nil, loop detection is skipped.
	LoopDetector LoopDetector

	// BatchPlanner 将 tool_calls 按资源冲突分组为可并行的 batch。
	// 当非 nil 时，dispatchTools 会先 Plan() 分组再按 batch 并行执行；
	// 为 nil 时保持原有串行逻辑（向后兼容）。
	BatchPlanner ToolBatchPlanner

	// CacheBuilder assembles the three-layer Prompt Cache control markers.
	// When nil, no CachePoints are added to ChatRequests.
	CacheBuilder CacheBuilder

	// MessageCompressor 在构建 ChatRequest 前压缩消息列表。
	// 当非 nil 且消息 token 数超过 TokenBudget 时被调用。
	// 典型实现委托给 kernel.ContextEngine.Compress()。
	// 通过回调注入避免 loop → kernel 循环依赖。
	MessageCompressor func(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error)

	// TokenBudget 是消息列表的 token 预算上限。
	// 当 > 0 且 MessageCompressor 非 nil 时，超预算的消息会被自动压缩。
	TokenBudget int

	// PreTurnHook 在每 turn 开始构建 ChatRequest 前被调用,允许集成方
	// 动态重写本轮曝露给 LLM 的工具 schema 列表。典型用途:P3.5 BrowserStage
	// 自动切换——每 turn 依据 recorder 信号决定本轮工具 allow-list。
	//
	// 返回 nil 切片表示"沿用 opts.Tools"。返回空切片表示"本轮不开放任何工具"。
	// 仅重写 schema,不改 dispatch registry。旧调用方继续使用它即可。
	// hook 返回非 nil error 时视为失败并终止 run(与其他硬失败一致)。
	PreTurnHook func(ctx context.Context, run *Run, turnIndex int) ([]llm.ToolSchema, error)

	// PreTurnStateHook 是 PreTurnHook 的扩展版本:除 schema 外,还可为本轮
	// 替换实际 dispatch 用的 ToolRegistry。适用于动态 Registry/Runtime 重建:
	// LLM 这一轮看到的工具集合,必须和 Runner 真正允许执行的集合一致。
	//
	// 返回 nil 表示沿用 opts.Tools + Runner.ToolRegistry。
	PreTurnStateHook func(ctx context.Context, run *Run, turnIndex int) (*PreTurnState, error)

	// Now returns the current time. Defaults to time.Now().UTC when nil.
	Now func() time.Time

	// CheckpointStore 用于任务级断点续传。当非 nil 时，Runner 在每个
	// Turn 完成后自动保存 checkpoint，并在 Run 进入 StatePaused/StateCrashed
	// 后恢复时从 checkpoint 重建状态。See checkpoint.go.
	CheckpointStore CheckpointStore

	// InterruptChecker 检查 run 是否收到中断信号。
	// 当非 nil 时，每 turn 开始前检查，收到中断则暂停/停止/重启。
	InterruptChecker RunInterruptChecker
}

// PreTurnState 描述某一轮的动态工具视图。
type PreTurnState struct {
	// Tools 是本轮暴露给 LLM 的 schema 列表。nil 表示沿用 opts.Tools。
	Tools []llm.ToolSchema
	// Registry 是本轮工具调度使用的 registry。nil 表示沿用 Runner.ToolRegistry。
	Registry tool.Registry
	// ToolChoice 覆盖本轮 tool_choice。空字符串表示沿用 opts.ToolChoice。
	ToolChoice string
}

// RunOptions configures a single Run execution.
type RunOptions struct {
	// System is the L1+L2 system prompt blocks.
	System []llm.SystemBlock

	// Tools is the tool schemas exposed to the LLM.
	Tools []llm.ToolSchema

	// ToolChoice controls tool selection: "auto" (default), "required",
	// "none", or a specific tool name.
	ToolChoice string

	// Model overrides the default model for this Run.
	Model string

	// MaxTokens 是单次 LLM 调用的最大输出 token 数。
	// 默认 0(不传给 provider,让其用模型自身上限,大多数主流模型 8K+)。
	// 之前默认硬编码 4096 经常截断 tool_use JSON 导致工具参数残缺,
	// 不应该由 brain 一刀切设上限,该上限由调用方/模型自身决定。
	MaxTokens int

	// Stream enables the streaming path (Provider.Stream) instead of
	// Provider.Complete.
	Stream bool

	// TaskBoundary is the message index where task context ends and
	// rolling history begins, used by CacheBuilder.BuildL2Task.
	TaskBoundary int

	// ChatCentralBrain 标记本 Run 是 chat 模式下的 central 大脑。
	// 启用后,nudge 触发条件更激进 — 任何"无 tool_use blocks 但有文本"的 turn
	// 都被视为 announce-without-act,自动注入 reminder 让 LLM 必须调工具。
	// 不靠关键词 — central 编排大脑本来就该调工具,纯文本响应是无意义的。
	// 专精 brain / run / serve 模式不启用,允许它们纯文本回复。
	ChatCentralBrain bool

	// SpecialistSubAgent 标记本 Run 是被 delegate 召唤的专精 sub agent
	// (code/browser/data 等被 central.delegate 调用执行单一任务的场景)。
	// 启用后:第一轮就强制要求 tool_use(0 工具 + 有文本 = 立即 nudge),
	// 因为专精 sub agent 是来"干活"的,不应该返回纯文本计划。
	// 实测发现:code brain 被 delegate 后第一轮常输出 400+ 字纯文字
	// (planning/分析)然后 stop_reason=end_turn 退出,实际什么都没写。
	// 行为类似 ChatCentralBrain,但语义不同 — 这个标志专门给 sub agent 用。
	SpecialistSubAgent bool
}

// RunResult is the final output of Runner.Execute.
type RunResult struct {
	// Run is the final Run state (terminal: completed/failed/canceled).
	Run *Run

	// Turns is the ordered list of TurnResults produced during execution.
	Turns []*TurnResult

	// FinalMessages is the full conversation history including all
	// assistant and tool_result messages.
	FinalMessages []llm.Message
}

// now returns the current time using the configured clock or the default.
func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// Execute drives a Run through its complete lifecycle: pending → running →
// (tool loops) → completed/failed/canceled. It returns a RunResult with the
// full conversation history and all TurnResults.
//
// The Run must be in StatePending. initialMessages is the starting
// conversation (typically a single user message).
//
// See 22-Agent-Loop规格.md §4 and §6.
func (r *Runner) Execute(ctx context.Context, run *Run, initialMessages []llm.Message, opts RunOptions) (result *RunResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("runner panic: %v", rec)
			if result == nil {
				result = &RunResult{Run: run}
			}
		}
	}()
	// 跨 turn 共享 LoopDetector(chat 模式)在 Run 结束时必须释放本 run 的 state,
	// 否则 detector.state map 用 runID 做 key 单调增长 → 长 chat session 内存泄漏。
	defer func() {
		if r.LoopDetector != nil && run != nil {
			r.LoopDetector.Forget(run.ID)
		}
	}()
	now := r.now()

	// Transition pending → running.
	if err := run.Start(now); err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	// Apply defaults.
	if opts.ToolChoice == "" {
		opts.ToolChoice = "auto"
	}
	// 不再给 MaxTokens 设默认值。0 表示不传 provider,让模型用自身上限。
	// 历史:之前默认 4096 经常截断 tool_use JSON,造成 LLM 工具调用失败。

	messages := make([]llm.Message, len(initialMessages))
	copy(messages, initialMessages)

	var turns []*TurnResult

	// ─── Checkpoint Resume ────────────────────────────────────────────────
	// 如果 Run 状态为 paused/crashed 且有 CheckpointStore，尝试从 checkpoint 恢复。
	if r.CheckpointStore != nil && (run.State == StatePaused || run.State == StateCrashed) {
		if restoredMessages, restoredTurns, ok := RestoreFromCheckpoint(r.CheckpointStore, run); ok {
			messages = restoredMessages
			turns = restoredTurns
		}
	}

	// nudgedAnnouncement:本 Run 是否已注入过 announce-without-act 兜底 reminder。
	// 单次触发 — nudge 只是安全网,LLM 应该自己学会"说就调"而不是依赖系统反复提醒。
	// 多次 nudge 实测让坏循环更长(每次 nudge → LLM 又输出长文本 → 浪费 30-60s),
	// 不如快速失败让上层 sub agent 重新 delegate。
	var nudgedAnnouncement bool

	for {
		now = r.now()

		// Update elapsed time.
		run.Budget.ElapsedTime = now.Sub(run.StartedAt)

		// Budget check — must happen before every Turn.
		if err := run.Budget.CheckTurn(); err != nil {
			run.Fail(r.now())
			be := toBrainError(err)
			turns = append(turns, &TurnResult{
				Turn:      &Turn{RunID: run.ID, Index: run.CurrentTurn + 1},
				NextState: StateFailed,
				Error:     be,
			})
			break
		}

		// Interrupt check — before creating a new Turn.
		if r.InterruptChecker != nil {
			if sig := r.InterruptChecker.CheckInterrupt(ctx, run.ID); sig != nil {
				switch sig.Action {
				case "stop":
					run.Fail(r.now())
					turns = append(turns, &TurnResult{
						Turn:      &Turn{RunID: run.ID, Index: run.CurrentTurn + 1},
						NextState: StateFailed,
						Error:     toBrainError(fmt.Errorf("interrupted: %s (%s)", sig.Reason, sig.Type)),
					})
					goto done
				case "pause":
					run.State = StatePaused
					turns = append(turns, &TurnResult{
						Turn:      &Turn{RunID: run.ID, Index: run.CurrentTurn + 1},
						NextState: StatePaused,
					})
					CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
					goto done
				default:
					// "restart" 或其他 — 记录后继续执行当前 turn
				}
			}
		}

		// Context cancellation check.
		if err := ctx.Err(); err != nil {
			run.Cancel(r.now())
			break
		}

		// Create a new Turn.
		run.CurrentTurn++
		turn := NewTurn(run.ID, run.CurrentTurn, now)

		// PreTurnHook/PreTurnStateHook: 让集成方按 turn 重写本轮 tools schema
		// 和可选的 dispatch registry。
		turnOpts := opts
		turnRegistry := r.ToolRegistry
		if r.PreTurnStateHook != nil {
			state, hookErr := r.PreTurnStateHook(ctx, run, run.CurrentTurn)
			if hookErr != nil {
				turn.End(r.now())
				be := toBrainError(hookErr)
				run.Fail(r.now())
				turns = append(turns, &TurnResult{
					Turn:      turn,
					NextState: StateFailed,
					Error:     be,
				})
				break
			}
			if state != nil {
				if state.Tools != nil {
					turnOpts.Tools = state.Tools
				}
				if state.Registry != nil {
					turnRegistry = state.Registry
				}
				if state.ToolChoice != "" {
					turnOpts.ToolChoice = state.ToolChoice
				}
			}
		}
		if r.PreTurnHook != nil && r.PreTurnStateHook == nil {
			newTools, hookErr := r.PreTurnHook(ctx, run, run.CurrentTurn)
			if hookErr != nil {
				turn.End(r.now())
				be := toBrainError(hookErr)
				run.Fail(r.now())
				turns = append(turns, &TurnResult{
					Turn:      turn,
					NextState: StateFailed,
					Error:     be,
				})
				break
			}
			if newTools != nil {
				turnOpts.Tools = newTools
			}
		}

		// Build the ChatRequest.
		req := r.buildChatRequest(run, messages, turnOpts)

		// Call LLM with transparent retry on transient errors (network/stream stalled).
		// 重试策略:同 turn 内最多 3 次。messages 完全相同(LLM 接 partial 没意义,
		// 直接重新跑全 turn)。3 次后真失败才把整个 run 标 failed。
		// 不重试的错误:context cancel、ValidateToolUseResponse 失败(LLM 输出格式错)。
		//
		// Backoff 短:0ms / 500ms / 2s。常见失败模式是 sidecar 重启 + 反向 RPC
		// 短暂 EOF,通常 100ms 内就恢复,长 backoff 浪费时间(用户实测 21min 慢的根因
		// 之一就是 1+2=3s 退避 × 多次 nudge × 多次 sidecar 重启)。
		var resp *llm.ChatResponse
		var llmErr error
		const maxLLMRetries = 3
		retryBackoffs := [...]time.Duration{0, 500 * time.Millisecond, 2 * time.Second}
		for attempt := 1; attempt <= maxLLMRetries; attempt++ {
			if opts.Stream {
				resp, llmErr = r.consumeStream(ctx, run, turn, req)
			} else {
				resp, llmErr = r.Provider.Complete(ctx, req)
			}
			if llmErr == nil {
				break
			}
			// ctx canceled 不重试
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(llmErr, context.Canceled) {
				break
			}
			if attempt < maxLLMRetries {
				backoff := retryBackoffs[attempt-1]
				fmt.Fprintf(os.Stderr, "[runner] LLM call attempt %d/%d failed: %v — retry in %v\n",
					attempt, maxLLMRetries, llmErr, backoff)
				if backoff > 0 {
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						llmErr = ctx.Err()
					}
					if errors.Is(ctx.Err(), context.Canceled) {
						break
					}
				}
			}
		}
		turn.LLMCalls++
		run.Budget.UsedLLMCalls++

		if llmErr != nil {
			turn.End(r.now())
			be := toBrainError(llmErr)
			nextState := StateFailed
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(llmErr, context.Canceled) {
				nextState = StateCanceled
				run.Cancel(r.now())
			} else {
				run.Fail(r.now())
			}
			turns = append(turns, &TurnResult{
				Turn:      turn,
				NextState: nextState,
				Error:     be,
			})
			break
		}

		if err := llm.ValidateToolUseResponse(r.Provider.Name(), resp); err != nil {
			turn.End(r.now())
			be := toBrainError(err)
			run.Fail(r.now())
			turns = append(turns, &TurnResult{
				Turn:      turn,
				Response:  resp,
				NextState: StateFailed,
				Error:     be,
			})
			break
		}

		// Update budget from usage.
		run.Budget.UsedCostUSD += resp.Usage.CostUSD
		run.Budget.UsedTurns++

		// Mid-turn cost check.
		if err := run.Budget.CheckCost(); err != nil {
			turn.End(r.now())
			be := toBrainError(err)
			turns = append(turns, &TurnResult{
				Turn:      turn,
				Response:  resp,
				NextState: StateFailed,
				Error:     be,
			})
			run.Fail(r.now())
			break
		}

		// Append assistant message to history.
		messages = append(messages, assistantMessage(resp))

		// Extract tool_use blocks.
		toolUseBlocks := extractToolUseBlocks(resp.Content)

		// 调试日志:让用户能直接看到每轮 LLM 输出的 stop_reason 和 tool_use 数,
		// 用于定位"嘴上承诺但工具调用没发出"类问题。
		// 双开关:
		//   1) DebugRunner 全局变量(cmd/brain 装配层从 config.diagnostics.debug.runner 读后设)
		//   2) 环境变量 BRAIN_RUNNER_DEBUG=1(应急/单次排查)
		// 任一开启即输出。
		if DebugRunner || os.Getenv("BRAIN_RUNNER_DEBUG") == "1" {
			toolNames := make([]string, 0, len(toolUseBlocks))
			for _, b := range toolUseBlocks {
				toolNames = append(toolNames, b.ToolName)
			}
			fmt.Fprintf(os.Stderr, "[runner-debug] turn=%d stop_reason=%q tool_use_count=%d tools=%v content_blocks=%d text_chars=%d\n",
				turn.Index, resp.StopReason, len(toolUseBlocks), toolNames, len(resp.Content), countTextChars(resp.Content))
			// 0 工具时打印每个 block 的类型 + 文本片段,排查 announce-without-act
			if len(toolUseBlocks) == 0 {
				for i, b := range resp.Content {
					preview := b.Text
					if len(preview) > 200 {
						preview = preview[:200] + "..."
					}
					fmt.Fprintf(os.Stderr, "[runner-debug] turn=%d block[%d] type=%q text=%q tool_use_id=%q tool_name=%q\n",
						turn.Index, i, b.Type, preview, b.ToolUseID, b.ToolName)
				}
			}
		}

		// 退出条件:LLM 没有 tool_use blocks → run 完成。
		//
		// 历史 bug:之前条件是 `len==0 || StopReason != "tool_use"`,
		// 把 "有 tool_use 但 stop_reason=length(被截断)" 也当成完成,
		// 导致 mimo / qwen 等模型输出超 max_tokens 截断时,LLM 实际
		// 调了工具但 runner 直接退出不 dispatch。修复:只看 tool_use
		// 是否存在,不看 stop_reason。stop_reason 只用于:
		//   - "max_tokens": 提示截断,但有 tool 就 dispatch
		//   - "end_turn"/"stop": 配合 len==0 时退出
		if len(toolUseBlocks) == 0 {
			// 兜底检测:LLM 无 tool_use blocks 时是否触发 reminder 重试。
			//
			// 两个触发路径:
			// 1. opts.ChatCentralBrain = true → 任何 0 工具的有文本响应都触发,不看关键词。
			//    chat 中央大脑就该调工具,纯文本响应没意义,直接 nudge LLM 重试。
			//    这是根治 mimo/deepseek "announce-without-act" 的协议层兜底
			//    (mimo/deepseek 不可靠地支持 tool_choice=required,只能在 runner 层重试)。
			// 2. 关键词命中 (shouldNudgeAnnouncement) → 兼容非 chat 场景下的明显故障短语。
			//
			// nudge 仅触发 1 次,避免对正常聊天回复反复打扰。
			shouldTriggerNudge := false
			if opts.ChatCentralBrain && hasTextContent(resp.Content) {
				shouldTriggerNudge = true
			} else if opts.SpecialistSubAgent && hasAnyMeaningfulContent(resp.Content) {
				// Sub agent 应该立即调工具,纯文本/纯 thinking 响应都没意义。
				// hasAnyMeaningfulContent 包含 text + thinking 块 — LLM 在
				// "思考但没决定调工具"时也算 announce-without-act,nudge 重试。
				shouldTriggerNudge = true
			} else if shouldNudgeAnnouncement(resp.Content) {
				shouldTriggerNudge = true
			}
			if !nudgedAnnouncement && shouldTriggerNudge && run.Budget.UsedTurns < run.Budget.MaxTurns {
				nudgedAnnouncement = true
				// 按角色给不同 nudge:central 提示 delegate/submit_workflow,
				// sub agent 提示 write_file/edit_file/shell_exec
				messages = append(messages, announcementNudgeMessageFor(opts.ChatCentralBrain))
				if DebugRunner || os.Getenv("BRAIN_RUNNER_DEBUG") == "1" {
					fmt.Fprintf(os.Stderr, "[runner-debug] turn=%d nudge: detected announcement-without-action, injected reminder\n", turn.Index)
				}
				turn.End(r.now())
				turns = append(turns, &TurnResult{
					Turn:      turn,
					Response:  resp,
					NextState: StateRunning,
				})
				CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
				continue
			}
			turn.End(r.now())
			turns = append(turns, &TurnResult{
				Turn:      turn,
				Response:  resp,
				NextState: StateCompleted,
			})
			CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
			run.Complete(r.now())
			break
		}
		// 有 tool_use 但 stop_reason=length(输出被截断):打日志提示,
		// 但仍然 dispatch tool,让 LLM 下一轮基于 tool 结果继续。
		if resp.StopReason == "max_tokens" {
			fmt.Fprintf(os.Stderr, "runner: stop_reason=max_tokens with %d tool_use blocks (output may be truncated, dispatching anyway)\n", len(toolUseBlocks))
		}

		// Tool dispatch phase.
		run.State = StateWaitingTool
		toolResultBlocks, toolCallCount := r.dispatchTools(ctx, run, turn, turnRegistry, toolUseBlocks)
		turn.ToolCalls += toolCallCount
		run.Budget.UsedToolCalls += toolCallCount

		// Append tool results as a user message.
		messages = append(messages, toolResultMessage(toolResultBlocks))

		// Check if any dispatched tool was task_complete → terminate run.
		for _, tb := range toolUseBlocks {
			if strings.HasSuffix(tb.ToolName, ".task_complete") || tb.ToolName == "task_complete" {
				turn.End(r.now())
				turns = append(turns, &TurnResult{
					Turn:      turn,
					Response:  resp,
					NextState: StateCompleted,
				})
				CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
				run.Complete(r.now())
				goto done
			}
		}

		// Restore running state.
		run.State = StateRunning

		// Loop detection.
		if r.LoopDetector != nil {
			for _, tb := range toolUseBlocks {
				hash := contentHash(tb.ToolName, tb.Input)
				verdict, detectErr := r.LoopDetector.Observe(ctx, run, LoopEvent{
					Type:        "tool_call",
					ToolName:    tb.ToolName,
					ContentHash: hash,
					TurnIndex:   turn.Index, // 同 turn 内多 block 不计入循环计数
				})
				if detectErr != nil {
					// Detection error is non-fatal — log and continue.
					continue
				}
				if verdict.IsLoop {
					turn.End(r.now())
					be := brainerrors.New(brainerrors.CodeAgentLoopDetected,
						brainerrors.WithMessage(fmt.Sprintf(
							"agent loop detected: pattern=%s confidence=%.2f",
							verdict.Pattern, verdict.Confidence,
						)),
					)
					turns = append(turns, &TurnResult{
						Turn:      turn,
						Response:  resp,
						NextState: StateFailed,
						Error:     be,
					})
					CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
					run.Fail(r.now())
					goto done
				}
			}
		}

		turn.End(r.now())
		turns = append(turns, &TurnResult{
			Turn:      turn,
			Response:  resp,
			NextState: StateRunning,
		})
		CheckpointAfterTurn(r.CheckpointStore, run, messages, turns)
	}

done:
	// Run 成功完成后清理 checkpoint，节省磁盘空间。
	if r.CheckpointStore != nil && run.State == StateCompleted {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if delErr := r.CheckpointStore.Delete(ctx, run.ID); delErr != nil {
			fmt.Fprintf(os.Stderr, "checkpoint: delete failed for run %s: %v\n", run.ID, delErr)
		}
	}

	return &RunResult{
		Run:           run,
		Turns:         turns,
		FinalMessages: messages,
	}, nil
}

// buildChatRequest constructs a ChatRequest from the current Run state,
// message history, and RunOptions.
func (r *Runner) buildChatRequest(run *Run, messages []llm.Message, opts RunOptions) *llm.ChatRequest {
	// 消息压缩：当超过 token 预算时自动 Compress
	finalMessages := messages
	if r.MessageCompressor != nil && r.TokenBudget > 0 {
		compressed, err := r.MessageCompressor(context.Background(), messages, r.TokenBudget)
		if err == nil && len(compressed) > 0 {
			finalMessages = compressed
		}
	}

	// Provider Capability 感知的 tool_choice 升级:
	//
	// SpecialistSubAgent 第 1 turn:如果 provider 支持 Required 级别的
	// tool_choice,强制升级到 "required",让 LLM 在第一轮必须输出 tool_use。
	// 这是根治"announce-without-act"在 native-tool-call 模型上的根因 —
	// Anthropic / GPT-4 此时一定会调工具,跳过原来的 nudge 兜底循环。
	//
	// 不支持 Required 的 provider(DeepSeek / Mimo / Qwen)保持原 opts.ToolChoice,
	// 由 IntentParser + ClarificationLoop 在 runner 层兜底(Phase 3-5)。
	//
	// reasoner=true 的 provider 第一轮也允许纯 thinking,不强制 required
	// (由 Phase 6 reasoner 策略处理)。
	toolChoice := opts.ToolChoice
	if opts.SpecialistSubAgent && run.CurrentTurn == 1 {
		caps := llm.CapabilitiesOf(r.Provider)
		if caps.ToolChoiceSupport >= llm.ToolChoiceRequired && !caps.Reasoner {
			toolChoice = "required"
		}
	}

	req := &llm.ChatRequest{
		RunID:           run.ID,
		TurnIndex:       run.CurrentTurn,
		BrainID:         run.BrainID,
		System:          opts.System,
		Messages:        finalMessages,
		Tools:           opts.Tools,
		ToolChoice:      toolChoice,
		Model:           opts.Model,
		MaxTokens:       opts.MaxTokens,
		Stream:          opts.Stream,
		RemainingBudget: run.Budget.Remaining(),
	}

	// Apply cache points if CacheBuilder is set.
	if r.CacheBuilder != nil {
		var cachePoints []llm.CachePoint
		cachePoints = append(cachePoints, r.CacheBuilder.BuildL1System(opts.System)...)
		cachePoints = append(cachePoints, r.CacheBuilder.BuildL2Task(messages, opts.TaskBoundary)...)
		cachePoints = append(cachePoints, r.CacheBuilder.BuildL3History(messages)...)
		req.CacheControl = cachePoints
	}

	return req
}

// consumeStream calls Provider.Stream and drains the StreamReader into a
// synthetic ChatResponse, forwarding events to the StreamConsumer.
func (r *Runner) consumeStream(ctx context.Context, run *Run, turn *Turn, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	reader, err := r.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return drainStream(ctx, reader, run, turn, r.StreamConsumer)
}

// dispatchTools executes all tool_use blocks and returns the corresponding
// tool_result ContentBlocks. It returns the blocks and the count of tool
// calls actually dispatched.
//
// 当 Runner.BatchPlanner 非 nil 时，先调用 Plan() 将 tool_calls 按资源冲突
// 分组为 batch，同一 batch 内的工具并行执行，batch 之间串行执行。
// 当 BatchPlanner 为 nil 时保持原有串行逻辑（向后兼容）。
func (r *Runner) dispatchTools(ctx context.Context, run *Run, turn *Turn, registry tool.Registry, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	if r.BatchPlanner != nil {
		return r.dispatchToolsBatched(ctx, run, turn, registry, toolUseBlocks)
	}
	return r.dispatchToolsSerial(ctx, run, turn, registry, toolUseBlocks)
}

// dispatchToolsSerial 是原始的串行工具执行路径。
func (r *Runner) dispatchToolsSerial(ctx context.Context, run *Run, turn *Turn, registry tool.Registry, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	var results []llm.ContentBlock
	count := 0

	for _, tb := range toolUseBlocks {
		count++
		results = append(results, r.executeSingleTool(ctx, run, turn, registry, tb))
	}

	return results, count
}

// dispatchToolsBatched 使用 BatchPlanner 将 tool_calls 分组后按 batch 并行执行。
// 当 BatchPlanner 关联了 ResourceLocker 时，在每个 batch 执行前通过
// AcquireSet 获取资源租约，batch 完成后 Release 释放，确保资源互斥保护。
func (r *Runner) dispatchToolsBatched(ctx context.Context, run *Run, turn *Turn, registry tool.Registry, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	// 构建 ToolCallNode 列表，附带 ToolConcurrencySpec。
	nodes := make([]ToolCallNode, len(toolUseBlocks))
	for i, tb := range toolUseBlocks {
		var spec *tool.ToolConcurrencySpec
		if registry != nil {
			if t, found := registry.Lookup(tb.ToolName); found {
				spec = t.Schema().Concurrency
			}
		} else if t, found := r.ToolRegistry.Lookup(tb.ToolName); found {
			spec = t.Schema().Concurrency
		}
		nodes[i] = ToolCallNode{
			Index:    i,
			ToolName: tb.ToolName,
			Args:     tb.Input,
			Spec:     spec,
		}
	}

	// 调用 BatchPlanner 分组。
	plan, err := r.BatchPlanner.Plan(nodes)
	if err != nil {
		// Plan 失败时回退到串行执行。
		return r.dispatchToolsSerial(ctx, run, turn, registry, toolUseBlocks)
	}

	// 获取 ResourceLocker（可能为 nil）。
	locker := r.BatchPlanner.ResourceLocker()

	// 按 batch 顺序执行，batch 内并行。
	// 预分配结果数组以保持与原始 tool_call 顺序一致。
	results := make([]llm.ContentBlock, len(toolUseBlocks))
	count := 0

	for _, batch := range plan.Batches {
		if len(batch.Calls) == 0 {
			continue
		}

		// 获取该 batch 的资源租约。
		var tokens []LeaseToken
		if locker != nil && len(batch.Leases) > 0 {
			tokens, err = locker.AcquireSet(ctx, batch.Leases)
			if err != nil {
				// 租约获取失败（超时），将该 batch 所有工具标记为错误。
				for _, node := range batch.Calls {
					count++
					results[node.Index] = llm.ContentBlock{
						Type:      "tool_result",
						ToolUseID: toolUseBlocks[node.Index].ToolUseID,
						Output:    json.RawMessage(fmt.Sprintf(`"resource lease acquisition failed: %v"`, err)),
						IsError:   true,
					}
				}
				continue
			}
		}

		if len(batch.Calls) == 1 {
			// 单个工具无需并行开销。
			node := batch.Calls[0]
			count++
			results[node.Index] = r.executeSingleTool(ctx, run, turn, registry, toolUseBlocks[node.Index])
		} else {
			// 并行执行 batch 内的多个工具。
			var wg sync.WaitGroup
			for _, node := range batch.Calls {
				count++
				wg.Add(1)
				go func(n ToolCallNode) {
					defer wg.Done()
					results[n.Index] = r.executeSingleTool(ctx, run, turn, registry, toolUseBlocks[n.Index])
				}(node)
			}
			wg.Wait()
		}

		// 释放该 batch 的资源租约。
		if locker != nil && len(tokens) > 0 {
			locker.ReleaseAll(tokens)
		}
	}

	// 转换为有序的切片返回（过滤掉零值，虽然正常情况下不应有）。
	var ordered []llm.ContentBlock
	for _, r := range results {
		if r.Type != "" {
			ordered = append(ordered, r)
		}
	}

	return ordered, count
}

// executeSingleTool 执行单个工具调用，包含 lookup → execute → sanitize 全流程。
// 返回对应的 tool_result ContentBlock。
func (r *Runner) executeSingleTool(ctx context.Context, run *Run, turn *Turn, registry tool.Registry, tb llm.ContentBlock) llm.ContentBlock {
	if r.ToolObserver != nil {
		r.ToolObserver.OnToolStart(ctx, run, turn, tb.ToolName, tb.Input)
	}

	// Lookup tool in registry.
	if registry == nil {
		registry = r.ToolRegistry
	}
	t, found := registry.Lookup(tb.ToolName)
	if !found {
		if r.ToolObserver != nil {
			r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, false, json.RawMessage(`"tool not found"`))
		}
		return llm.ContentBlock{
			Type:      "tool_result",
			ToolUseID: tb.ToolUseID,
			Output:    json.RawMessage(fmt.Sprintf(`"tool not found: %s"`, tb.ToolName)),
			IsError:   true,
		}
	}

	// Execute the tool.
	result, execErr := t.Execute(ctx, tb.Input)
	if execErr != nil {
		errMsg, _ := json.Marshal(fmt.Sprintf("tool execution failed: %v", execErr))
		if r.ToolObserver != nil {
			r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, false, json.RawMessage(errMsg))
		}
		return llm.ContentBlock{
			Type:      "tool_result",
			ToolUseID: tb.ToolUseID,
			Output:    json.RawMessage(errMsg),
			IsError:   true,
		}
	}
	if result == nil {
		errMsg, _ := json.Marshal(fmt.Sprintf("tool %s returned nil result", tb.ToolName))
		if r.ToolObserver != nil {
			r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, false, json.RawMessage(errMsg))
		}
		return llm.ContentBlock{
			Type:      "tool_result",
			ToolUseID: tb.ToolUseID,
			Output:    json.RawMessage(errMsg),
			IsError:   true,
		}
	}

	// Sanitize if sanitizer is configured.
	if r.Sanitizer != nil {
		sanitized, sanitizeErr := r.Sanitizer.Sanitize(ctx, result, SanitizeMeta{
			ToolName: tb.ToolName,
			Risk:     t.Risk(),
			RunID:    run.ID,
		})
		if sanitizeErr != nil {
			if r.ToolObserver != nil {
				r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, false, json.RawMessage(fmt.Sprintf(`"sanitize error: %v"`, sanitizeErr)))
			}
			return llm.ContentBlock{
				Type:      "tool_result",
				ToolUseID: tb.ToolUseID,
				Output:    json.RawMessage(fmt.Sprintf(`"tool result sanitization failed: %v"`, sanitizeErr)),
				IsError:   true,
			}
		}
		// Use the sanitized block but preserve the tool_use_id.
		sanitized.ToolUseID = tb.ToolUseID
		if r.ToolObserver != nil {
			r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, !sanitized.IsError, sanitized.Output)
		}
		return *sanitized
	}

	// No sanitizer — pass through directly.
	// 防御:工具返回 result.Output 若为长度 0 的非 nil RawMessage,后续序列化
	// 给 LLM API 会触发 marshal 错。归一化为 nil 让 omitempty 正常工作。
	output := result.Output
	if len(output) == 0 {
		output = nil
	}
	block := llm.ContentBlock{
		Type:      "tool_result",
		ToolUseID: tb.ToolUseID,
		Output:    output,
		IsError:   result.IsError,
	}
	if r.ToolObserver != nil {
		r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, !result.IsError, result.Output)
	}
	return block
}

// assistantMessage wraps a ChatResponse's content as an assistant Message.
func assistantMessage(resp *llm.ChatResponse) llm.Message {
	return llm.Message{
		Role:    "assistant",
		Content: resp.Content,
	}
}

// toolResultMessage wraps tool_result ContentBlocks as a user Message,
// following the Anthropic API convention.
func toolResultMessage(blocks []llm.ContentBlock) llm.Message {
	return llm.Message{
		Role:    "user",
		Content: blocks,
	}
}

// contentHash produces a stable SHA-256 hash of the tool name + input args
// for the LoopDetector.
func contentHash(toolName string, input json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte("|"))
	h.Write(input)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// countTextChars 统计 ContentBlock 列表中所有 text/thinking 块的总字符数,
// 给 BRAIN_RUNNER_DEBUG 日志用,帮助判断 LLM 输出是否被截断。
func countTextChars(blocks []llm.ContentBlock) int {
	total := 0
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "thinking" {
			total += len(b.Text)
		}
	}
	return total
}

// 注:nudge 只是最低限度的安全网,真正的"说就调"判断应该让 LLM 自己学会
// (通过 tool description 和 prompt 契约)。这里不维护关键词白名单 — 那是无止境的,
// 而且会让 LLM 永远依赖兜底而不长进。
// 触发条件极保守:只对最显式的"我立刻提交工作流"类短语兜底一次,其余靠 LLM 自觉。
var announcePhrases = []string{
	// 仅保留最明确、最常见的"动手"宣告。其他场景靠 LLM 自己判断。
	"立刻提交", "立即提交", "现在提交",
	"提交工作流", "提交 workflow",
	"i'll submit", "i will submit",
	"submitting the workflow",
	"now submitting",
}

func shouldNudgeAnnouncement(blocks []llm.ContentBlock) bool {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(strings.ToLower(b.Text))
			sb.WriteByte(' ')
		}
	}
	text := sb.String()
	if text == "" {
		return false
	}
	for _, p := range announcePhrases {
		if strings.Contains(text, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// hasTextContent 检测 content blocks 是否含非空文本(忽略 thinking、tool_use 等)。
// 用于 ChatCentralBrain 模式下判断是否需要 nudge — LLM 输出了文本但无 tool_use。
func hasTextContent(blocks []llm.ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// hasAnyMeaningfulContent 检测 content blocks 是否含**任何**有意义的内容
// (text 或 thinking),用于 SpecialistSubAgent nudge 路径。
// 之所以不只看 text:LLM 在"思考但没决定调工具"时只发 thinking 块,
// 这种情况 sub agent 也该被 nudge 强制调工具,否则会直接 end_turn 退出。
func hasAnyMeaningfulContent(blocks []llm.ContentBlock) bool {
	for _, b := range blocks {
		if (b.Type == "text" || b.Type == "thinking") && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// announcementNudgeMessage 构造 user 角色的 reminder 消息(被 LLM 看作系统侧追问)。
// 用 user role 而非 system,避免破坏前置 system block 的 cache;且多数 provider 对
// "user 之后再来一条 user"是合法的(LLM 会把它当连续输入处理)。
//
// isCentral=true 时给 central 编排相关 tool 提示(delegate/submit_workflow);
// false 时给 sub agent 通用提示(write_file/edit_file/shell_exec/task_complete)。
// 让 nudge 与实际 registry 工具对齐,避免 LLM 看到"不存在的工具"困惑给空响应。
func announcementNudgeMessage() llm.Message {
	return announcementNudgeMessageFor(false)
}

func announcementNudgeMessageFor(isCentral bool) llm.Message {
	var hint string
	if isCentral {
		hint = "  • If the user wants you to do/build/make something: call submit_workflow (multi-step) or delegate (one-shot) NOW with concrete arguments.\n" +
			"  • If you need to read context first: call read_file / list_files / search.\n" +
			"  • If the request is unclear or impossible: call note to record your question, then briefly explain to the user.\n" +
			"  • If genuinely done: call task_complete with a summary."
	} else {
		hint = "  • If the user wants you to write/create code: call write_file / edit_file with the actual content.\n" +
			"  • If you need to inspect first: call read_file / list_files / search.\n" +
			"  • If you need to run a command: call shell_exec.\n" +
			"  • If genuinely done: call task_complete with a summary."
	}
	return llm.Message{
		Role: "user",
		Content: []llm.ContentBlock{{
			Type: "text",
			Text: "[system reminder] Your previous response had no tool_use block — only text. " +
				"In this system, text alone changes nothing; the user sees text but no work happens. " +
				"You MUST emit a tool_use block in this turn. Choose one:\n" +
				hint + "\n" +
				"Do not write another planning paragraph. Emit a tool_use block — that is the only way work gets done.",
		}},
	}
}
