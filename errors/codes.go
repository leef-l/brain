package errors

// This file is the v1 frozen reserved clause from 21-错误模型.md appendix A.
// Every error_code that appears on the wire MUST be registered here or added
// via the extension API in registry.go. Emitting an unregistered code is a
// protocol violation per 21 §5.1 and trips the errmodel_violation_total
// counter per §7.2.
//
// Naming rules frozen by 21 §5.1:
//   - snake_case ASCII, length ≤ 64
//   - "subject_verb_object" or "domain_event" shape
//   - domain-prefixed (sidecar_* / tool_* / llm_* / frame_* / brain_* /
//     workflow_* / db_* / record_* / internal_*)
//   - NO vague suffixes (error / failed / problem on their own)
//   - NO dynamic values embedded in the code itself
//
// Every const is mirrored into the global registry by init() in registry.go
// with the Class and Retryable defaults from appendix A. A table-driven test
// in codes_test.go verifies the constants and the registry agree.
//
// NOTE: This list is cross-language. Other SDKs (Python/Rust/TS) MUST copy
// the string values verbatim — see 21 §3.4.

// sidecar_* — sidecar process / handshake lifecycle. Appendix A §A.1.
const (
	// CodeSidecarStartFailed — sidecar process failed to start (pre-execv).
	// Class: InternalBug. Retryable: false.
	CodeSidecarStartFailed = "sidecar_start_failed"

	// CodeSidecarExitNonzero — sidecar exited with a non-zero status code.
	// Class: Permanent. Retryable: false.
	CodeSidecarExitNonzero = "sidecar_exit_nonzero"

	// CodeSidecarHung — sidecar missed its heartbeat deadline.
	// Class: Transient. Retryable: true.
	CodeSidecarHung = "sidecar_hung"

	// CodeSidecarStdoutEOF — unexpected EOF while reading sidecar stdout.
	// Class: Transient. Retryable: true.
	CodeSidecarStdoutEOF = "sidecar_stdout_eof"

	// CodeSidecarStdinBrokenPipe — EPIPE while writing to sidecar stdin.
	// Class: Transient. Retryable: true.
	CodeSidecarStdinBrokenPipe = "sidecar_stdin_broken_pipe"

	// CodeSidecarCrashed — sidecar process killed by a signal.
	// Class: InternalBug. Retryable: false.
	CodeSidecarCrashed = "sidecar_crashed"

	// CodeSidecarProtocolViolation — sidecar emitted a frame that violates
	// 20-协议规格.md. Class: InternalBug. Retryable: false.
	CodeSidecarProtocolViolation = "sidecar_protocol_violation"

	// CodeSidecarVersionMismatch — sidecar protocol version disagrees with
	// the Kernel's supported range. Class: Permanent. Retryable: false.
	CodeSidecarVersionMismatch = "sidecar_version_mismatch"
)

// tool_* — tool invocation failures. Appendix A §A.2.
const (
	// CodeToolNotFound — the requested tool name is not registered.
	// Class: Permanent. Retryable: false.
	CodeToolNotFound = "tool_not_found"

	// CodeToolExecutionFailed — the tool body raised a runtime failure
	// (non-zero exit / logical failure). Class: Permanent. Retryable: false.
	CodeToolExecutionFailed = "tool_execution_failed"

	// CodeToolTimeout — tool exceeded its per-call budget.
	// Class: Transient. Retryable: true.
	CodeToolTimeout = "tool_timeout"

	// CodeToolInputInvalid — tool input failed schema validation.
	// Class: UserFault. Retryable: false.
	CodeToolInputInvalid = "tool_input_invalid"

	// CodeToolSandboxDenied — sandbox policy rejected the tool call.
	// Class: SafetyRefused. Retryable: false.
	CodeToolSandboxDenied = "tool_sandbox_denied"
)

