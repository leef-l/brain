package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/leef-l/brain/llm"
	"github.com/leef-l/brain/loop"
	"github.com/leef-l/brain/runtimeaudit"
	"github.com/leef-l/brain/tool"
)

type managedRunExecution struct {
	Runtime       *cliRuntime
	Record        *persistedRunRecord
	Registry      tool.Registry
	Provider      llm.Provider
	ProviderName  string
	ProviderModel string
	BrainID       string
	Prompt        string
	MaxTurns      int
	MaxDuration   time.Duration
	Stream        bool
	SystemPrompt  string
}

type managedRunOutcome struct {
	Result      *loop.RunResult
	ReplyText   string
	Summary     map[string]interface{}
	SummaryJSON json.RawMessage
	PlanID      int64
	FinalStatus string
}

func executeManagedRun(ctx context.Context, req managedRunExecution) (*managedRunOutcome, error) {
	ctx = runtimeaudit.WithSink(ctx, runtimeaudit.SinkFunc(func(_ context.Context, ev runtimeaudit.Event) {
		if req.Runtime == nil || req.Runtime.RunStore == nil || req.Record == nil {
			return
		}
		_ = req.Runtime.RunStore.appendEvent(req.Record.ID, ev.Type, ev.Message, append(json.RawMessage(nil), ev.Data...))
	}))
	_ = req.Runtime.RunStore.appendEvent(req.Record.ID, "run.started", "run started", json.RawMessage(fmtJSON(map[string]interface{}{
		"brain":          req.BrainID,
		"provider":       req.ProviderName,
		"provider_model": req.ProviderModel,
	})))

	run := loop.NewRun(
		req.Record.ID,
		req.BrainID,
		managedRunBudget(req.MaxTurns, req.MaxDuration),
	)

	runner := &loop.Runner{
		Provider:     req.Provider,
		ToolRegistry: req.Registry,
		ToolObserver: &runtimeToolObserver{
			runtime: req.Runtime,
			runID:   req.Record.ID,
		},
	}

	opts := loop.RunOptions{
		System:     []llm.SystemBlock{{Text: req.SystemPrompt, Cache: true}},
		Tools:      buildToolSchemas(req.Registry),
		ToolChoice: "auto",
		MaxTokens:  4096,
		Stream:     req.Stream,
	}

	if err := saveRunCheckpoint(ctx, req.Runtime, req.Record, "running", 0, req.Record.ID+"-start"); err != nil {
		return nil, err
	}

	result, err := runner.Execute(ctx, run, []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: req.Prompt}}},
	}, opts)
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() == context.Canceled {
			status = "cancelled"
		}
		_, _ = req.Runtime.RunStore.finish(req.Record.ID, status, errJSON, err.Error())
		return nil, err
	}

	finalTurnIndex := 0
	finalTurnUUID := req.Record.ID + "-completed"
	if n := len(result.Turns); n > 0 && result.Turns[n-1] != nil && result.Turns[n-1].Turn != nil {
		finalTurnIndex = result.Turns[n-1].Turn.Index
		if result.Turns[n-1].Turn.UUID != "" {
			finalTurnUUID = result.Turns[n-1].Turn.UUID
		}
	}

	finalStatus := string(result.Run.State)
	if err := saveRunCheckpoint(ctx, req.Runtime, req.Record, finalStatus, finalTurnIndex, finalTurnUUID); err != nil {
		return nil, err
	}
	if err := saveRunUsage(ctx, req.Runtime, req.Record, req.ProviderName, req.ProviderModel, result); err != nil {
		return nil, err
	}

	replyText := ""
	for i := len(result.FinalMessages) - 1; i >= 0; i-- {
		msg := result.FinalMessages[i]
		if msg.Role == "assistant" {
			replyText = extractText(msg.Content)
			break
		}
	}

	planID, err := saveRunPlan(ctx, req.Runtime, req.Record, map[string]interface{}{
		"run_id":         result.Run.ID,
		"store_run_id":   req.Record.StoreRunID,
		"brain_id":       result.Run.BrainID,
		"prompt":         req.Prompt,
		"state":          finalStatus,
		"turns":          result.Run.Budget.UsedTurns,
		"llm_calls":      result.Run.Budget.UsedLLMCalls,
		"tool_calls":     result.Run.Budget.UsedToolCalls,
		"provider":       req.ProviderName,
		"provider_model": req.ProviderModel,
	})
	if err != nil {
		return nil, err
	}

	summary := map[string]interface{}{
		"run_id":       result.Run.ID,
		"store_run_id": req.Record.StoreRunID,
		"brain_id":     result.Run.BrainID,
		"state":        finalStatus,
		"turns":        result.Run.Budget.UsedTurns,
		"llm_calls":    result.Run.Budget.UsedLLMCalls,
		"tool_calls":   result.Run.Budget.UsedToolCalls,
		"elapsed_ms":   result.Run.Budget.ElapsedTime.Milliseconds(),
		"reply":        replyText,
		"provider":     req.ProviderName,
		"plan_id":      planID,
	}
	summaryJSON, _ := json.Marshal(summary)
	if _, err := req.Runtime.RunStore.finish(req.Record.ID, finalStatus, summaryJSON, ""); err != nil {
		return nil, err
	}

	return &managedRunOutcome{
		Result:      result,
		ReplyText:   replyText,
		Summary:     summary,
		SummaryJSON: summaryJSON,
		PlanID:      planID,
		FinalStatus: finalStatus,
	}, nil
}

func managedRunBudget(maxTurns int, maxDuration time.Duration) loop.Budget {
	return loop.Budget{
		MaxTurns:     maxTurns,
		MaxCostUSD:   2.0,
		MaxLLMCalls:  maxTurns * 2,
		MaxToolCalls: maxTurns * 4,
		MaxDuration:  effectiveRunMaxDuration(maxDuration, 0),
	}
}

type runtimeToolObserver struct {
	runtime *cliRuntime
	runID   string
}

func (o *runtimeToolObserver) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, input json.RawMessage) {
	if o == nil || o.runtime == nil || o.runtime.RunStore == nil {
		return
	}
	data := map[string]interface{}{
		"tool":  toolName,
		"input": json.RawMessage(append([]byte(nil), input...)),
	}
	_ = o.runtime.RunStore.appendEvent(o.runID, "tool.start", toolName, json.RawMessage(fmtJSON(data)))
}

func (o *runtimeToolObserver) OnToolEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	if o == nil || o.runtime == nil || o.runtime.RunStore == nil {
		return
	}
	data := map[string]interface{}{
		"tool":   toolName,
		"ok":     ok,
		"output": json.RawMessage(append([]byte(nil), output...)),
	}
	_ = o.runtime.RunStore.appendEvent(o.runID, "tool.end", toolName, json.RawMessage(fmtJSON(data)))
}

func fmtJSON(v interface{}) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
