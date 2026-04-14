package braintesting

import (
	"context"
	"errors"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// wrapCtxErr converts a raw ctx.Err() into a typed BrainError so every
// helper in this package surfaces a consistent shape regardless of
// whether the caller cancelled the test or hit a deadline.
func wrapCtxErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("braintesting: context canceled"),
		)
	case errors.Is(err, context.DeadlineExceeded):
		return brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("braintesting: context deadline exceeded"),
		)
	default:
		return err
	}
}
