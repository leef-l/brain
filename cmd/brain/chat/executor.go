package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type RunResult struct {
	Result       *loop.RunResult
	Err          error
	ReplyText    string
	Canceled     bool
	BaseMsgCount int // 执行前 state.Messages 的长度，用于追加而非覆盖
}

// StartChatRun 启动一个新的 chat run，返回分配的 runID。
// 事件（progress + result）通过 eventCh 统一发送。
func StartChatRun(state *State, provider llm.Provider, brainID string, maxTurns int,
	input string, eventCh chan<- ChatEvent) string {

	state.TurnCount++
	turnIndex := state.TurnCount

	baseMessages := make([]llm.Message, len(state.Messages))
	copy(baseMessages, state.Messages)
	baseMsgCount := len(state.Messages)
	registry := state.Registry
	opts := state.Opts
	runtime, _ := deps.NewDefaultCLIRuntime(brainID)
	var runRec *cliruntime.RunRecord
	if runtime != nil {
		runRec, _ = runtime.RunStore.Create(brainID, input, string(state.Mode), state.Sandbox.Primary())
	}

	ctx, cancel := config.WithOptionalTimeout(context.Background(), state.RunTimeout)
	runID := state.StartRun(input, cancel)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\n[brain panic] run %s: %v\n", runID, r)
				select {
				case eventCh <- ChatEvent{RunID: runID, Type: "result", Result: &RunResult{
					Err:          fmt.Errorf("internal panic: %v", r),
					BaseMsgCount: baseMsgCount,
				}}:
				case <-ctx.Done():
				}
			}
			state.RemoveRun(runID)
		}()

		if runtime != nil && runRec != nil {
			_ = deps.SaveRunCheckpoint(ctx, runtime, runRec, "running", 0, runRec.ID+"-start")
			ctx = runtimeaudit.WithSink(ctx, runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
				_ = runtime.RunStore.AppendEvent(runRec.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
			}))
		}
		result, err := runChatTurn(ctx, provider, registry, opts, brainID, maxTurns,
			turnIndex, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, runID, eventCh)
		if runtime != nil && runRec != nil {
			persistChatTurn(ctx, runtime, runRec, provider.Name(), input, state.Mode, state.Sandbox.Primary(), opts.System, result, err)
		}
		// MACCS Wave 7+ 项目级持久化:把本 turn 的 user + assistant 消息写到
		// project_conversations 表,并更新 last_active_at。
		// 仅在选了项目(非无项目模式)时执行。
		persistChatTurnToProject(ctx, state, input, result, err)
		rr := RunResult{
			Result:       result,
			Err:          err,
			Canceled:     ctx.Err() == context.Canceled,
			BaseMsgCount: baseMsgCount,
		}
		if result != nil {
			rr.ReplyText = extractAssistantReply(result.FinalMessages)
		}
		select {
		case eventCh <- ChatEvent{RunID: runID, Type: "result", Result: &rr}:
		case <-ctx.Done():
		}
	}()

	return runID
}

func runChatTurn(ctx context.Context, provider llm.Provider, registry tool.Registry,
	opts loop.RunOptions, brainID string, maxTurns int, turnIndex int,
	baseMessages []llm.Message, input, workdir string, maxDuration time.Duration,
	runID string, eventCh chan<- ChatEvent) (*loop.RunResult, error) {
	ctx = kernel.WithSubtaskContext(ctx, &protocol.SubtaskContext{
		UserUtterance: input,
		TurnIndex:     turnIndex,
	})

	// MACCS 自动委派判断（仅 central 模式）：扫一遍用户输入的关键词，命中
	// browser/code/verifier 等明确意图时，给 LLM 加一条 system hint 强制
	// 走 delegate，不靠它"自觉" Tier 决策。
	if brainID == "central" {
		if hint := DispatchHint(input); hint != "" {
			opts.System = append(append([]llm.SystemBlock(nil), opts.System...),
				llm.SystemBlock{Text: hint, Cache: false})
		}
	}

	messages := append(baseMessages, llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: input}},
	})

	run := loop.NewRun(
		fmt.Sprintf("chat-%d-%s", turnIndex, time.Now().UTC().Format("150405")),
		brainID,
		loop.Budget{
			MaxTurns:     maxTurns,
			MaxCostUSD:   5.0,
			MaxLLMCalls:  maxTurns * 2,
			MaxToolCalls: maxTurns * 4,
			MaxDuration:  config.EffectiveRunMaxDuration(maxDuration, 5*time.Minute),
		},
	)

	reporter := &LiveReporter{RunID: runID, Ch: eventCh, Workdir: workdir}
	runner := &loop.Runner{
		Provider:       provider,
		ToolRegistry:   registry,
		StreamConsumer: reporter,
		ToolObserver:   reporter,
		Sanitizer:      loop.NewMemSanitizer(),
		LoopDetector:   loop.NewMemLoopDetector(),
		CacheBuilder:   loop.NewMemCacheBuilder(),
	}
	opts.Stream = true

	return runner.Execute(ctx, run, messages, opts)
}

