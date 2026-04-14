package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/leef-l/brain/sdk/llm"
)

// MemStreamBuffer holds the accumulated event data for a single (runID, turnIndex)
// pair. It is filled by MemStreamConsumer's On* callbacks and may be read via
// MemStreamConsumer.Snapshot for testing or logging. All fields are guarded by
// the parent MemStreamConsumer.mu lock when accessed from outside a single-
// goroutine context.
//
// See 22-Agent-Loop规格.md §7.
type MemStreamBuffer struct {
	// RunID is the parent Run identifier. See 22-Agent-Loop规格.md §4.1.
	RunID string

	// TurnIndex is the 1-based ordinal of the Turn. See 22-Agent-Loop规格.md §6.1.
	TurnIndex int

	// Content accumulates all text fragments received via OnContentDelta.
	// The final value is the full assistant message text for this Turn.
	// See 22-Agent-Loop规格.md §7.2.
	Content strings.Builder

	// ToolCalls accumulates partial tool-call frames received via
	// OnToolCallDelta, in arrival order. See 22-Agent-Loop规格.md §7.3.
	ToolCalls []MemToolCallFrame

	// MessageDeltas collects the raw JSON payloads from every OnMessageDelta
	// event in arrival order. See 22-Agent-Loop规格.md §7.4.
	MessageDeltas []json.RawMessage

	// FinalUsage is the token and cost accounting received from OnMessageEnd.
	// Zero-value until OnMessageEnd fires. See 22-Agent-Loop规格.md §7.5.
	FinalUsage llm.Usage

	// EventCounts tracks how many times each event type has fired for this
	// (runID, turnIndex). Keys follow the stream event type names:
	// "message_start", "content_delta", "tool_call_delta", "message_delta",
	// "message_end". See 22-Agent-Loop规格.md §7.
	EventCounts map[string]int

	// Finished is set to true when OnMessageEnd fires. A Finished buffer
	// MUST NOT receive further events. See 22-Agent-Loop规格.md §7.5.
	Finished bool
}

// MemToolCallFrame holds the accumulated state for a single streaming tool
// call, built up from successive OnToolCallDelta events.
//
// See 22-Agent-Loop规格.md §7.3.
type MemToolCallFrame struct {
	// ToolName is the best-known tool identifier so far. It MAY be empty on
	// the very first delta and is updated as later deltas carry the full name.
	// See 22-Agent-Loop规格.md §7.3.
	ToolName string

	// ArgsPartial accumulates the JSON argument fragments in arrival order.
	// The final value is the complete JSON arguments string when the LLM has
	// finished emitting the tool call. See 22-Agent-Loop规格.md §7.3.
	ArgsPartial strings.Builder
}

// bufferKey returns the map key used to look up a MemStreamBuffer by (runID,
// turnIndex). The format is deterministic and collision-free for non-empty
// runIDs.
func bufferKey(runID string, turnIndex int) string {
	return fmt.Sprintf("%s/%d", runID, turnIndex)
}

// MemStreamConsumer is the in-process StreamConsumer implementation. It
// records every stream event into a per-(runID, turnIndex) MemStreamBuffer
// using an internal sync.Mutex for concurrent-safety. Callers retrieve
// accumulated state via Snapshot.
//
// All five On* methods are idempotent with respect to event ordering and
// safe to call from the goroutine that owns the stream. They MUST NOT be
// called after OnMessageEnd has fired for the same (runID, turnIndex) pair.
//
// See 22-Agent-Loop规格.md §7.
type MemStreamConsumer struct {
	mu      sync.Mutex
	buffers map[string]*MemStreamBuffer
}

// NewMemStreamConsumer constructs an empty MemStreamConsumer ready to receive
// events from any number of concurrent Runs.
//
// See 22-Agent-Loop规格.md §7.
func NewMemStreamConsumer() *MemStreamConsumer {
	return &MemStreamConsumer{
		buffers: make(map[string]*MemStreamBuffer),
	}
}

// getOrCreate returns the buffer for (runID, turnIndex), creating it if it
// does not exist. MUST be called with mu held.
func (c *MemStreamConsumer) getOrCreate(runID string, turnIndex int) *MemStreamBuffer {
	key := bufferKey(runID, turnIndex)
	buf, ok := c.buffers[key]
	if !ok {
		buf = &MemStreamBuffer{
			RunID:       runID,
			TurnIndex:   turnIndex,
			EventCounts: make(map[string]int),
		}
		c.buffers[key] = buf
	}
	return buf
}

// OnMessageStart fires when the LLM stream emits its EventMessageStart frame,
// signaling the beginning of a new assistant message for the given Turn. The
// corresponding MemStreamBuffer is created (or reset) and "message_start" is
// counted. See 22-Agent-Loop规格.md §7.1.
func (c *MemStreamConsumer) OnMessageStart(_ context.Context, run *Run, turn *Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.getOrCreate(run.ID, turn.Index)
	buf.EventCounts["message_start"]++
}

