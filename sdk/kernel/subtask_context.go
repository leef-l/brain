package kernel

import (
	"context"

	"github.com/leef-l/brain/sdk/protocol"
)

type subtaskContextKey struct{}

// WithSubtaskContext attaches delegation intent to ctx so tools like
// central.delegate can propagate immutable caller intent without asking the LLM
// to preserve it in rewritten instructions.
func WithSubtaskContext(ctx context.Context, subtask *protocol.SubtaskContext) context.Context {
	if subtask == nil {
		return ctx
	}
	cp := *subtask
	return context.WithValue(ctx, subtaskContextKey{}, &cp)
}

// SubtaskContextFromContext returns a defensive copy of the attached
// SubtaskContext, if any.
func SubtaskContextFromContext(ctx context.Context) *protocol.SubtaskContext {
	if ctx == nil {
		return nil
	}
	subtask, _ := ctx.Value(subtaskContextKey{}).(*protocol.SubtaskContext)
	if subtask == nil {
		return nil
	}
	cp := *subtask
	return &cp
}
