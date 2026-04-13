package skeleton

import (
	"math/rand"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// ---------------------------------------------------------------------------
// Decide 决策矩阵核心路径
// ---------------------------------------------------------------------------

func TestDecideTransientHealthyRetry(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("hung"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{
		Health:  brainerrors.HealthHealthy,
		Attempt: 0,
	})
	if d.Action != brainerrors.ActionRetry {
		t.Errorf("Action = %q, want retry", d.Action)
	}
	if d.BackoffHint <= 0 {
		t.Error("BackoffHint should be positive")
	}
}

func TestDecideTransientExhausted(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("hung"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{
		Health:  brainerrors.HealthHealthy,
		Attempt: 3,
	})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("Action = %q, want fail after exhaustion", d.Action)
	}
}

func TestDecideTransientDegraded(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarStdoutEOF,
		brainerrors.WithMessage("eof"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{
		Health:  brainerrors.HealthDegraded,
		Attempt: 0,
	})
	if d.Action != brainerrors.ActionRetry {
		t.Errorf("Action = %q, want retry on degraded first attempt", d.Action)
	}
	d2 := brainerrors.Decide(err, brainerrors.DecideContext{
		Health:  brainerrors.HealthDegraded,
		Attempt: 1,
	})
	if d2.Action != brainerrors.ActionFail {
		t.Errorf("Action = %q, want fail on degraded second attempt", d2.Action)
	}
}

func TestDecideTransientQuarantined(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("hung"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{
		Health: brainerrors.HealthQuarantined,
	})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("Action = %q, want fail on quarantined", d.Action)
	}
}

func TestDecidePermanentAlwaysFails(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeToolNotFound,
		brainerrors.WithMessage("not found"),
	)
	policies := []brainerrors.FaultPolicy{
		brainerrors.FaultPolicyFailFast,
		brainerrors.FaultPolicyBestEffort,
		brainerrors.FaultPolicyRetry,
	}
	for _, p := range policies {
		d := brainerrors.Decide(err, brainerrors.DecideContext{FaultPolicy: p})
		if d.Action != brainerrors.ActionFail {
			t.Errorf("Permanent + %q: Action = %q, want fail", p, d.Action)
		}
	}
}

func TestDecideUserFaultFails(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeToolInputInvalid,
		brainerrors.WithMessage("bad input"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("Action = %q, want fail for user_fault", d.Action)
	}
}

func TestDecideQuotaExceededFails(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeLLMQuotaExhaustedDaily,
		brainerrors.WithMessage("quota"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("Action = %q, want fail for quota_exceeded", d.Action)
	}
}

func TestDecideSafetyRefusedAskHuman(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeLLMSafetyRefused,
		brainerrors.WithMessage("refused"),
	)
	d := brainerrors.Decide(err, brainerrors.DecideContext{})
	if d.Action != brainerrors.ActionAskHuman {
		t.Errorf("Action = %q, want ask_human", d.Action)
	}
}

func TestDecideInternalBugEscalation(t *testing.T) {
	err := brainerrors.New(brainerrors.CodePanicked,
		brainerrors.WithMessage("panic"),
	)

	// Healthy → degrade
	d := brainerrors.Decide(err, brainerrors.DecideContext{Health: brainerrors.HealthHealthy})
	if d.Action != brainerrors.ActionDegradeBrain {
		t.Errorf("healthy: Action = %q, want degrade_brain", d.Action)
	}

	// Degraded → quarantine
	d = brainerrors.Decide(err, brainerrors.DecideContext{Health: brainerrors.HealthDegraded})
	if d.Action != brainerrors.ActionQuarantine {
		t.Errorf("degraded: Action = %q, want quarantine", d.Action)
	}

	// Quarantined → fail
	d = brainerrors.Decide(err, brainerrors.DecideContext{Health: brainerrors.HealthQuarantined})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("quarantined: Action = %q, want fail", d.Action)
	}
}

func TestDecideNilErrorViolation(t *testing.T) {
	d := brainerrors.Decide(nil, brainerrors.DecideContext{})
	if d.Action != brainerrors.ActionFail {
		t.Errorf("nil error: Action = %q, want fail", d.Action)
	}
	if !d.Violation {
		t.Error("nil error should set Violation=true")
	}
}

// ---------------------------------------------------------------------------
// 退避抖动范围 ±20%
// ---------------------------------------------------------------------------

func TestBackoffJitterRange(t *testing.T) {
	err := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("hung"),
	)
	rng := rand.New(rand.NewSource(42))
	base := 1 * time.Second
	lo := time.Duration(float64(base) * 0.8)
	hi := time.Duration(float64(base) * 1.2)

	for i := 0; i < 100; i++ {
		d := brainerrors.Decide(err, brainerrors.DecideContext{
			Attempt: 0,
			Rand:    rng,
		})
		if d.BackoffHint < lo || d.BackoffHint > hi {
			t.Errorf("attempt 0 backoff %v outside [%v, %v]", d.BackoffHint, lo, hi)
		}
	}
}

// ---------------------------------------------------------------------------
// Decision 辅助方法
// ---------------------------------------------------------------------------

func TestDecisionRetryAlias(t *testing.T) {
	d := brainerrors.Decision{Action: brainerrors.ActionRetry}
	if !d.Retry() {
		t.Error("Retry() should return true for ActionRetry")
	}
	d2 := brainerrors.Decision{Action: brainerrors.ActionFail}
	if d2.Retry() {
		t.Error("Retry() should return false for ActionFail")
	}
}
