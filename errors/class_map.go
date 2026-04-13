package errors

// CodeMetadata is the immutable per-code descriptor registered in
// 21-错误模型.md appendix A. Every reserved code MUST have exactly one
// metadata row; the global registry (registry.go) keeps these indexed and
// exposes them via Lookup.
//
// This type is intentionally tiny so the compile-time reservedMetadata slice
// below stays readable as a giant literal table.
type CodeMetadata struct {
	// Code is the wire-level error_code string. MUST match the constant in
	// codes.go. MUST satisfy the naming rules in 21 §5.1.
	Code string

	// Class is the v1 taxonomy bucket this code belongs to. Decide reads
	// this field (via Lookup) to route retry/circuit-breaker decisions.
	Class ErrorClass

	// Retryable is the static default for the Retryable field on any
	// BrainError built with this code. A caller MAY override it via
	// WithRetryable, but anything inconsistent with the Class invariant
	// (e.g. ClassTransient + Retryable=false) is a protocol violation per
	// 21 §3.2 field-level MUST list.
	Retryable bool
}

// reservedMetadata is the 21 appendix A clause reproduced as Go data. The
// init function in registry.go walks this slice and seeds the global
// ErrorRegistry. The slice is exported via ReservedCodes for tests that want
// to enumerate every reserved code.
//
// Ordering mirrors appendix A §A.1~§A.7 so a visual diff stays readable.
var reservedMetadata = []CodeMetadata{
	// A.1 sidecar_*
	{CodeSidecarStartFailed, ClassInternalBug, false},
	{CodeSidecarExitNonzero, ClassPermanent, false},
	{CodeSidecarHung, ClassTransient, true},
	{CodeSidecarStdoutEOF, ClassTransient, true},
	{CodeSidecarStdinBrokenPipe, ClassTransient, true},
	{CodeSidecarCrashed, ClassInternalBug, false},
	{CodeSidecarProtocolViolation, ClassInternalBug, false},
	{CodeSidecarVersionMismatch, ClassPermanent, false},

	// A.2 tool_*
	{CodeToolNotFound, ClassPermanent, false},
	{CodeToolExecutionFailed, ClassPermanent, false},
	{CodeToolTimeout, ClassTransient, true},
	{CodeToolInputInvalid, ClassUserFault, false},
	{CodeToolSandboxDenied, ClassSafetyRefused, false},

	// A.3 llm_*
	{CodeLLMRateLimitedShortterm, ClassTransient, true},
	{CodeLLMQuotaExhaustedDaily, ClassQuotaExceeded, false},
	{CodeLLMUpstream5xx, ClassTransient, true},
	{CodeLLMSafetyRefused, ClassSafetyRefused, false},
	{CodeLLMContextOverflow, ClassPermanent, false},
	{CodeLLMAuthFailed, ClassPermanent, false},

	// A.4 frame_* / protocol_*
	{CodeFrameTooLarge, ClassPermanent, false},
	{CodeFrameParseError, ClassPermanent, false},
	{CodeFrameEncodingError, ClassInternalBug, false},
	{CodeMethodNotFound, ClassPermanent, false},
	{CodeInvalidParams, ClassUserFault, false},
	{CodeDeadlineExceeded, ClassTransient, true},
	{CodeStaleResponse, ClassInternalBug, false},
	{CodeShuttingDown, ClassTransient, true},

	// A.5 brain_* / workflow_*
	{CodeBrainOverloaded, ClassTransient, true},
	{CodeBrainTaskFailed, ClassPermanent, false},
	{CodeWorkflowPrecondition, ClassUserFault, false},
	{CodePolicyGateDenied, ClassSafetyRefused, false},

	// A.6 db_* / record_*
	{CodeRecordNotFound, ClassUserFault, false},
	{CodeDBDeadlock, ClassTransient, true},
	{CodeDBUniqueViolation, ClassUserFault, false},

	// A.7 internal_*
	{CodePanicked, ClassInternalBug, false},
	{CodeAssertionFailed, ClassInternalBug, false},
	{CodeInvariantViolated, ClassInternalBug, false},
	{CodeUnknown, ClassInternalBug, false},

	// A.8 budget_* — 22-Agent-Loop规格.md §5.
	{CodeBudgetTurnsExhausted, ClassPermanent, false},
	{CodeBudgetCostExhausted, ClassPermanent, false},
	{CodeBudgetToolCallsExhausted, ClassPermanent, false},
	{CodeBudgetLLMCallsExhausted, ClassPermanent, false},
	{CodeBudgetTimeoutExhausted, ClassPermanent, false},

	// A.9 loop_* / reflection_* — 22-Agent-Loop规格.md §11, §12.
	{CodeAgentLoopDetected, ClassPermanent, false},
	{CodeReflectionBudgetExhausted, ClassPermanent, false},

	// A.10 sanitizer_* — 22-Agent-Loop规格.md §10.2.
	{CodeToolSanitizeFailed, ClassPermanent, false},

	// A.11 license_* — paid specialist brain licensing.
	{CodeLicenseNotFound, ClassPermanent, false},
	{CodeLicenseInvalidSignature, ClassPermanent, false},
	{CodeLicenseExpired, ClassPermanent, false},
	{CodeLicenseNotYetValid, ClassPermanent, false},
	{CodeLicenseBrainNotAllowed, ClassPermanent, false},
	{CodeLicenseFeatureNotAllowed, ClassPermanent, false},
	{CodeLicenseSchemaUnsupported, ClassPermanent, false},
}

// ReservedCodes returns a defensive copy of the v1 appendix A metadata clause.
// Callers MUST NOT mutate the returned slice — mutations won't propagate to
// the live registry, but the slice header is shared across calls. Use Lookup
// for per-code queries.
func ReservedCodes() []CodeMetadata {
	out := make([]CodeMetadata, len(reservedMetadata))
	copy(out, reservedMetadata)
	return out
}
