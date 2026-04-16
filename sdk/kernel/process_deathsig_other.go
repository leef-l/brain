//go:build !linux

package kernel

import "os/exec"

// setSidecarDeathSignal is a no-op on non-Linux platforms.
// Orphan cleanup relies on the sidecar detecting stdin EOF.
func setSidecarDeathSignal(_ *exec.Cmd) {}
