package main

import (
	"testing"
	"time"
)

func TestEffectiveRunMaxDuration_ZeroDisablesLoopBudgetTimeout(t *testing.T) {
	if got := effectiveRunMaxDuration(0, 5*time.Minute); got != 0 {
		t.Fatalf("effectiveRunMaxDuration(0, 5m) = %s, want 0", got)
	}
}

func TestManagedRunBudget_UsesRequestedMaxDuration(t *testing.T) {
	budget := managedRunBudget(4, 30*time.Minute)
	if budget.MaxDuration != 30*time.Minute {
		t.Fatalf("budget.MaxDuration = %s, want 30m", budget.MaxDuration)
	}
}

func TestManagedRunBudget_AllowsDisabledTimeout(t *testing.T) {
	budget := managedRunBudget(4, 0)
	if budget.MaxDuration != 0 {
		t.Fatalf("budget.MaxDuration = %s, want 0", budget.MaxDuration)
	}
}
