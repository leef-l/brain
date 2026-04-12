package llm

import (
	"context"
	"encoding/json"
)

// Provider is the main-process abstraction over a concrete LLM vendor
// (Anthropic, OpenAI, DeepSeek, ...). Implementations are registered in
// the Agent Loop Runner and selected per-Brain. See 02-核心架构.md §5 and
// 22-Agent-Loop规格.md §5.
type Provider interface {
	// Name returns the stable provider identifier (e.g. "anthropic").
	Name() string
	// Complete executes a single non-streaming chat request. See
	// 22-Agent-Loop规格.md §5 for the frozen v1 semantics.
	Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// Stream executes a streaming chat request and returns a StreamReader
	// the caller MUST Close. See 22-Agent-Loop规格.md §5 streaming contract.
	Stream(ctx context.Context, req *ChatRequest) (StreamReader, error)
}

// StreamReader is a pull-based iterator over streaming events produced by
// Provider.Stream. Callers MUST call Close exactly once when done. See
// 22-Agent-Loop规格.md §5.
type StreamReader interface {
	// Next blocks until the next StreamEvent is available or ctx is done.
	// It returns io.EOF (wrapped as a BrainError by the adapter) at end
	// of stream.
	Next(ctx context.Context) (StreamEvent, error)
	// Close releases any underlying resources (HTTP body, goroutines).
	Close() error
}

// StreamEvent is a single event in a provider stream, normalized to the
// v1 event type taxonomy in 22-Agent-Loop规格.md §5 streaming events.
type StreamEvent struct {
	// Type is the normalized event type.
	Type StreamEventType
	// Data is the raw JSON payload associated with the event, shape
	// depending on Type. See 22-Agent-Loop规格.md §5.
	Data json.RawMessage
}

// StreamEventType is the normalized streaming event type, defined in
// 22-Agent-Loop规格.md §5. v1 is frozen: new types MAY be added but
// existing ones MUST NOT be renamed or removed.
type StreamEventType string

// Frozen v1 stream event types — see 22-Agent-Loop规格.md §5.
const (
	// EventMessageStart marks the start of an assistant message.
	EventMessageStart StreamEventType = "message.start"
	// EventContentDelta carries an incremental text content delta.
	EventContentDelta StreamEventType = "content.delta"
	// EventToolCallDelta carries an incremental tool_use input delta.
	EventToolCallDelta StreamEventType = "tool_call.delta"
	// EventMessageDelta carries incremental message-level metadata
	// updates (e.g. stop_reason, usage).
	EventMessageDelta StreamEventType = "message.delta"
	// EventMessageEnd marks the end of the assistant message.
	EventMessageEnd StreamEventType = "message.end"
)
