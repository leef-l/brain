//go:build darwin

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

// darwinSandbox implements CommandSandbox on macOS using sandbox-exec (Seatbelt).
// Mirrors Claude Code's macOS sandbox:
//   - Filesystem: deny-default; allow reads on system dirs and home;
//     allow writes only to sandbox dirs + /tmp
//   - Network: deny all
//   - Process: allow fork/exec for shell tools
//
// sandbox-exec is deprecated by Apple but still works on all macOS versions.
// Claude Code uses the same approach.
type darwinSandbox struct {
	sb  *Sandbox
	cfg *SandboxConfig
}

func newPlatformSandbox(sb *Sandbox, cfg *SandboxConfig) CommandSandbox {
	return &darwinSandbox{sb: sb, cfg: cfg}
}

func (d *darwinSandbox) Available() bool {
	return cachedHasBinary("sandbox-exec")
}

func (d *darwinSandbox) Run(ctx context.Context, command string, workDir string,
	stdout, stderr io.Writer) (int, error) {

	if !d.Available() {
		return -1, fmt.Errorf("sandbox-exec not found in PATH")
	}

	profile := d.buildProfile()
	cmd := exec.CommandContext(ctx, "sandbox-exec", "-p", profile, "sh", "-c", command)

	if workDir != "" {
		cmd.Dir = workDir
	} else if d.sb != nil {
		cmd.Dir = d.sb.Primary()
	}

	cmd.Env = d.sandboxEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	return exitCodeFromErr(err), nil
}

// buildProfile generates a Seatbelt profile.
//
// Rule order in Seatbelt: the LAST matching rule wins.
// Structure:
//  1. (deny default) — block everything
//  2. (allow ...) — open up what's needed
//  3. (deny ...) — explicit deny-read overrides at the end
func (d *darwinSandbox) buildProfile() string {
	var b strings.Builder

	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n\n")

	// Process operations.
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow signal)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach*)\n\n")

	// System directories: read-only.
	for _, dir := range []string{
		"/usr", "/bin", "/sbin", "/Library",
		"/System", "/Applications", "/opt", "/etc",
		"/dev", "/private/tmp", "/private/var", "/var",
	} {
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", dir))
	}
	b.WriteString("\n")

	// Temp: read + write.
	b.WriteString("(allow file-write* (subpath \"/tmp\"))\n")
	b.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	if tmpdir := os.Getenv("TMPDIR"); tmpdir != "" {
		if real, err := filepath.EvalSymlinks(tmpdir); err == nil {
			b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", real))
			b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", real))
		}
	}
	b.WriteString("\n")

	// Home: read-only.
	home, _ := os.UserHomeDir()
	if home != "" {
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", home))
		b.WriteString("\n")
	}

	// Sandbox allowed dirs: read + write.
	for _, dir := range d.sb.Allowed() {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", real))
		b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", real))
		if real != dir {
			b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", dir))
			b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", dir))
		}
	}
	b.WriteString("\n")

	// Extra writable dirs from config.
	if d.cfg != nil {
		for _, extra := range d.cfg.AllowWrite {
			expanded := expandHomeDarwin(extra)
			if real, err := filepath.EvalSymlinks(expanded); err == nil {
				expanded = real
			}
			b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", expanded))
			b.WriteString(fmt.Sprintf("(allow file-write* (subpath \"%s\"))\n", expanded))
		}
	}

	// Go toolchain: read-only.
	for _, key := range []string{"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE"} {
		if v := os.Getenv(key); v != "" {
			if real, err := filepath.EvalSymlinks(v); err == nil {
				v = real
			}
			b.WriteString(fmt.Sprintf("(allow file-read* (subpath \"%s\"))\n", v))
		}
	}
	b.WriteString("\n")

	// Deny-read overrides (LAST so they win over allows).
	if d.cfg != nil {
		for _, deny := range d.cfg.DenyRead {
			expanded := expandHomeDarwin(deny)
			b.WriteString(fmt.Sprintf("(deny file-read* (subpath \"%s\"))\n", expanded))
			b.WriteString(fmt.Sprintf("(deny file-write* (subpath \"%s\"))\n", expanded))
		}
	}

	// Network: deny all.
	b.WriteString("\n(deny network*)\n")

	return b.String()
}

func (d *darwinSandbox) sandboxEnv() []string {
	var env []string
	for _, key := range []string{
		"PATH", "HOME", "USER", "SHELL", "TERM", "LANG", "LC_ALL",
		"TMPDIR",
		"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE",
		"DEVELOPER_DIR",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func expandHomeDarwin(path string) string {
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