// OnContentDelta fires for each incremental text chunk arriving on the LLM
// stream. The fragment is appended to MemStreamBuffer.Content and
// "content_delta" is counted. See 22-Agent-Loop规格.md §7.2.
func (c *MemStreamConsumer) OnContentDelta(_ context.Context, run *Run, turn *Turn, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.getOrCreate(run.ID, turn.Index)
	buf.Content.WriteString(text)
	buf.EventCounts["content_delta"]++
}

// OnToolCallDelta fires when the LLM stream emits a partial tool_use block.
// If toolName is non-empty it is used to create a new MemToolCallFrame;
// subsequent deltas with the same empty toolName are accumulated into the
// most recently opened frame. "tool_call_delta" is counted.
//
// See 22-Agent-Loop规格.md §7.3.
func (c *MemStreamConsumer) OnToolCallDelta(_ context.Context, run *Run, turn *Turn, toolName string, argsPartial string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.getOrCreate(run.ID, turn.Index)
	// A non-empty toolName signals the start of a new tool call frame.
	if toolName != "" || len(buf.ToolCalls) == 0 {
		buf.ToolCalls = append(buf.ToolCalls, MemToolCallFrame{ToolName: toolName})
	}
	// Accumulate args into the most recent frame.
	last := len(buf.ToolCalls) - 1
	buf.ToolCalls[last].ArgsPartial.WriteString(argsPartial)
	buf.EventCounts["tool_call_delta"]++
}

// OnMessageDelta fires for EventMessageDelta frames carrying message-level
// metadata. The raw JSON payload is appended to MemStreamBuffer.MessageDeltas
// and "message_delta" is counted. See 22-Agent-Loop规格.md §7.4.
func (c *MemStreamConsumer) OnMessageDelta(_ context.Context, run *Run, turn *Turn, delta json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.getOrCreate(run.ID, turn.Index)
	// Deep-copy the slice to avoid aliasing.
	copied := make(json.RawMessage, len(delta))
	copy(copied, delta)
	buf.MessageDeltas = append(buf.MessageDeltas, copied)
	buf.EventCounts["message_delta"]++
}

// OnMessageEnd fires once the LLM stream has emitted its EventMessageEnd
// frame. usage is stored in MemStreamBuffer.FinalUsage and Finished is set
// to true. "message_end" is counted. After this call the buffer is sealed;
// further events for the same (runID, turnIndex) are a protocol violation but
// are handled gracefully (they are still counted). See 22-Agent-Loop规格.md §7.5.
func (c *MemStreamConsumer) OnMessageEnd(_ context.Context, run *Run, turn *Turn, usage llm.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.getOrCreate(run.ID, turn.Index)
	buf.FinalUsage = usage
	buf.Finished = true
	buf.EventCounts["message_end"]++
}

// Snapshot returns a shallow copy of the MemStreamBuffer for (runID,
// turnIndex). The Content and ArgsPartial fields are snapshotted as plain
// strings, so the returned value is safe to inspect after the lock is
// released. Returns (nil, false) when no buffer exists for the given pair.
//
// See 22-Agent-Loop规格.md §7 (testing aid).
func (c *MemStreamConsumer) Snapshot(runID string, turnIndex int) (*MemStreamBuffer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bufferKey(runID, turnIndex)
	orig, ok := c.buffers[key]
	if !ok {
		return nil, false
	}
	// Shallow-copy primitive fields; deep-copy slices and maps.
	snap := &MemStreamBuffer{
		RunID:      orig.RunID,
		TurnIndex:  orig.TurnIndex,
		FinalUsage: orig.FinalUsage,
		Finished:   orig.Finished,
	}
	snap.Content.WriteString(orig.Content.String())
	if len(orig.MessageDeltas) > 0 {
		snap.MessageDeltas = make([]json.RawMessage, len(orig.MessageDeltas))
		for i, d := range orig.MessageDeltas {
			cp := make(json.RawMessage, len(d))
			copy(cp, d)
			snap.MessageDeltas[i] = cp
		}
	}
	if len(orig.ToolCalls) > 0 {
		snap.ToolCalls = make([]MemToolCallFrame, len(orig.ToolCalls))
		for i, tc := range orig.ToolCalls {
			snap.ToolCalls[i].ToolName = tc.ToolName
			snap.ToolCalls[i].ArgsPartial.WriteString(tc.ArgsPartial.String())
		}
	}
	snap.EventCounts = make(map[string]int, len(orig.EventCounts))
	for k, v := range orig.EventCounts {
		snap.EventCounts[k] = v
	}
	return snap, true
}
