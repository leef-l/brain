package loop

import (
	"context"
	"errors"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// wrapLoopCtxErr converts a raw ctx.Err() from the loop package into a
// typed BrainError so callers see a consistent shape regardless of
// whether the Run was cancelled or timed out. The mapping follows the
// 21-错误模型.md guidance:
//
//   - context.Canceled      → CodeShuttingDown (transient, retryable)
//   - context.DeadlineExceeded → CodeDeadlineExceeded (transient, retryable)
//   - any other error       → passthrough (already typed)
//
// Defined once in the loop package so every sub-implementation
// (budget / detector / sanitizer / stream) reports ctx errors the same
// way without a helper import cycle.
func wrapLoopCtxErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("loop: context canceled"),
		)
	case errors.Is(err, context.DeadlineExceeded):
		return brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("loop: context deadline exceeded"),
		)
	default:
		return err
	}
}
