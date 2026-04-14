package errors

import (
	"math/rand"
	"time"
)

// Action is the enumeration of verdicts the Decide engine can return. It is
// the single source of truth for what a scheduler does with a failed
// BrainError — any ad-hoc if/else in callers is a protocol violation per
// 21-错误模型.md §8.
type Action string

const (
	// ActionRetry — re-execute the same request after BackoffHint. Callers
	// MUST honor BackoffHint and MUST apply the ±20% jitter from §8.3 before
	// sleeping (the jitter is already baked in by Decide, so callers can use
	// the value directly).
	ActionRetry Action = "retry"

	// ActionFail — give up on the request. Downstream callers surface the
	// failure to the task/workflow layer. Fail does not imply a violation;
	// it is the expected terminal verdict for Permanent / UserFault / etc.
	ActionFail Action = "fail"

	// ActionDegradeBrain — mark the sidecar as degraded per 21 §8.4 error
	// budget rules. Retry budgets halve, concurrency drops to 1, and the
	// InternalBug counter is incremented. After the counter clears three in
	// five minutes the Kernel escalates to Quarantine.
	ActionDegradeBrain Action = "degrade_brain"

	// ActionQuarantine — isolate the sidecar. New requests targeted at this
	// brain MUST be rejected immediately by Decide until the half-open probe
	// succeeds (see 21 §8.5). Always accompanied by a P1 alert.
	ActionQuarantine Action = "quarantine"

	// ActionAskHuman — hand the decision to a human via the checkpoint
	// pipeline (21 §10). The only class that produces this is SafetyRefused.
	ActionAskHuman Action = "ask_human"
)

// FaultPolicy selects how aggressively Decide tries to recover from
// failures. Set at the WorkflowRun level and passed down through
// DecideContext. See 21 §8.1.
type FaultPolicy string

const (
	// FaultPolicyFailFast gives up on the first Permanent failure and does
	// not attempt graceful continuation. Default for user-facing flows where
	// the operator wants to see failures quickly.
	FaultPolicyFailFast FaultPolicy = "fail_fast"

	// FaultPolicyBestEffort fails the current task but lets siblings in the
	// same batch continue. Default for batch jobs and multi-stage workflows
	// where one failure should not torpedo the whole run.
	FaultPolicyBestEffort FaultPolicy = "best_effort"

	// FaultPolicyRetry is a label that affects some niche routing decisions
	// but does NOT enable retry for Permanent (the matrix in §8.2 row 11
	// explicitly makes Permanent + retry still fail). The name is historical;
	// it exists because the spec freezes it.
	FaultPolicyRetry FaultPolicy = "retry"
)

// Health is the current sidecar health state. Driven by the error budget
// machinery in 21 §8.4 and mutated by Decide itself when it returns
// DegradeBrain / Quarantine. Tests can set it directly.
type Health string

const (
	// HealthHealthy is the normal state. Full retry budget and concurrency.
	HealthHealthy Health = "healthy"

	// HealthDegraded is the first-level brownout. Retry budget halved,
	// concurrency capped at 1, one more violation lands in Quarantine.
	HealthDegraded Health = "degraded"

	// HealthQuarantined is the isolated state. New requests refused for
	// 10 minutes, then a half-open probe decides the next transition.
	HealthQuarantined Health = "quarantined"
)

// DecideContext carries the runtime inputs Decide needs beyond the
// BrainError itself. Callers MUST populate FaultPolicy, Health, and Attempt;
// everything else has sensible zero-value defaults. See 21 §8.1.
type DecideContext struct {
	// FaultPolicy is the WorkflowRun-level policy. Empty strings are
	// treated as FaultPolicyFailFast.
	FaultPolicy FaultPolicy

	// Attempt is the zero-based retry counter — 0 means "this was the first
	// try". The scheduler increments it before the next call to Decide so
	// that matrix row 4 (attempt ≥ 3) triggers correctly on the fourth
	// overall execution.
	Attempt int

	// Health is the sidecar's health state at the moment of the failure.
	// Callers read this from the error-budget ledger before calling Decide.
	Health Health

	// Now overrides time.Now for deterministic tests. Production code leaves
	// it zero and Decide uses the package-level now() function.
	Now time.Time

	// Rand overrides the jitter source for deterministic tests. Production
	// code leaves it nil and Decide seeds its own local rand.Rand.
	Rand *rand.Rand
}

