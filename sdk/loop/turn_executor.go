package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

// TurnExecutor implements the frozen Executor interface from
// 22-Agent-Loop规格.md §6.2. It drives exactly one Turn of a Run: calls
// the LLM Provider, dispatches any tool_use blocks via the ToolRegistry,
// sanitizes tool results, and produces a TurnResult with the computed
// NextState.
//
// All optional fields (Sanitizer, StreamConsumer) are nil-safe: when nil
// the corresponding stage is skipped.
type TurnExecutor struct {
	// Provider is the LLM provider to call. Required.
	Provider llm.Provider

	// ToolRegistry is the tool catalog for resolving tool_use blocks. Required.
	ToolRegistry tool.Registry

	// Sanitizer sanitizes tool results before they are fed back to the LLM.
	// When nil, tool results are passed through without sanitization.
	Sanitizer ToolResultSanitizer

	// StreamConsumer receives streaming callbacks when the request uses
	// Provider.Stream. When nil, streaming events are consumed silently.
	StreamConsumer StreamConsumer

	// Now returns the current time. Defaults to time.Now().UTC when nil.
	Now func() time.Time
}

// now returns the current time using the configured clock or the default.
func (e *TurnExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

// Execute drives exactly one Turn of the Run. It creates a Turn, calls the
// LLM, optionally dispatches tool calls, and returns a TurnResult. The
// caller (Runner) is responsible for state-machine transitions and message
// history management.
//
// See 22-Agent-Loop规格.md §6.2.
func (e *TurnExecutor) Execute(ctx context.Context, run *Run, req *llm.ChatRequest) (*TurnResult, error) {
	now := e.now()
	turn := NewTurn(run.ID, run.CurrentTurn, now)

	// Call LLM.
	var resp *llm.ChatResponse
	var llmErr error

	if req.Stream {
		resp, llmErr = e.consumeStream(ctx, run, turn, req)
	} else {
		resp, llmErr = e.Provider.Complete(ctx, req)
	}
	turn.LLMCalls++

	if llmErr != nil {
		turn.End(e.now())
		be := toBrainError(llmErr)
		return &TurnResult{
			Turn:      turn,
			Response:  nil,
			NextState: StateFailed,
			Error:     be,
		}, nil
	}

	// Determine next state from response.
	toolUseBlocks := extractToolUseBlocks(resp.Content)

	var nextState State
	if len(toolUseBlocks) > 0 && resp.StopReason == "tool_use" {
		nextState = StateRunning // Runner will loop back after tool dispatch
	} else {
		nextState = StateCompleted
	}

	turn.End(e.now())

	return &TurnResult{
		Turn:      turn,
		Response:  resp,
		NextState: nextState,
	}, nil
}

// consumeStream drains a StreamReader into a synthetic ChatResponse. It
// forwards events to the StreamConsumer when non-nil.
func (e *TurnExecutor) consumeStream(ctx context.Context, run *Run, turn *Turn, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	reader, err := e.Provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return drainStream(ctx, reader, run, turn, e.StreamConsumer)
}

// drainStream reads all events from a StreamReader and assembles a
// ChatResponse. It forwards events to consumer when non-nil. This is a
// shared helper used by both TurnExecutor and Runner.
func drainStream(ctx context.Context, reader llm.StreamReader, run *Run, turn *Turn, consumer StreamConsumer) (*llm.ChatResponse, error) {
	resp := &llm.ChatResponse{}

	// Accumulators for building content blocks from stream deltas.
	var textAccum string
	var toolCalls []llm.ContentBlock
	var currentToolCall *llm.ContentBlock

	for {
		ev, err := reader.Next(ctx)
		if err != nil {
			// End of stream or error — if we have accumulated data, use it.
			// MockProvider signals EOF via a BrainError with "EOF" in the message.
			if resp.StopReason != "" {
				break
			}
			// If we have no data at all, propagate the error.
			if len(resp.Content) == 0 && textAccum == "" && len(toolCalls) == 0 {
				return nil, err
			}
			break
		}

		switch ev.Type {
		case llm.EventMessageStart:
			// Parse message ID and model from the start event.
			var startData struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			}
			if json.Unmarshal(ev.Data, &startData) == nil {
				resp.ID = startData.ID
				resp.Model = startData.Model
			}
			if consumer != nil {
				consumer.OnMessageStart(ctx, run, turn)
			}

		case llm.EventContentDelta:
			var deltaData struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(ev.Data, &deltaData) == nil {
				textAccum += deltaData.Text
				if consumer != nil {
					consumer.OnContentDelta(ctx, run, turn, deltaData.Text)
				}
			}

		case llm.EventToolCallDelta:
			var tcData struct {
				ToolUseID string          `json:"tool_use_id"`
				ToolName  string          `json:"tool_name"`
				Input     json.RawMessage `json:"input"`
			}
			if json.Unmarshal(ev.Data, &tcData) == nil {
				if tcData.ToolName != "" {
					// New tool call — flush any pending one.
					if currentToolCall != nil {
						toolCalls = append(toolCalls, *currentToolCall)
					}
					currentToolCall = &llm.ContentBlock{
						Type:      "tool_use",
						ToolUseID: tcData.ToolUseID,
						ToolName:  tcData.ToolName,
						Input:     tcData.Input,
					}
				} else if currentToolCall != nil && len(tcData.Input) > 0 {
					// Append partial input JSON to current tool call.
					currentToolCall.Input = append(currentToolCall.Input, tcData.Input...)
				}
				if consumer != nil {
					consumer.OnToolCallDelta(ctx, run, turn, tcData.ToolName, string(tcData.Input))
				}
			}

		case llm.EventMessageDelta:
			var mdData struct {
				StopReason string    `json:"stop_reason"`
				Usage      llm.Usage `json:"usage"`
			}
			if json.Unmarshal(ev.Data, &mdData) == nil {
				if mdData.StopReason != "" {
					resp.StopReason = mdData.StopReason
				}
				resp.Usage = mdData.Usage
			}
			if consumer != nil {
				consumer.OnMessageDelta(ctx, run, turn, ev.Data)
			}

		case llm.EventMessageEnd:
			var endData struct {
				Usage llm.Usage `json:"usage"`
			}
			if json.Unmarshal(ev.Data, &endData) == nil {
				resp.Usage = endData.Usage
			}
			resp.FinishedAt = time.Now().UTC()
			if consumer != nil {
				consumer.OnMessageEnd(ctx, run, turn, resp.Usage)
			}
			// Message end — break out of the loop.
			goto done
		}
	}

done:
	// Flush any pending text accumulator into a content block.
	if textAccum != "" {
		resp.Content = append(resp.Content, llm.ContentBlock{
			Type: "text",
			Text: textAccum,
		})
	}

	// Flush any pending tool call.
	if currentToolCall != nil {
		toolCalls = append(toolCalls, *currentToolCall)
	}
	resp.Content = append(resp.Content, toolCalls...)

	if resp.FinishedAt.IsZero() {
		resp.FinishedAt = time.Now().UTC()
	}

	return resp, nil
}

// extractToolUseBlocks returns only tool_use content blocks from a response.
func extractToolUseBlocks(content []llm.ContentBlock) []llm.ContentBlock {
	var blocks []llm.ContentBlock
	for _, b := range content {
		if b.Type == "tool_use" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// toBrainError converts any error into a *brainerrors.BrainError. If the
// error is already a BrainError it is returned as-is; otherwise it is
// wrapped with CodeUnknown.
func toBrainError(err error) *brainerrors.BrainError {
	if err == nil {
		return nil
	}
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		return be
	}
	return brainerrors.New(brainerrors.CodeUnknown,
		brainerrors.WithMessage(fmt.Sprintf("unexpected error: %v", err)),
	)
}
