//go:build linux

package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// bwrapSandbox implements CommandSandbox using bubblewrap on Linux.
// Mirrors Claude Code's Linux sandbox:
//   - System dirs (/usr, /bin, /lib*, /etc, /opt, /sbin): read-only
//   - /dev, /proc: properly mounted
//   - /tmp: private tmpfs (isolated from host)
//   - Sandbox allowed dirs: read-write
//   - User home: read-only (for .gitconfig, .ssh, etc.)
//   - DenyRead dirs: not mounted at all
//   - Network: fully isolated (--unshare-net)
//   - PID namespace: isolated (--unshare-pid)
type bwrapSandbox struct {
	sb  *Sandbox
	cfg *SandboxConfig
}

func newPlatformSandbox(sb *Sandbox, cfg *SandboxConfig) CommandSandbox {
	return &bwrapSandbox{sb: sb, cfg: cfg}
}

func (b *bwrapSandbox) Available() bool {
	return cachedHasBinary("bwrap")
}

func (b *bwrapSandbox) Run(ctx context.Context, command string, workDir string,
	stdout, stderr io.Writer) (int, error) {

	if !b.Available() {
		return -1, fmt.Errorf("bubblewrap (bwrap) not found in PATH")
	}

	args := b.buildArgs(command, workDir)
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	return exitCodeFromErr(err), nil
}

func (b *bwrapSandbox) buildArgs(command, workDir string) []string {
	denied := b.denySet()
	var args []string

	// System directories: read-only bind.
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/etc"} {
		if dirExists(dir) && !denied[dir] {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	for _, lib := range []string{"/lib", "/lib64", "/lib32"} {
		if dirExists(lib) && !denied[lib] {
			args = append(args, "--ro-bind", lib, lib)
		}
	}
	if dirExists("/opt") && !denied["/opt"] {
		args = append(args, "--ro-bind", "/opt", "/opt")
	}

	// Devices and proc.
	args = append(args, "--dev", "/dev", "--proc", "/proc")

	// Private /tmp (isolated from host).
	args = append(args, "--tmpfs", "/tmp")

	// Sandbox writable directories (project dirs).
	for _, dir := range b.sb.Allowed() {
		if !dirExists(dir) {
			continue
		}
		args = append(args, "--bind", dir, dir)
	}

	// Extra writable dirs from config.
	if b.cfg != nil {
		for _, extra := range b.cfg.AllowWrite {
			expanded := expandHome(extra)
			if dirExists(expanded) && !b.isAlreadyAllowed(expanded) {
				args = append(args, "--bind", expanded, expanded)
			}
		}
	}

	// User home: read-only (for .gitconfig, .ssh, .npmrc, etc.)
	home, _ := os.UserHomeDir()
	if home != "" && !denied[home] && !b.isAlreadyAllowed(home) {
		args = append(args, "--ro-bind", home, home)
	}

	// Environment variables.
	if home != "" {
		args = append(args, "--setenv", "HOME", home)
	}
	args = append(args, "--setenv", "PATH", os.Getenv("PATH"))
	if term := os.Getenv("TERM"); term != "" {
		args = append(args, "--setenv", "TERM", term)
	}
	if lang := os.Getenv("LANG"); lang != "" {
		args = append(args, "--setenv", "LANG", lang)
	}

	// Go toolchain — resolve symlinks before binding.
	for _, key := range []string{"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE"} {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		args = append(args, "--setenv", key, v)

		realV, err := filepath.EvalSymlinks(v)
		if err != nil {
			realV = v
		}
		// Only bind if not already covered by HOME or sandbox dirs.
		if dirExists(realV) && !b.isUnder(realV, home) && !b.isAlreadyAllowed(realV) && !denied[realV] {
			args = append(args, "--ro-bind", realV, realV)
		}
	}

	// Namespace isolation.
	args = append(args, "--unshare-net")     // No network
	args = append(args, "--unshare-pid")     // PID isolation
	args = append(args, "--die-with-parent") // Kill sandbox if brain exits

	// Working directory.
	if workDir != "" {
		args = append(args, "--chdir", workDir)
	}

	// The command.
	args = append(args, "sh", "-c", command)

	return args
}

func (b *bwrapSandbox) denySet() map[string]bool {
	m := make(map[string]bool)
	if b.cfg != nil {
		for _, d := range b.cfg.DenyRead {
			m[expandHome(d)] = true
		}
	}
	return m
}

func (b *bwrapSandbox) isUnder(path, parent string) bool {
	if parent == "" {
		return false
	}
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	return path == parent || strings.HasPrefix(path, parent+"/")
}

func (b *bwrapSandbox) isAlreadyAllowed(path string) bool {
	path = filepath.Clean(path)
	for _, dir := range b.sb.Allowed() {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		if home != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func shellName() string { return "sh" }
func shellFlag() string { return "-c" }
