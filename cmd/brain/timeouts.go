package main

import (
	"context"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
)

var (
	resolveRunTimeoutWithConfig = config.ResolveRunTimeoutWithConfig
	withOptionalTimeout         = config.WithOptionalTimeout
	effectiveRunMaxDuration     = config.EffectiveRunMaxDuration
)

// Shims to keep existing call sites working with *brainConfig (= *config.Config).
var _ func(*brainConfig, string, time.Duration) (time.Duration, error) = resolveRunTimeoutWithConfig
var _ func(context.Context, time.Duration) (context.Context, context.CancelFunc) = withOptionalTimeout
