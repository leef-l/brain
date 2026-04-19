package loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

	// Now returns the current time. Defaults to time.Now().UTC when nil.
	Now func() time.Time
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

	// MaxTokens is the max output tokens per LLM call. Defaults to 4096.
	MaxTokens int

	// Stream enables the streaming path (Provider.Stream) instead of
	// Provider.Complete.
	Stream bool

	// TaskBoundary is the message index where task context ends and
	// rolling history begins, used by CacheBuilder.BuildL2Task.
	TaskBoundary int
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
func (r *Runner) Execute(ctx context.Context, run *Run, initialMessages []llm.Message, opts RunOptions) (*RunResult, error) {
	now := r.now()

	// Transition pending → running.
	if err := run.Start(now); err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}

	// Apply defaults.
	if opts.ToolChoice == "" {
		opts.ToolChoice = "auto"
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 4096
	}

	messages := make([]llm.Message, len(initialMessages))
	copy(messages, initialMessages)

	var turns []*TurnResult

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

		// Context cancellation check.
		if err := ctx.Err(); err != nil {
			run.Cancel(r.now())
			break
		}

		// Create a new Turn.
		run.CurrentTurn++
		turn := NewTurn(run.ID, run.CurrentTurn, now)

		// Build the ChatRequest.
		req := r.buildChatRequest(run, messages, opts)

		// Call LLM.
		var resp *llm.ChatResponse
		var llmErr error

		if opts.Stream {
			resp, llmErr = r.consumeStream(ctx, run, turn, req)
		} else {
			resp, llmErr = r.Provider.Complete(ctx, req)
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

		// If no tool calls or terminal stop reason → complete.
		if len(toolUseBlocks) == 0 || resp.StopReason != "tool_use" {
			turn.End(r.now())
			turns = append(turns, &TurnResult{
				Turn:      turn,
				Response:  resp,
				NextState: StateCompleted,
			})
			run.Complete(r.now())
			break
		}

		// Tool dispatch phase.
		run.State = StateWaitingTool
		toolResultBlocks, toolCallCount := r.dispatchTools(ctx, run, turn, toolUseBlocks)
		turn.ToolCalls += toolCallCount
		run.Budget.UsedToolCalls += toolCallCount

		// Append tool results as a user message.
		messages = append(messages, toolResultMessage(toolResultBlocks))

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
	}

done:
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

	req := &llm.ChatRequest{
		RunID:           run.ID,
		TurnIndex:       run.CurrentTurn,
		BrainID:         run.BrainID,
		System:          opts.System,
		Messages:        finalMessages,
		Tools:           opts.Tools,
		ToolChoice:      opts.ToolChoice,
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
func (r *Runner) dispatchTools(ctx context.Context, run *Run, turn *Turn, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	if r.BatchPlanner != nil {
		return r.dispatchToolsBatched(ctx, run, turn, toolUseBlocks)
	}
	return r.dispatchToolsSerial(ctx, run, turn, toolUseBlocks)
}

// dispatchToolsSerial 是原始的串行工具执行路径。
func (r *Runner) dispatchToolsSerial(ctx context.Context, run *Run, turn *Turn, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	var results []llm.ContentBlock
	count := 0

	for _, tb := range toolUseBlocks {
		count++
		results = append(results, r.executeSingleTool(ctx, run, turn, tb))
	}

	return results, count
}

// dispatchToolsBatched 使用 BatchPlanner 将 tool_calls 分组后按 batch 并行执行。
// 当 BatchPlanner 关联了 ResourceLocker 时，在每个 batch 执行前通过
// AcquireSet 获取资源租约，batch 完成后 Release 释放，确保资源互斥保护。
func (r *Runner) dispatchToolsBatched(ctx context.Context, run *Run, turn *Turn, toolUseBlocks []llm.ContentBlock) ([]llm.ContentBlock, int) {
	// 构建 ToolCallNode 列表，附带 ToolConcurrencySpec。
	nodes := make([]ToolCallNode, len(toolUseBlocks))
	for i, tb := range toolUseBlocks {
		var spec *tool.ToolConcurrencySpec
		if t, found := r.ToolRegistry.Lookup(tb.ToolName); found {
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
		return r.dispatchToolsSerial(ctx, run, turn, toolUseBlocks)
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
			results[node.Index] = r.executeSingleTool(ctx, run, turn, toolUseBlocks[node.Index])
		} else {
			// 并行执行 batch 内的多个工具。
			var wg sync.WaitGroup
			for _, node := range batch.Calls {
				count++
				wg.Add(1)
				go func(n ToolCallNode) {
					defer wg.Done()
					results[n.Index] = r.executeSingleTool(ctx, run, turn, toolUseBlocks[n.Index])
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
func (r *Runner) executeSingleTool(ctx context.Context, run *Run, turn *Turn, tb llm.ContentBlock) llm.ContentBlock {
	if r.ToolObserver != nil {
		r.ToolObserver.OnToolStart(ctx, run, turn, tb.ToolName, tb.Input)
	}

	// Lookup tool in registry.
	t, found := r.ToolRegistry.Lookup(tb.ToolName)
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
		if r.ToolObserver != nil {
			r.ToolObserver.OnToolEnd(ctx, run, turn, tb.ToolName, false, json.RawMessage(fmt.Sprintf(`"exec error: %v"`, execErr)))
		}
		return llm.ContentBlock{
			Type:      "tool_result",
			ToolUseID: tb.ToolUseID,
			Output:    json.RawMessage(fmt.Sprintf(`"tool execution failed: %v"`, execErr)),
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
	block := llm.ContentBlock{
		Type:      "tool_result",
		ToolUseID: tb.ToolUseID,
		Output:    result.Output,
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
