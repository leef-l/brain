//go:build !linux && !windows && !darwin

package tool

import (
	"context"
	"fmt"
	"io"
)

// fallbackSandbox is a no-op sandbox for unsupported platforms.
// Always reports as unavailable — commands are refused, not run bare.
type fallbackSandbox struct {
	sb  *Sandbox
	cfg *SandboxConfig
}

func newPlatformSandbox(sb *Sandbox, cfg *SandboxConfig) CommandSandbox {
	return &fallbackSandbox{sb: sb, cfg: cfg}
}

func (f *fallbackSandbox) Available() bool { return false }

func (f *fallbackSandbox) Run(_ context.Context, _ string, _ string,
	_, _ io.Writer) (int, error) {
	return -1, fmt.Errorf("OS-level sandbox not available on this platform")
}

func shellName() string { return "sh" }
func shellFlag() string { return "-c" }
