package loop

import (
	"context"
	"encoding/json"
)

// ToolObserver receives tool execution lifecycle callbacks from Runner in
// near-real-time so terminal UIs can surface what the agent is doing.
type ToolObserver interface {
	OnToolStart(ctx context.Context, run *Run, turn *Turn, toolName string, input json.RawMessage)
	OnToolEnd(ctx context.Context, run *Run, turn *Turn, toolName string, ok bool, output json.RawMessage)
}
