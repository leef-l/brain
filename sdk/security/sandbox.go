package security

import "time"

// Sandbox is the four-dimensional isolation surface defined in
// 23-安全模型.md §3.1. Every brain sidecar and every tool execution MUST run
// under a Sandbox whose policies are evaluated before any side effect
// reaches the host system.
//
// The four dimensions — FS, Net, Proc, Sys — correspond to the rows of the
// matrix in 23 §3.1; see §§3.2–3.5 for the normative default policies, and
// §3.6 for the four implementation levels (L0 none → L1 seccomp → L2 Docker
// → L3 gVisor/Firecracker). The Sandbox interface itself is
// implementation-agnostic: wave-2 packages will plug in concrete backends
// (runc, gVisor, ...), but every backend MUST expose these four policy
// views so that guardrails and audit sinks see a uniform shape.
type Sandbox interface {
	// FS returns the filesystem access policy enforced for this sandbox.
	// See 23 §3.2.
	FS() FSPolicy

	// Net returns the network egress policy enforced for this sandbox.
	// See 23 §3.3.
	Net() NetPolicy

	// Proc returns the process / binary execution policy enforced for
	// this sandbox. See 23 §3.4.
	Proc() ProcPolicy

	// Sys returns the system-call policy (seccomp filter shape) enforced
	// for this sandbox. See 23 §3.5.
	Sys() SysPolicy
}

// FSPolicy describes the filesystem allow / deny lists defined in
// 23-安全模型.md §3.2. Paths are absolute host paths. Inside the worktree
// defined by the Run, reads and writes are normally unrestricted; outside
// of it, access is denied by default and only the paths explicitly listed
// below may be traversed.
type FSPolicy struct {
	// ReadAllowed is the list of host paths the sandbox may read from
	// outside of its primary worktree. See 23 §3.2.
	ReadAllowed []string

	// WriteAllowed is the list of host paths the sandbox may write to
	// outside of its primary worktree. See 23 §3.2.
	WriteAllowed []string

	// Denied is the list of host paths that MUST always be rejected even
	// if they happen to match a broader allow entry (e.g. ~/.ssh,
	// /proc/self/environ, cloud metadata bind mounts). See 23 §3.2.
	Denied []string
}

// NetPolicy describes the network egress allow / deny lists defined in
// 23-安全模型.md §3.3. The default is an empty allow list — every outbound
// connection MUST be justified by a matching entry here. Metadata service
// IPs, loopback, and RFC1918 networks MUST stay denied unless the policy
// explicitly exempts them (23 §3.3).
type NetPolicy struct {
	// AllowedHosts is the explicit DNS / host whitelist the sandbox may
	// connect to. See 23 §3.3.
	AllowedHosts []string

	// DeniedHosts is the explicit host blacklist that MUST be rejected
	// even if a broader allow rule would otherwise grant access. See
	// 23 §3.3 for the mandatory baseline entries.
	DeniedHosts []string

	// AllowedPorts is the list of TCP / UDP ports the sandbox may open.
	// An empty list means "whatever the host resolves to" under AllowedHosts.
	AllowedPorts []int
}

// ProcPolicy describes the allowed-binary and process-count policy defined
// in 23-安全模型.md §3.4. Every brain MUST declare its allowed_binaries up
// front; the Runner enforces the list via seccomp / ptrace / runtime
// wrappers.
type ProcPolicy struct {
	// MaxProcs caps the number of concurrent child processes the sandbox
	// may spawn. See 23 §3.4.
	MaxProcs int

	// AllowedExe is the whitelist of executable basenames (git, node,
	// python, pytest, ...) that the sandbox may exec. Entries outside of
	// this list MUST be rejected and audited. See 23 §3.4.
	AllowedExe []string
}

// SysPolicy describes the system-call / resource policy defined in
// 23-安全模型.md §3.5. It is the shape a concrete backend (seccomp,
// AppArmor, gVisor) translates into its own filter language.
type SysPolicy struct {
	// MaxMemoryMB caps the resident memory available to the sandbox, in
	// megabytes. See 23 §3.5 and 附录 B.
	MaxMemoryMB int

	// MaxCPUTime caps the wall-clock CPU time the sandbox may consume
	// before the Runner terminates it. See 23 §3.5 and 附录 B.
	MaxCPUTime time.Duration
}
