package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func resolveRunTimeoutWithConfig(cfg *brainConfig, explicit string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(explicit)
	if raw == "" && cfg != nil {
		raw = strings.TrimSpace(cfg.Timeout)
	}
	if raw == "" {
		return fallback, nil
	}
	if raw == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid timeout %q: must be >= 0", raw)
	}
	return d, nil
}

func withOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func effectiveRunMaxDuration(timeout time.Duration, fallback time.Duration) time.Duration {
	if timeout < 0 {
		return fallback
	}
	return timeout
}
