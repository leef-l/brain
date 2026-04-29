// Package agent defines the BrainAgent abstraction from 02-BrainKernel设计.md §3.
//
// A BrainAgent is one role (central, code, browser, verifier, ...) that can
// participate in a Run. Agents are executed in sidecars (see 02 §12.5), and
// communicate with the Kernel via the stdio wire protocol (20).
package agent

import "context"

// Kind identifies a brain role. It's a string for extensibility — third-party
// brains can register their own kinds without modifying this package.
//
// The built-in kinds defined here correspond to the brains described in
// 02-BrainKernel设计.md §1 (decision list).
type Kind string

const (
	// KindCentral is the orchestrator brain that owns the plan and delegates
	// subtasks to specialist brains. See 02 §3.
	KindCentral Kind = "central"

	// KindCode is a specialist brain that writes and edits code.
	KindCode Kind = "code"

	// KindBrowser is a specialist brain that drives a headless browser.
	KindBrowser Kind = "browser"

	// KindVerifier is a specialist brain that runs tests and verifies work.
	KindVerifier Kind = "verifier"

	// KindFault is a specialist brain that injects faults for chaos testing.
	KindFault Kind = "fault"

	// KindData is a specialist brain that collects, stores, and computes
	// feature vectors from market data. See brains/data/.
	KindData Kind = "data"

	// KindQuant is a specialist brain that runs trading strategies, risk
	// management, and execution. See brains/quant/.
	KindQuant Kind = "quant"

	// KindDesktop is a specialist brain for OS-level automation outside the
	// browser: file management, window control, keyboard shortcuts. See
	// brains/desktop/.
	KindDesktop Kind = "desktop"

	// KindEasyMVP is a specialist brain for EasyMVP project contract execution:
	// architect_chat, plan_review, plan_compile, acceptance_mapping, etc.
	KindEasyMVP Kind = "easymvp"
)

// BuiltinKinds returns the set of built-in specialist brain kinds (excluding
// central). Third-party brains can register arbitrary Kind strings without
// appearing in this list.
func BuiltinKinds() []Kind {
	return []Kind{
		KindCode,
		KindBrowser,
		KindVerifier,
		KindFault,
		KindData,
		KindQuant,
		KindDesktop,
		KindEasyMVP,
	}
}

// LLMAccessMode captures the three LLM credential strategies defined in
// 02-BrainKernel设计.md §12.5.7 (decision 7). See 23-安全模型.md §5 for the
// full threat model.
type LLMAccessMode string

const (
	// LLMAccessProxied is the default: the sidecar never holds LLM credentials.
	// Every call round-trips through the host via the `llm.complete` reverse
	// RPC so the Kernel can enforce guardrails, cost, and audit.
	LLMAccessProxied LLMAccessMode = "proxied"

	// LLMAccessDirect lets the sidecar hold a provider API key and call the LLM
	// directly. Only Zone-3 brains may use this; the sidecar MUST emit a
	// `trace.emit` record for every call or the Kernel circuit-breaks.
	LLMAccessDirect LLMAccessMode = "direct"

	// LLMAccessHybrid defaults to proxied but lets the sidecar request a
	// short-lived direct window via `llm.requestDirectAccess(ttl)`.
	LLMAccessHybrid LLMAccessMode = "hybrid"
)

// Descriptor is the registration record a brain emits during `initialize`.
// See 02-BrainKernel设计.md §12.5.2 and 20-协议规格.md §4 for the full set of
// fields exchanged in the handshake.
type Descriptor struct {
	// Kind identifies this brain role.
	Kind Kind

	// Version is the brain sidecar's own version, independent of SDK/Kernel.
	Version string

	// LLMAccess controls how this brain obtains LLM credentials (see §12.5.7).
	LLMAccess LLMAccessMode

	// SupportedTools is the tool registry this brain claims capability for.
	// The Kernel uses it to validate `tool.invoke` requests.
	SupportedTools []string

	// Capabilities is a free-form set of feature flags (e.g., "streaming",
	// "multi-turn"). Third parties may extend this without breaking compat.
	Capabilities map[string]bool
}

// Agent is the Kernel-side abstraction of a running brain sidecar.
//
// Implementations of this interface are produced by the kernel package's
// BrainRunner — not by user code. User code interacts with brains via the
// Kernel's dispatch API, which internally calls Agent methods.
type Agent interface {
	// Kind returns the brain role.
	Kind() Kind

	// Descriptor returns the handshake descriptor this agent declared.
	Descriptor() Descriptor

	// Ready blocks until the sidecar has completed `initialize` and is ready
	// to accept work, or the context is cancelled.
	Ready(ctx context.Context) error

	// Shutdown requests graceful shutdown. The sidecar gets time to flush
	// trace events and release resources. After this returns, the agent is
	// no longer usable.
	Shutdown(ctx context.Context) error
}

// RPCAgent extends Agent with access to the underlying BidirRPC session.
// The Orchestrator uses this to register reverse-RPC handlers (llm.complete,
// subtask.delegate) on a running sidecar's RPC session after Start().
//
// Both ProcessRunner's processAgent and StdioRunner's pipeAgent implement
// this interface. Consumer code should type-assert: ag.(RPCAgent).
type RPCAgent interface {
	Agent

	// RPC returns the bidirectional JSON-RPC session for this sidecar.
	// The caller may register additional handlers or make outbound calls.
	RPC() interface{}
}
