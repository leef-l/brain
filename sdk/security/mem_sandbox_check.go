package security

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// cleanPath normalises a filesystem path before policy matching.
// It resolves . and .. components so traversal patterns like
// "/allowed/../etc/shadow" cannot bypass prefix-based checks.
func cleanPath(path string) string {
	return filepath.Clean(path)
}

// SandboxChecker is a thin decision engine on top of a Sandbox's four
// policy views. Concrete sandbox backends (runc, gVisor, seccomp) are
// responsible for enforcement; SandboxChecker exposes a uniform in-process
// "would this action be denied?" surface used by the Kernel's guardrail
// pipeline, the loop.ToolResultSanitizer, and the compliance tests in 25
// §6. See 23-安全模型.md §3 for the normative policy matrix.
//
// Every Check* method returns a BrainError carrying CodeToolSandboxDenied
// on rejection so downstream audit code can route the event to the same
// telemetry path as real runtime denials.
type SandboxChecker struct {
	sandbox Sandbox
}

// NewSandboxChecker wraps a Sandbox in a checker. sb MUST NOT be nil; the
// checker treats a nil sandbox as a programming error per 23 §3.1.
func NewSandboxChecker(sb Sandbox) *SandboxChecker {
	if sb == nil {
		panic("security.NewSandboxChecker: sandbox is required")
	}
	return &SandboxChecker{sandbox: sb}
}

// CheckRead validates that path is permitted by the FSPolicy's ReadAllowed
// list and not blocked by Denied. See 23-安全模型.md §3.2.
func (c *SandboxChecker) CheckRead(path string) error {
	path = cleanPath(path)
	pol := c.sandbox.FS()
	if matchesAny(pol.Denied, path) {
		return denyf("fs.read: path %q is on the deny list", path)
	}
	if !matchesAny(pol.ReadAllowed, path) {
		return denyf("fs.read: path %q is not on the read allow list", path)
	}
	return nil
}

// CheckWrite validates that path is permitted by the FSPolicy's
// WriteAllowed list and not blocked by Denied. See 23-安全模型.md §3.2.
func (c *SandboxChecker) CheckWrite(path string) error {
	path = cleanPath(path)
	pol := c.sandbox.FS()
	if matchesAny(pol.Denied, path) {
		return denyf("fs.write: path %q is on the deny list", path)
	}
	if !matchesAny(pol.WriteAllowed, path) {
		return denyf("fs.write: path %q is not on the write allow list", path)
	}
	return nil
}

// CheckDial validates that an outbound TCP/UDP connection to (host, port)
// is allowed by the NetPolicy. See 23-安全模型.md §3.3.
func (c *SandboxChecker) CheckDial(host string, port int) error {
	pol := c.sandbox.Net()
	if matchesAny(pol.DeniedHosts, host) {
		return denyf("net.dial: host %q is on the deny list", host)
	}
	if len(pol.AllowedHosts) > 0 && !matchesAny(pol.AllowedHosts, host) {
		return denyf("net.dial: host %q is not on the allow list", host)
	}
	if len(pol.AllowedPorts) > 0 {
		allowed := false
		for _, p := range pol.AllowedPorts {
			if p == port {
				allowed = true
				break
			}
		}
		if !allowed {
			return denyf("net.dial: port %d is not on the allow list", port)
		}
	}
	return nil
}

// CheckExec validates that binaryBasename may be exec'd under the
// ProcPolicy. See 23-安全模型.md §3.4.
func (c *SandboxChecker) CheckExec(binaryBasename string) error {
	pol := c.sandbox.Proc()
	if binaryBasename == "" {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("proc.exec: binary basename is empty"))
	}
	if len(pol.AllowedExe) == 0 {
		return denyf("proc.exec: no allowed binaries configured")
	}
	for _, allowed := range pol.AllowedExe {
		if allowed == binaryBasename {
			return nil
		}
	}
	return denyf("proc.exec: binary %q is not on the allow list", binaryBasename)
}

// CheckProcCount validates that the running number of sandboxed processes
// has not reached MaxProcs. See 23-安全模型.md §3.4.
func (c *SandboxChecker) CheckProcCount(running int) error {
	pol := c.sandbox.Proc()
	if pol.MaxProcs > 0 && running >= pol.MaxProcs {
		return denyf("proc.count: running=%d max=%d", running, pol.MaxProcs)
	}
	return nil
}

// CheckMemory validates that usedMB remains within SysPolicy.MaxMemoryMB.
// See 23-安全模型.md §3.5.
func (c *SandboxChecker) CheckMemory(usedMB int) error {
	pol := c.sandbox.Sys()
	if pol.MaxMemoryMB > 0 && usedMB > pol.MaxMemoryMB {
		return denyf("sys.memory: used=%dMB max=%dMB", usedMB, pol.MaxMemoryMB)
	}
	return nil
}

// CheckCPU validates that used CPU time remains within SysPolicy.MaxCPUTime.
// See 23-安全模型.md §3.5.
func (c *SandboxChecker) CheckCPU(used time.Duration) error {
	pol := c.sandbox.Sys()
	if pol.MaxCPUTime > 0 && used > pol.MaxCPUTime {
		return denyf("sys.cpu: used=%s max=%s", used, pol.MaxCPUTime)
	}
	return nil
}

// matchesAny reports whether s is prefix-matched by any entry in list.
// Exact equality and path-prefix equality are both accepted.
func matchesAny(list []string, s string) bool {
	for _, p := range list {
		if p == "" {
			continue
		}
		if p == s {
			return true
		}
		if strings.HasSuffix(p, "/") && strings.HasPrefix(s, p) {
			return true
		}
		if !strings.HasSuffix(p, "/") && strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

func denyf(format string, args ...interface{}) error {
	return brainerrors.New(brainerrors.CodeToolSandboxDenied,
		brainerrors.WithMessage(fmt.Sprintf(format, args...)))
}
