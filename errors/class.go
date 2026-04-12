// Package errors implements the cross-language error contract defined in
// 21-错误模型.md.
//
// The BrainError struct, ErrorClass six-value taxonomy, Decide retry matrix,
// and Fingerprint algorithm are all frozen v1 contracts — see the spec for
// normative behavior. This package is the single error-construction entry
// point for every layer of BrainKernel: business code MUST return
// *BrainError rather than bare Go errors across Runner/Kernel boundaries.
//
// This package has zero external dependencies and does not import any other
// brain subpackage; it sits at the root of the dependency topology so that
// every other subpackage can depend on it freely.
package errors

// ErrorClass is the six-value taxonomy defined in 21-错误模型.md §4.1.
//
// Every BrainError MUST classify into exactly one of the frozen v1 classes
// below. The scheduler, retrier, circuit breaker, and alert router only look
// at Class when deciding what to do with an error, so a wrong classification
// will mis-route the entire recovery strategy. See 21 §4.2 for the
// classification principles and §4.4 for the anti-patterns to avoid.
type ErrorClass string

const (
	// ClassTransient marks short-lived external failures where replaying the
	// same request has a high probability of succeeding (network blip, DB
	// deadlock, LLM 5xx, short-term rate limit). This is the only class whose
	// default Retryable is true. See 21 §4.1.
	ClassTransient ErrorClass = "transient"

	// ClassPermanent marks failures where retrying the same request will
	// always fail — the input or the code must change first (compile errors,
	// test failures, syntax errors, missing files, invalid args). See 21 §4.1.
	ClassPermanent ErrorClass = "permanent"

	// ClassUserFault marks errors caused by user-supplied prompt, config, or
	// files. Never retried by the scheduler; the UI must ask the user to fix
	// their input and resubmit. See 21 §4.1 and 21 §4.2.
	ClassUserFault ErrorClass = "user_fault"

	// ClassQuotaExceeded marks quota/rate/concurrency exhaustion that recovers
	// only after waiting or upgrading (LLM daily token cap, per-minute request
	// cap). Not retried by the normal matrix; goes through a separate quota
	// cooldown path. See 21 §4.1 and 21 §8.4.
	ClassQuotaExceeded ErrorClass = "quota_exceeded"

	// ClassSafetyRefused marks refusals from the LLM, policy layer, or sandbox
	// (Anthropic safety refusal, sandbox privilege denial, gate veto). Not
	// retryable; escalates to a human checkpoint via Decide → AskHuman. See
	// 21 §4.1 and 21 §10.
	ClassSafetyRefused ErrorClass = "safety_refused"

	// ClassInternalBug marks bugs in the Kernel or sidecar code itself
	// (panics, assertion failures, unexpected schema violations). Not
	// retryable but MUST raise an alert and contributes to the internal-bug
	// budget that triggers degrade/quarantine. See 21 §4.1 and 21 §8.4.
	ClassInternalBug ErrorClass = "internal_bug"
)