// llm_* — LLM provider / adapter failures. Appendix A §A.3.
const (
	// CodeLLMRateLimitedShortterm — short-term rate limit (retry-after ≤ 60s).
	// Class: Transient. Retryable: true.
	CodeLLMRateLimitedShortterm = "llm_rate_limited_shortterm"

	// CodeLLMQuotaExhaustedDaily — daily or monthly quota exhausted.
	// Class: QuotaExceeded. Retryable: false.
	CodeLLMQuotaExhaustedDaily = "llm_quota_exhausted_daily"

	// CodeLLMUpstream5xx — LLM upstream returned a 5xx.
	// Class: Transient. Retryable: true.
	CodeLLMUpstream5xx = "llm_upstream_5xx"

	// CodeLLMSafetyRefused — LLM refused the request on safety grounds.
	// Class: SafetyRefused. Retryable: false.
	CodeLLMSafetyRefused = "llm_safety_refused"

	// CodeLLMContextOverflow — request exceeds model context window.
	// Class: Permanent. Retryable: false.
	CodeLLMContextOverflow = "llm_context_overflow"

	// CodeLLMAuthFailed — API key missing / invalid / expired.
	// Class: Permanent. Retryable: false.
	CodeLLMAuthFailed = "llm_auth_failed"
)

// frame_* / protocol_* — wire protocol errors. Appendix A §A.4.
const (
	// CodeFrameTooLarge — frame exceeds the 16 MiB limit.
	// Class: Permanent. Retryable: false.
	CodeFrameTooLarge = "frame_too_large"

	// CodeFrameParseError — JSON parse failure in a frame payload.
	// Class: Permanent. Retryable: false.
	CodeFrameParseError = "frame_parse_error"

	// CodeFrameEncodingError — local serialization failed before send.
	// Class: InternalBug. Retryable: false.
	CodeFrameEncodingError = "frame_encoding_error"

	// CodeMethodNotFound — RPC method name not registered.
	// Class: Permanent. Retryable: false.
	CodeMethodNotFound = "method_not_found"

	// CodeInvalidParams — request params failed schema validation.
	// Class: UserFault. Retryable: false.
	CodeInvalidParams = "invalid_params"

	// CodeDeadlineExceeded — context deadline hit mid-request.
	// Class: Transient. Retryable: true.
	CodeDeadlineExceeded = "deadline_exceeded"

	// CodeStaleResponse — response arrived after the request was already
	// cancelled or timed out. Class: InternalBug. Retryable: false.
	CodeStaleResponse = "stale_response"

	// CodeShuttingDown — Kernel is gracefully shutting down; retry to a new
	// instance. Class: Transient. Retryable: true.
	CodeShuttingDown = "shutting_down"
)

// brain_* / workflow_* — runtime invariants. Appendix A §A.5.
const (
	// CodeBrainOverloaded — brain inflight queue full.
	// Class: Transient. Retryable: true.
	CodeBrainOverloaded = "brain_overloaded"

	// CodeBrainTaskFailed — top-level wrap for a domain task failure.
	// Class: Permanent. Retryable: false.
	CodeBrainTaskFailed = "brain_task_failed"

	// CodeWorkflowPrecondition — WorkflowRun is not in a state that allows
	// the requested operation. Class: UserFault. Retryable: false.
	CodeWorkflowPrecondition = "workflow_precondition"

	// CodePolicyGateDenied — a risk-gate policy vetoed the action.
	// Class: SafetyRefused. Retryable: false.
	CodePolicyGateDenied = "policy_gate_denied"
)

