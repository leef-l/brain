package sidecar

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

// executionStreamConsumer 实现 loop.StreamConsumer，将 LLM 流式事件通过
// brain/progress Notify 实时推送到 host Brain。
type executionStreamConsumer struct {
	executionID string
}

func newExecutionStreamConsumer(executionID string) *executionStreamConsumer {
	return &executionStreamConsumer{executionID: executionID}
}

func (c *executionStreamConsumer) OnMessageStart(ctx context.Context, run *loop.Run, turn *loop.Turn) {
	EmitProgress(ctx, ProgressEvent{
		Kind:        "llm_start",
		ExecutionID: c.executionID,
	})
}

func (c *executionStreamConsumer) OnContentDelta(ctx context.Context, run *loop.Run, turn *loop.Turn, text string) {
	EmitProgress(ctx, ProgressEvent{
		Kind:        "content",
		ExecutionID: c.executionID,
		Message:     text,
	})
}

func (c *executionStreamConsumer) OnToolCallDelta(ctx context.Context, run *loop.Run, turn *loop.Turn, toolName string, argsPartial string) {
	EmitProgress(ctx, ProgressEvent{
		Kind:        "tool_call_delta",
		ExecutionID: c.executionID,
		ToolName:    toolName,
		Detail:      argsPartial,
	})
}

func (c *executionStreamConsumer) OnMessageDelta(ctx context.Context, run *loop.Run, turn *loop.Turn, delta json.RawMessage) {
	EmitProgress(ctx, ProgressEvent{
		Kind:        "llm_delta",
		ExecutionID: c.executionID,
		Detail:      string(delta),
	})
}

func (c *executionStreamConsumer) OnMessageEnd(ctx context.Context, run *loop.Run, turn *loop.Turn, usage llm.Usage) {
	usageJSON, _ := json.Marshal(usage)
	EmitProgress(ctx, ProgressEvent{
		Kind:        "llm_end",
		ExecutionID: c.executionID,
		Detail:      string(usageJSON),
	})
}
