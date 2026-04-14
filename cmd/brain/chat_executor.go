package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/runtimeaudit"
	"github.com/leef-l/brain/sdk/tool"
)

type chatRunResult struct {
	result    *loop.RunResult
	err       error
	replyText string
	canceled  bool
}

func startChatRun(state *chatState, provider llm.Provider, brainID string, maxTurns int,
	input string, resultCh chan<- chatRunResult, progressCh chan<- chatProgressEvent) {

	state.turnCount++
	turnIndex := state.turnCount

	baseMessages := make([]llm.Message, len(state.messages))
	copy(baseMessages, state.messages)
	registry := state.registry
	opts := state.opts
	runtime, _ := newDefaultCLIRuntime(brainID)
	var runRec *persistedRunRecord
	if runtime != nil {
		runRec, _ = runtime.RunStore.create(brainID, input, string(state.mode), state.sandbox.Primary())
	}

	ctx, cancel := withOptionalTimeout(context.Background(), state.runTimeout)
	gen := state.setCancelRun(cancel)

	go func() {
		defer state.clearCancelRun(gen)

		if runtime != nil && runRec != nil {
			_ = saveRunCheckpoint(ctx, runtime, runRec, "running", 0, runRec.ID+"-start")
			ctx = runtimeaudit.WithSink(ctx, runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
				_ = runtime.RunStore.appendEvent(runRec.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
			}))
		}
		result, err := runChatTurn(ctx, provider, registry, opts, brainID, maxTurns,
			turnIndex, baseMessages, input, state.sandbox.Primary(), state.runTimeout, progressCh)
		if runtime != nil && runRec != nil {
			persistChatTurn(ctx, runtime, runRec, provider.Name(), input, state.mode, state.sandbox.Primary(), result, err)
		}
		rr := chatRunResult{
			result:   result,
			err:      err,
			canceled: ctx.Err() == context.Canceled,
		}
		if result != nil {
			rr.replyText = extractAssistantReply(result.FinalMessages)
		}
		resultCh <- rr
	}()
}

func runChatTurn(ctx context.Context, provider llm.Provider, registry tool.Registry,
	opts loop.RunOptions, brainID string, maxTurns int, turnIndex int,
	baseMessages []llm.Message, input, workdir string, maxDuration time.Duration,
	progressCh chan<- chatProgressEvent) (*loop.RunResult, error) {

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
			MaxDuration:  effectiveRunMaxDuration(maxDuration, 5*time.Minute),
		},
	)

	runner := &loop.Runner{
		Provider:       provider,
		ToolRegistry:   registry,
		StreamConsumer: &chatLiveReporter{ch: progressCh, workdir: workdir},
		ToolObserver:   &chatLiveReporter{ch: progressCh, workdir: workdir},
	}
	opts.Stream = true

	return runner.Execute(ctx, run, messages, opts)
}

func persistChatTurn(ctx context.Context, runtime *cliRuntime, runRec *persistedRunRecord, providerName, input string, mode chatMode, workdir string, result *loop.RunResult, err error) {
	if runtime == nil || runRec == nil {
		return
	}
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() == context.Canceled {
			status = "cancelled"
		}
		_, _ = runtime.RunStore.finish(runRec.ID, status, errJSON, err.Error())
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
	_ = saveRunCheckpoint(ctx, runtime, runRec, string(result.Run.State), finalTurnIndex, finalTurnUUID)
	_ = saveRunUsage(ctx, runtime, runRec, providerName, "", result)

	replyText := extractAssistantReply(result.FinalMessages)
	planID, _ := saveRunPlan(ctx, runtime, runRec, map[string]interface{}{
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
	_, _ = runtime.RunStore.finish(runRec.ID, string(result.Run.State), summary, "")
}

func extractAssistantReply(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return extractText(messages[i].Content)
		}
	}
	return ""
}
