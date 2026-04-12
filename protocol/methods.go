package protocol

// The constants in this file are the frozen v1 JSON-RPC method-name
// namespace from 20-协议规格.md §10. Any wire method that does not appear
// in this list (or in a future additive extension with a bumped
// ProtocolVersion) is a protocol violation and MUST be rejected with
// -32601 "Method not found" per 20-协议规格.md §9.2.
//
// The list below mirrors the 5.7 cheatsheet in brain骨架实施计划.md. When
// the full method catalogue from 20-协议规格.md §10.1/§10.2 is rolled into
// the SDK (initialize, brain.describe, brain.run, brain.cancel,
// health.ping, shutdown, artifact.beginUpload, ...), the additional names
// MUST be appended to this file as a skeleton extension rather than being
// invented inside call sites.

// Lifecycle methods — 20-协议规格.md §6 and §10.1.
const (
	// MethodInitialize is the host→sidecar handshake request that opens
	// the session; its payload is InitializeRequest. See 20 §6.4
	// Starting.
	MethodInitialize = "initialize"

	// MethodShutdown is the host→sidecar graceful shutdown request that
	// drives the sidecar state machine into Draining. See 20 §6.3.
	MethodShutdown = "shutdown"

	// MethodHeartbeat is the bidirectional keep-alive request covered
	// by 20 §7. Implementations MAY alias this to the protocol-layer
	// `$/ping` meta method from 20 §7.2.
	MethodHeartbeat = "heartbeat"
)

// LLM methods — 20-协议规格.md §10.1 (sidecar→host direction).
const (
	// MethodLLMComplete is the non-streaming chat completion request
	// emitted by the sidecar on the reverse channel. See
	// 22-Agent-Loop规格.md §5 for the payload contract.
	MethodLLMComplete = "llm.complete"

	// MethodLLMStream is the streaming chat completion request; its
	// incremental events are delivered via `$/progress` notifications
	// per 20 §10.3. See 22-Agent-Loop规格.md §5.
	MethodLLMStream = "llm.stream"

	// MethodLLMRequestDirect is the optional sidecar→host request that
	// asks for a temporary direct-access window in LLMAccessHybrid
	// mode. See 20 §10.2 and 23-安全模型.md for the approval rules.
	MethodLLMRequestDirect = "llm.requestDirectAccess"
)

// Tool method — 20-协议规格.md §10.1 (sidecar→host direction).
const (
	// MethodToolInvoke is the sidecar→host tool invocation request; the
	// host runs the tool on behalf of the brain inside the sandbox.
	MethodToolInvoke = "tool.invoke"
)

// Plan methods — 20-协议规格.md §10.1 (sidecar→host direction, central
// brain only per 02-BrainKernel设计.md §12.5.4.3).
const (
	// MethodPlanCreate is the central-brain→host request that persists
	// a new BrainPlan version for the current Run.
	MethodPlanCreate = "plan.create"

	// MethodPlanUpdate is the central-brain→host request that appends a
	// PlanDelta to the current BrainPlan version.
	MethodPlanUpdate = "plan.update"
)

// Artifact methods — 20-协议规格.md §8 (content_ref upload/download).
const (
	// MethodArtifactPut is the sidecar→host request that uploads a
	// large artifact payload via the upload session protocol from
	// 20 §8.3.
	MethodArtifactPut = "artifact.put"

	// MethodArtifactGet is the sidecar→host request that resolves an
	// existing artifact content_ref and streams the bytes back, per
	// 20 §8.4.
	MethodArtifactGet = "artifact.get"
)

// Observability methods — 20-协议规格.md §10.1 (sidecar→host
// notifications).
const (
	// MethodTraceEmit is the sidecar→host notification that writes a
	// span/event into the host-side trace pipeline.
	MethodTraceEmit = "trace.emit"

	// MethodAuditEmit is the sidecar→host notification that appends an
	// audit record for security-relevant actions (see 23-安全模型.md).
	MethodAuditEmit = "audit.emit"
)