func persistChatTurn(ctx context.Context, runtime *cliruntime.Runtime, runRec *cliruntime.RunRecord, providerName, input string, mode env.PermissionMode, workdir string, system []llm.SystemBlock, result *loop.RunResult, err error) {
	if runtime == nil || runRec == nil {
		return
	}
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() == context.Canceled {
			status = "canceled"
		}
		_ = cliruntime.SaveRunCheckpointWithMessages(ctx, runtime, runRec, status, 0, runRec.ID+"-"+status, nil, system)
		_, _ = runtime.RunStore.Finish(runRec.ID, status, errJSON, err.Error())
		return
	}

	finalTurnIndex := 0
	finalTurnUUID := runRec.ID + "-completed"
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Turn != nil {
		finalTurnIndex = result.Turns[n-1].Turn.Index
		if result.Turns[n-1].Turn.UUID != "" {
			finalTurnUUID = result.Turns[n-1].Turn.UUID
		}
	}
	_ = cliruntime.SaveRunCheckpointWithMessages(ctx, runtime, runRec, string(result.Run.State), finalTurnIndex, finalTurnUUID, result.FinalMessages, system)
	_ = deps.SaveRunUsage(ctx, runtime, runRec, providerName, "", result)

	replyText := extractAssistantReply(result.FinalMessages)
	planID, _ := deps.SaveRunPlan(ctx, runtime, runRec, map[string]interface{}{
		"chat_turn":       true,
		"run_id":          result.Run.ID,
		"store_run_id":    runRec.StoreRunID,
		"brain_id":        result.Run.BrainID,
		"prompt":          input,
		"state":           string(result.Run.State),
		"turns":           result.Run.Budget.UsedTurns,
		"llm_calls":       result.Run.Budget.UsedLLMCalls,
		"tool_calls":      result.Run.Budget.UsedToolCalls,
		"provider":        providerName,
		"permission_mode": string(mode),
		"workdir":         workdir,
	})
	summary, _ := json.Marshal(map[string]interface{}{
		"chat_turn":    true,
		"run_id":       runRec.ID,
		"store_run_id": runRec.StoreRunID,
		"brain_id":     result.Run.BrainID,
		"state":        string(result.Run.State),
		"turns":        result.Run.Budget.UsedTurns,
		"llm_calls":    result.Run.Budget.UsedLLMCalls,
		"tool_calls":   result.Run.Budget.UsedToolCalls,
		"elapsed_ms":   result.Run.Budget.ElapsedTime.Milliseconds(),
		"reply":        replyText,
		"provider":     providerName,
		"plan_id":      planID,
	})
	_, _ = runtime.RunStore.Finish(runRec.ID, string(result.Run.State), summary, "")
}

func extractAssistantReply(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return extractText(messages[i].Content)
		}
	}
	return ""
}

// persistChatTurnToProject 把本 turn 的 user + assistant 消息写入 project_conversations
// 并更新 projects.last_active_at。MACCS Wave 7+ 多项目持久化的核心写入点。
// 任意失败 silent(stderr 打印),不阻塞主流程。
func persistChatTurnToProject(ctx context.Context, state *State, input string, result *loop.RunResult, runErr error) {
	if state == nil || state.IsNoProject || state.CurrentProject == nil {
		return
	}
	if state.ProjectStore == nil || state.ProjectsStore == nil {
		return
	}
	pid := state.CurrentProject.ID

	// 写 user message
	userMsg := llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: input}},
	}
	toSave := []llm.Message{userMsg}

	// 写 assistant 回复(若有)
	if runErr == nil && result != nil && len(result.FinalMessages) > 0 {
		// 取最后一条 assistant 消息(往后追加 user 之后的所有 assistant 内容也行,
		// 但 chat 单 turn 通常只产出一条 assistant 终态)
		for i := len(result.FinalMessages) - 1; i >= 0; i-- {
			if result.FinalMessages[i].Role == "assistant" {
				toSave = append(toSave, result.FinalMessages[i])
				break
			}
		}
	}

	if err := state.ProjectStore.SaveMessages(ctx, pid, toSave); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: persist project messages: %v\n", err)
	}
	if err := state.ProjectsStore.UpdateLastActive(ctx, pid, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "brain chat: update project last_active: %v\n", err)
	}
}