// Decision is the output of Decide. The engine returns one Decision per
// failure — callers MUST NOT second-guess the verdict or splice in their
// own retry logic. See 21 §8.1.
type Decision struct {
	// Action is the verdict. Always one of the ActionXxx constants.
	Action Action

	// BackoffHint is the recommended delay before the next attempt when
	// Action == ActionRetry. For other actions the value is zero. Jitter is
	// already applied — callers can sleep(BackoffHint) directly.
	BackoffHint time.Duration

	// MaxRetries is the remaining retry budget at the time of this
	// decision. Helpful for callers that want to log "X tries left" but not
	// used for control flow (the matrix is authoritative).
	MaxRetries int

	// ReasonLog is a human-readable rationale for the verdict. Goes to the
	// structured log line attached to this failure. Machine consumers MUST
	// look at Action instead of parsing this string.
	ReasonLog string

	// Violation is set to true when the decision was forced by a protocol
	// violation (matrix row 8: Transient + Retryable=false). Callers MUST
	// bump errmodel_violation_total when this is true — see 21 §7.2.
	Violation bool
}

// Retry is a legacy alias for Action == ActionRetry. Retained so the
// skeleton callers written before the full Decide landed keep compiling.
// New code SHOULD switch to the Action field.
func (d Decision) Retry() bool { return d.Action == ActionRetry }

// Reason is a legacy alias for ReasonLog, kept for the same reason as Retry.
func (d Decision) Reason() string { return d.ReasonLog }

// Backoff parameters from 21-错误模型.md §8.3. These are package-level
// variables so operators can tune them from a single spot if the default
// ever needs to change — but the spec freezes them in v1, so touching
// these outside of a host-level override hook is a compatibility break.
var (
	// backoffInitial is the first-retry delay before jitter. §8.3.
	backoffInitial = 1 * time.Second

	// backoffMultiplier is the exponential growth factor. Each retry
	// multiplies the previous delay by this until backoffMax is reached.
	backoffMultiplier = 2.0

	// backoffMax is the single-wait cap. §8.3 says 30s.
	backoffMax = 30 * time.Second

	// backoffJitter is the ±fraction applied to every backoff. 0.2 means
	// ±20%. Multiplied by a uniform [-1, 1) random source.
	backoffJitter = 0.2

	// maxRetriesHealthy is the total retry budget on a healthy sidecar.
	// §8.3 says ≤ 3.
	maxRetriesHealthy = 3

	// maxRetriesDegraded is the total retry budget on a degraded sidecar.
	// §8.3 says ≤ 1.
	maxRetriesDegraded = 1
)

// Decide applies the 17-row decision matrix from 21-错误模型.md appendix B
// to err and returns the retry/backoff verdict. It is the single source of
// truth for retry decisions in the Kernel — any caller that writes its own
// if/else on Class is a protocol violation (21 §8).
//
// The algorithm walks the matrix top-to-bottom, taking the first row that
// matches. Rows are grouped by Class for readability; each group handles
// retryable / fault_policy / health / attempt branching inline. The rows
// correspond 1:1 with appendix B rows 1-17 — C-E-04 verifies this.
//
// Nil BrainError is treated as "no failure" and produces ActionFail with a
// violation reason. Callers SHOULD NOT call Decide with nil; the check is
// defensive.
func Decide(err *BrainError, dctx DecideContext) Decision {
	if err == nil {
		return Decision{
			Action:    ActionFail,
			ReasonLog: "decide called with nil error (violation)",
			Violation: true,
		}
	}
	// Normalize zero values so tests and production read the same code path.
	if dctx.FaultPolicy == "" {
		dctx.FaultPolicy = FaultPolicyFailFast
	}
	if dctx.Health == "" {
		dctx.Health = HealthHealthy
	}

	switch err.Class {
	case ClassTransient:
		return decideTransient(err, dctx)
	case ClassPermanent:
		return decidePermanent(err, dctx)
	case ClassUserFault:
		// Row 12. UserFault never retries — UI must ask the user to fix
		// their input and resubmit.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "user_fault: returning to UI for user correction",
		}
	case ClassQuotaExceeded:
		// Row 13. Quota cooldown path is handled outside Decide; here we
		// just terminate the current request. The scheduler is expected to
		// pause the brain and subscribe to cooldown expiry.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "quota_exceeded: entering cooldown path",
		}
	case ClassSafetyRefused:
		// Row 14. Always human — 21 §10 forbids automatic retry or
		// rewriting to a different class to bypass the gate.
		return Decision{
			Action:    ActionAskHuman,
			ReasonLog: "safety_refused: escalating to human checkpoint",
		}
	case ClassInternalBug:
		return decideInternalBug(err, dctx)
	default:
		// Unknown class is a taxonomy violation. Fail + flag it loudly.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "unknown class (" + string(err.Class) + "): violation",
			Violation: true,
		}
	}
}

