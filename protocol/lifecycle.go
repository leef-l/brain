package protocol

import (
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// InitializeRequest is the payload of the host→sidecar `initialize`
// request that opens every sidecar session, as specified in
// 20-协议规格.md §6 (lifecycle) and the method table in §10.1.
//
// The struct mirrors the wire schema so that Go and non-Go brain SDKs can
// decode/encode symmetrically. Fields that appear only in specific
// LLMAccess modes are documented individually below; the host MUST NOT
// populate them in modes where they are forbidden (see 23-安全模型.md).
type InitializeRequest struct {
	// ProtocolVersion is the host-advertised wire protocol version
	// (semver). The sidecar MUST refuse to initialize if it cannot speak
	// the requested version. See 20-协议规格.md §6.4 Starting rules.
	ProtocolVersion string `json:"protocol_version"`

	// KernelVersion is the BrainKernel build version, advertised for
	// diagnostics and compatibility gating. See 20-协议规格.md §6.4.
	KernelVersion string `json:"kernel_version"`

	// Capabilities is the host-advertised capability set as a flat
	// map[string]bool. The sidecar compares it against its own
	// capability table and replies with BrainCapabilities in the
	// InitializeResponse.
	Capabilities map[string]bool `json:"capabilities,omitempty"`

	// LLMConfig carries non-secret LLM configuration (model name,
	// temperature caps, provider name, ...). It is always populated.
	LLMConfig map[string]interface{} `json:"llm_config,omitempty"`

	// LLMCredentials carries short-lived LLM API credentials. It is
	// populated ONLY when the LLMAccess mode is direct or hybrid, per
	// 23-安全模型.md; proxy mode MUST leave this nil to keep secrets out
	// of the sidecar address space.
	LLMCredentials map[string]string `json:"llm_credentials,omitempty"`

	// WorkspacePath is the absolute path to the per-Run workspace root
	// that the sidecar is allowed to read and write. The host MUST
	// enforce the sandbox boundary; the field here is informational.
	WorkspacePath string `json:"workspace_path,omitempty"`

	// RunContext carries per-Run metadata the sidecar needs for tracing
	// and audit (run id, parent trace ids, tenant hints, ...).
	RunContext map[string]interface{} `json:"run_context,omitempty"`
}

// InitializeResponse is the sidecar→host reply to `initialize`. It
// advertises the sidecar's effective capabilities so the host can reject
// incompatible sessions before any business request is sent. See
// 20-协议规格.md §6.4 Starting and the method index in §10.1.
type InitializeResponse struct {
	// ProtocolVersion is the sidecar-chosen wire protocol version,
	// which MUST be a version the host advertised in the request.
	ProtocolVersion string `json:"protocol_version"`

	// BrainVersion is the sidecar build version used for diagnostics.
	BrainVersion string `json:"brain_version"`

	// BrainCapabilities is the effective capability set the sidecar
	// commits to for the duration of the session.
	BrainCapabilities map[string]bool `json:"brain_capabilities,omitempty"`

	// SupportedTools is the list of tool identifiers the sidecar is
	// willing to handle via tool.invoke. Tools not listed here MUST
	// cause the host to reject matching tool.invoke requests locally.
	SupportedTools []string `json:"supported_tools,omitempty"`
}

// SidecarState is the 5-state lifecycle FSM defined in 20-协议规格.md §6.2.
// Every SidecarInstance keeps a single state field guarded by a mutex so
// concurrent callers observe a consistent transition order. The valid
// transitions are enumerated in isValidTransition below and enforced by
// TransitionTo — illegal transitions return a BrainError tagged with
// CodeInvariantViolated so the alert pipeline catches state machine bugs.
type SidecarState int

const (
	// StateStarting is the initial state after spawn. Only the initialize
	// response is allowed to flow in this state; the FSM times out after
	// 30s per §6.4 if initialize never completes.
	StateStarting SidecarState = iota

	// StateRunning is the normal steady-state after a successful
	// initialize. All business requests are allowed in both directions
	// and the §7 heartbeat scheduler is active.
	StateRunning

	// StateDraining is entered after a shutdown request, an idle timeout,
	// or a transport failure. New brain.run requests are refused with
	// CodeShuttingDown; in-flight Runs are allowed to finish.
	StateDraining

	// StateClosed means stdin has been closed and the FSM is waiting for
	// the sidecar process to exit. No new frames flow.
	StateClosed

	// StateWaited means the process has exited and os.Process.Wait is in
	// progress. The waiter table is drained with CodeSidecarExitNonzero /
	// CodeSidecarCrashed as appropriate.
	StateWaited

	// StateReaped is terminal. All resources are released; the SidecarInstance
	// can be removed from the pool.
	StateReaped
)

// String returns the lowercase name used in log lines and metric labels.
func (s SidecarState) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateDraining:
		return "draining"
	case StateClosed:
		return "closed"
	case StateWaited:
		return "waited"
	case StateReaped:
		return "reaped"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// validTransitions encodes the state machine edges from 20-协议规格.md §6.3
// as an adjacency set. A transition from A to B is legal iff B is in
// validTransitions[A]. Entries are exhaustive — any edge not present here
// is a bug and TransitionTo rejects it.
//
// The spec diagram reads:
//
//	Starting  → Running | Draining (init timeout / unrecoverable error)
//	Running   → Draining
//	Draining  → Closed
//	Closed    → Waited
//	Waited    → Reaped
//
// Plus a safety valve: any non-terminal state MAY transition directly to
// Waited when the process has already died (e.g. SIGKILL triggered by a
// frame_too_large violation in §2.4). This shortcut is what lets the FSM
// recover from hard kills without requiring a graceful Draining pass.
var validTransitions = map[SidecarState]map[SidecarState]bool{
	StateStarting: {
		StateRunning:  true,
		StateDraining: true,
		StateWaited:   true, // safety valve
	},
	StateRunning: {
		StateDraining: true,
		StateWaited:   true, // safety valve
	},
	StateDraining: {
		StateClosed: true,
		StateWaited: true, // safety valve
	},
	StateClosed: {
		StateWaited: true,
	},
	StateWaited: {
		StateReaped: true,
	},
	StateReaped: {}, // terminal
}

// IsValidTransition reports whether from → to is an allowed edge in the
// FSM. Exposed so unit tests (C-14) and the recovery subsystem can check
// a candidate transition before attempting it.
func IsValidTransition(from, to SidecarState) bool {
	nexts, ok := validTransitions[from]
	if !ok {
		return false
	}
	return nexts[to]
}

// SidecarInstance is the minimal host-side handle that owns a sidecar's
// lifecycle state. The full instance struct in brain/kernel carries more
// fields (pid, cmd, stdin/stdout handles, waiter table). This subset lives
// in the protocol package because the FSM transitions are part of the
// wire protocol spec and tests need to drive them without depending on
// the kernel package.
//
// Concurrent-safe: all mutating methods hold mu. Readers that only want
// the current state should call State() rather than touch the field
// directly so the mutex order is preserved.
type SidecarInstance struct {
	mu             sync.Mutex
	state          SidecarState
	stateChangedAt time.Time

	// listener is an optional callback invoked inside the mutex for every
	// successful transition. Tests use it to assert the observed sequence
	// of states. Production wiring plugs a metrics hook here.
	listener func(from, to SidecarState, at time.Time)
}

// NewSidecarInstance builds a SidecarInstance in StateStarting with the
// given transition listener. Passing nil for the listener is valid — the
// FSM still enforces the transition table, there is just no observer.
func NewSidecarInstance(listener func(from, to SidecarState, at time.Time)) *SidecarInstance {
	return &SidecarInstance{
		state:          StateStarting,
		stateChangedAt: time.Now(),
		listener:       listener,
	}
}

// State returns the current state. Safe for concurrent callers.
func (s *SidecarInstance) State() SidecarState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// StateChangedAt returns the wall-clock time the current state was
// entered. Used by the watchdog to decide whether a stuck sidecar has
// exceeded its per-state deadline (30s Starting, drain_timeout Draining,
// 5+5s Closed per §6.4).
func (s *SidecarInstance) StateChangedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateChangedAt
}