// budget_* — Agent Loop budget exhaustion codes from 22-Agent-Loop规格.md §5.
// Each code maps one-to-one to a Budget dimension (turns, cost, tool calls,
// LLM calls, wall-clock) so the Runner can surface the exact exhausted axis
// to the caller. All five are ClassPermanent because exhaustion is a hard
// stop: the Run cannot make further progress without an operator bumping
// the envelope. Retryable is false for the same reason.
const (
	// CodeBudgetTurnsExhausted — UsedTurns reached MaxTurns.
	// Class: Permanent. Retryable: false. See 22 §5.1.
	CodeBudgetTurnsExhausted = "budget_turns_exhausted"

	// CodeBudgetCostExhausted — UsedCostUSD reached MaxCostUSD.
	// Class: Permanent. Retryable: false. See 22 §5.2.
	CodeBudgetCostExhausted = "budget_cost_exhausted"

	// CodeBudgetToolCallsExhausted — UsedToolCalls reached MaxToolCalls.
	// Class: Permanent. Retryable: false. See 22 §5.3.
	CodeBudgetToolCallsExhausted = "budget_tool_calls_exhausted"

	// CodeBudgetLLMCallsExhausted — UsedLLMCalls reached MaxLLMCalls.
	// Class: Permanent. Retryable: false. See 22 §5.3.
	CodeBudgetLLMCallsExhausted = "budget_llm_calls_exhausted"

	// CodeBudgetTimeoutExhausted — ElapsedTime exceeded MaxDuration.
	// Class: Permanent. Retryable: false. See 22 §5.4.
	CodeBudgetTimeoutExhausted = "budget_timeout_exhausted"
)

// loop_* / reflection_* — Agent Loop stuck-loop and reflection budget
// codes from 22-Agent-Loop规格.md §11 and §12. The loop detector fires
// agent_loop_detected on the third strike of any of the five patterns in
// §11.1 (repeated tool call, repeated error fingerprint, no-progress turn,
// budget hole, thought explosion). reflection_budget_exhausted is the
// companion code for §12.2. Both are ClassPermanent because the Run
// cannot recover without operator intervention — see 22 §11.2.
const (
	// CodeAgentLoopDetected — the LoopDetector promoted a stuck-loop
	// pattern to its third strike and the Runner MUST fail the Run.
	// Class: Permanent. Retryable: false. See 22 §11.2.
	CodeAgentLoopDetected = "agent_loop_detected"

	// CodeReflectionBudgetExhausted — the Runner exceeded one of the
	// three reflection budgets (per_fingerprint / per_tool / total).
	// Class: Permanent. Retryable: false. See 22 §12.2.
	CodeReflectionBudgetExhausted = "reflection_budget_exhausted"
)

// sanitizer_* — 22-Agent-Loop规格.md §10.2 tool-result sanitizer codes.
// Every tool result MUST pass through the sanitizer before the next Turn
// feeds it to the LLM; a policy violation (secret detected with no
// redaction rule, binary content with no artifact fallback, ...) MUST
// abort with a ClassPermanent BrainError instead of leaking the raw
// payload to the model.
const (
	// CodeToolSanitizeFailed — sanitizer rejected the tool result.
	// Class: Permanent. Retryable: false. See 22 §10.2.
	CodeToolSanitizeFailed = "tool_sanitize_failed"
)

// db_* / record_* — persistence failures. Appendix A §A.6.
const (
	// CodeRecordNotFound — query returned no rows for a required lookup.
	// Class: UserFault. Retryable: false.
	CodeRecordNotFound = "record_not_found"

	// CodeDBDeadlock — MySQL error 1213 / SQLite SQLITE_BUSY equivalent.
	// Class: Transient. Retryable: true.
	CodeDBDeadlock = "db_deadlock"

	// CodeDBUniqueViolation — MySQL error 1062 / duplicate key.
	// Class: UserFault. Retryable: false.
	CodeDBUniqueViolation = "db_unique_violation"
)

// internal_* — Kernel-side bugs. Appendix A §A.7. These MUST only appear
// with ClassInternalBug and MUST trigger an alert per 21 §8.2.
const (
	// CodePanicked — recovered Go panic. Class: InternalBug. Retryable: false.
	CodePanicked = "panicked"

	// CodeAssertionFailed — invariant assertion tripped.
	// Class: InternalBug. Retryable: false.
	CodeAssertionFailed = "assertion_failed"

	// CodeInvariantViolated — business invariant violation (state machine
	// corruption, ordering violation, ...). Class: InternalBug. Retryable: false.
	CodeInvariantViolated = "invariant_violated"

	// CodeUnknown — fallback bucket. Its appearance SHOULD trigger an alert —
	// Unknown means we failed to classify, which is a bug in the caller.
	// Class: InternalBug. Retryable: false.
	CodeUnknown = "unknown"
)