// decideTransient implements appendix B rows 1-8: the Transient ladder with
// health-aware retry budgets and the retryable-false violation row.
func decideTransient(err *BrainError, dctx DecideContext) Decision {
	// Row 8: Transient + Retryable=false is a protocol violation regardless
	// of health or fault_policy. Catch this before anything else so the
	// violation counter always fires.
	if !err.Retryable {
		return Decision{
			Action:    ActionFail,
			ReasonLog: "transient + retryable=false is a class/retryable invariant violation",
			Violation: true,
		}
	}

	// Row 7: quarantined health short-circuits — the whole point of
	// quarantine is to refuse new traffic, even when the request itself is
	// retryable in principle.
	if dctx.Health == HealthQuarantined {
		return Decision{
			Action:    ActionFail,
			ReasonLog: "transient on quarantined sidecar: refusing until probe",
		}
	}

	// Rows 5-6: degraded health — one and only one retry.
	if dctx.Health == HealthDegraded {
		if dctx.Attempt >= maxRetriesDegraded {
			return Decision{
				Action:     ActionFail,
				MaxRetries: 0,
				ReasonLog:  "transient retry budget exhausted on degraded sidecar",
			}
		}
		return Decision{
			Action:      ActionRetry,
			BackoffHint: backoffFor(1, dctx), // degraded doubles the initial
			MaxRetries:  maxRetriesDegraded - dctx.Attempt - 1,
			ReasonLog:   "transient retry on degraded sidecar (1 attempt budget)",
		}
	}

	// Rows 1-4: healthy. Retry budget is 3 (attempts 0, 1, 2).
	if dctx.Attempt >= maxRetriesHealthy {
		return Decision{
			Action:     ActionFail,
			MaxRetries: 0,
			ReasonLog:  "transient retry budget exhausted on healthy sidecar",
		}
	}
	return Decision{
		Action:      ActionRetry,
		BackoffHint: backoffFor(dctx.Attempt, dctx),
		MaxRetries:  maxRetriesHealthy - dctx.Attempt - 1,
		ReasonLog:   "transient retry on healthy sidecar",
	}
}

// decidePermanent implements rows 9-11: all three fault policies ultimately
// fail. The ReasonLog differentiates the rows so the scheduler can tell the
// UI what to show.
func decidePermanent(err *BrainError, dctx DecideContext) Decision {
	switch dctx.FaultPolicy {
	case FaultPolicyBestEffort:
		return Decision{
			Action:    ActionFail,
			ReasonLog: "permanent under best_effort: failing this task, siblings continue",
		}
	case FaultPolicyRetry:
		// Row 11. The "retry" label does NOT enable retry for Permanent.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "permanent under retry policy: retry does not apply to permanent class",
		}
	default:
		// Row 9 (fail_fast) and unset. Immediate fail.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "permanent under fail_fast: immediate fail",
		}
	}
}

// decideInternalBug implements rows 15-17: health-based escalation from
// DegradeBrain → Quarantine → Fail-because-already-quarantined. Every
// InternalBug contributes to the §8.4 error budget; the Action tells the
// caller which ledger to touch.
func decideInternalBug(err *BrainError, dctx DecideContext) Decision {
	switch dctx.Health {
	case HealthQuarantined:
		// Row 17. Already isolated — nothing to escalate. Still a bug, but
		// the quarantine timer handles recovery.
		return Decision{
			Action:    ActionFail,
			ReasonLog: "internal_bug on quarantined sidecar: refused",
		}
	case HealthDegraded:
		// Row 16. Second strike in the degraded window escalates to
		// quarantine + P1 alert.
		return Decision{
			Action:    ActionQuarantine,
			ReasonLog: "internal_bug on degraded sidecar: escalating to quarantine (P1)",
		}
	default:
		// Row 15. First strike — degrade the brain and keep going.
		return Decision{
			Action:    ActionDegradeBrain,
			ReasonLog: "internal_bug on healthy sidecar: degrading brain",
		}
	}
}

// backoffFor returns the jittered exponential backoff duration for a given
// attempt index. attempt 0 → 1s, attempt 1 → 2s, attempt 2 → 4s, capped at
// backoffMax, with ±backoffJitter applied. A local rand.Rand is created per
// call when the caller did not inject one — Decide must be safe for
// concurrent use, so we cannot share the package math/rand default source.
func backoffFor(attempt int, dctx DecideContext) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := float64(backoffInitial)
	for i := 0; i < attempt; i++ {
		base *= backoffMultiplier
		if time.Duration(base) >= backoffMax {
			base = float64(backoffMax)
			break
		}
	}
	if time.Duration(base) > backoffMax {
		base = float64(backoffMax)
	}

	// Apply ±backoffJitter. Uniform [-1, 1) × jitter gives symmetric spread.
	var jitter float64
	if dctx.Rand != nil {
		jitter = (dctx.Rand.Float64()*2 - 1) * backoffJitter
	} else {
		// Per-call Rand seeded from time.Now so unseeded callers still get
		// non-correlated spreads. time.Now is cheap relative to the sleep.
		src := rand.New(rand.NewSource(time.Now().UnixNano()))
		jitter = (src.Float64()*2 - 1) * backoffJitter
	}
	return time.Duration(base * (1 + jitter))
}