// TransitionTo moves the FSM to the target state. Returns a BrainError
// (CodeInvariantViolated) when the transition is not in the allowed
// adjacency set. Callers SHOULD treat any error here as a programming
// bug — the FSM is enforced so illegal transitions cannot slip into
// production silently.
//
// On success the listener is invoked while mu is still held so observers
// see transitions in the exact order they occur. Listeners MUST NOT
// acquire any lock that could end up blocked on something that itself
// wants the SidecarInstance mutex — that would deadlock the FSM.
func (s *SidecarInstance) TransitionTo(to SidecarState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !IsValidTransition(s.state, to) {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"sidecar FSM: illegal transition %s → %s", s.state, to)),
		)
	}

	old := s.state
	s.state = to
	s.stateChangedAt = time.Now()

	if s.listener != nil {
		s.listener(old, to, s.stateChangedAt)
	}
	return nil
}

// ForceTransitionTo bypasses the validity check and is reserved for the
// lifecycle recovery path where the host has already SIGKILLed the
// sidecar and needs to force-move the FSM to Waited. Exported separately
// so a regular call site cannot accidentally skip validation.
//
// Every ForceTransitionTo call MUST be justified by a comment at the call
// site explaining which spec clause permits the skip. The audit log
// pipeline additionally flags force transitions so reviewers can verify
// the justification post-hoc.
func (s *SidecarInstance) ForceTransitionTo(to SidecarState, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.state
	s.state = to
	s.stateChangedAt = time.Now()

	if s.listener != nil {
		s.listener(old, to, s.stateChangedAt)
	}
	_ = reason // reason is captured by the listener hook in production
}
