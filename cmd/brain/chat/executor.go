package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type RunResult struct {
	Result    *loop.RunResult
	Err       error
	ReplyText string
	Canceled  bool
}

func StartChatRun(state *State, provider llm.Provider, brainID string, maxTurns int,
	input string, resultCh chan<- RunResult, progressCh chan<- ProgressEvent) {

	state.TurnCount++
	turnIndex := state.TurnCount

	baseMessages := make([]llm.Message, len(state.Messages))
	copy(baseMessages, state.Messages)
	registry := state.Registry
	opts := state.Opts
	runtime, _ := deps.NewDefaultCLIRuntime(brainID)
	var runRec *cliruntime.RunRecord
	if runtime != nil {
		runRec, _ = runtime.RunStore.Create(brainID, input, string(state.Mode), state.Sandbox.Primary())
	}

	ctx, cancel := config.WithOptionalTimeout(context.Background(), state.RunTimeout)
	gen := state.SetCancelRun(cancel)

	go func() {
		defer state.ClearCancelRun(gen)

		if runtime != nil && runRec != nil {
			_ = deps.SaveRunCheckpoint(ctx, runtime, runRec, "running", 0, runRec.ID+"-start")
			ctx = runtimeaudit.WithSink(ctx, runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
				_ = runtime.RunStore.AppendEvent(runRec.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
			}))
		}
		result, err := runChatTurn(ctx, provider, registry, opts, brainID, maxTurns,
			turnIndex, baseMessages, input, state.Sandbox.Primary(), state.RunTimeout, progressCh)
		if runtime != nil && runRec != nil {
			persistChatTurn(ctx, runtime, runRec, provider.Name(), input, state.Mode, state.Sandbox.Primary(), opts.System, result, err)
		}
		rr := RunResult{
			Result:   result,
			Err:      err,
			Canceled: ctx.Err() == context.Canceled,
		}
		if result != nil {
			rr.ReplyText = extractAssistantReply(result.FinalMessages)
		}
		resultCh <- rr
	}()
}

func runChatTurn(ctx context.Context, provider llm.Provider, registry tool.Registry,
	opts loop.RunOptions, brainID string, maxTurns int, turnIndex int,
	baseMessages []llm.Message, input, workdir string, maxDuration time.Duration,
	progressCh chan<- ProgressEvent) (*loop.RunResult, error) {

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

	runner := &loop.Runner{
		Provider:       provider,
		ToolRegistry:   registry,
		StreamConsumer: &LiveReporter{Ch: progressCh, Workdir: workdir},
		ToolObserver:   &LiveReporter{Ch: progressCh, Workdir: workdir},
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
