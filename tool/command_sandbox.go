package tool

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

// CommandSandbox provides OS-level process isolation for shell commands.
// On Linux this is implemented via bubblewrap (bwrap); on macOS via
// sandbox-exec (Seatbelt); on Windows via Job Objects.
//
// Design mirrors Claude Code:
//   - Only Bash/shell commands are sandboxed (not Read/Write/Search tools)
//   - Filesystem: writable dirs are explicitly listed; system dirs read-only
//   - Network: isolated by default (Linux/macOS only)
//   - If sandbox is unavailable, commands are refused (not silently run bare)
type CommandSandbox interface {
	// Available reports whether the sandbox backend is functional.
	Available() bool

	// Run executes `command` inside the sandbox and returns the result.
	// stdout and stderr receive the command's output. The sandbox
	// handles the full lifecycle (process creation, isolation, cleanup).
	// This design lets platforms like Windows hook between Start and Wait.
	Run(ctx context.Context, command string, workDir string,
		stdout, stderr io.Writer) (exitCode int, err error)
}

// SandboxConfig controls the sandbox behavior. Loaded from config.json.
type SandboxConfig struct {
	Enabled    bool     `json:"enabled"`
	AllowWrite []string `json:"allow_write"` // extra writable dirs
	DenyRead   []string `json:"deny_read"`   // dirs hidden from sandbox
	AllowNet   []string `json:"allow_net"`   // allowed network domains (empty = no net)

	// FailIfUnavailable: if true, refuse to run commands when sandbox
	// backend is not available. If false, fall back to bare execution.
	FailIfUnavailable bool `json:"fail_if_unavailable"`
}

// NewCommandSandbox creates the platform-appropriate sandbox implementation.
// Returns nil if sandbox is disabled or Sandbox is nil.
func NewCommandSandbox(sb *Sandbox, cfg *SandboxConfig) CommandSandbox {
	if sb == nil {
		return nil
	}
	if cfg != nil && !cfg.Enabled {
		return nil
	}
	return newPlatformSandbox(sb, cfg)
}

// --- shared helpers ---

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// exitCodeFromErr extracts the exit code from an exec error.
// Returns 0 if err is nil, -1 if it's not an ExitError.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// cachedLookPath checks once whether a binary is in PATH.
var lookPathCache sync.Map // string → bool

func cachedHasBinary(name string) bool {
	if v, ok := lookPathCache.Load(name); ok {
		return v.(bool)
	}
	_, err := exec.LookPath(name)
	found := err == nil
	lookPathCache.Store(name, found)
	return found
}
