package central

import (
	"context"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

func TestNewCentralBrainLearner(t *testing.T) {
	cl := NewCentralBrainLearner()
	if cl == nil {
		t.Fatal("expected non-nil CentralBrainLearner")
	}
	if cl.taskCount != 0 {
		t.Fatalf("expected taskCount=0, got %d", cl.taskCount)
	}
	if cl.successCount != 0 {
		t.Fatalf("expected successCount=0, got %d", cl.successCount)
	}
	if cl.latencyEWMA.Value != 0 {
		t.Fatalf("expected latencyEWMA.Value=0, got %f", cl.latencyEWMA.Value)
	}
}

func TestCentralBrainLearnerRecordOutcome(t *testing.T) {
	cl := NewCentralBrainLearner()
	ctx := context.Background()

	// Record a successful outcome.
	if err := cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: true, Duration: 100 * time.Millisecond}); err != nil {
		t.Fatalf("RecordOutcome failed: %v", err)
	}
	if cl.taskCount != 1 {
		t.Fatalf("expected taskCount=1, got %d", cl.taskCount)
	}
	if cl.successCount != 1 {
		t.Fatalf("expected successCount=1, got %d", cl.successCount)
	}

	// Record a failed outcome.
	if err := cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: false, Duration: 200 * time.Millisecond}); err != nil {
		t.Fatalf("RecordOutcome failed: %v", err)
	}
	if cl.taskCount != 2 {
		t.Fatalf("expected taskCount=2, got %d", cl.taskCount)
	}
	if cl.successCount != 1 {
		t.Fatalf("expected successCount=1, got %d", cl.successCount)
	}
}

func TestCentralBrainLearnerRecordOutcomeDelegate(t *testing.T) {
	cl := NewCentralBrainLearner()
	ctx := context.Background()

	// Record a successful delegate outcome.
	if err := cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: true, TaskType: "delegate", Duration: 100 * time.Millisecond}); err != nil {
		t.Fatalf("RecordOutcome failed: %v", err)
	}
	if cl.delegateOk != 1 {
		t.Fatalf("expected delegateOk=1, got %d", cl.delegateOk)
	}
	if cl.delegateFail != 0 {
		t.Fatalf("expected delegateFail=0, got %d", cl.delegateFail)
	}

	// Record a failed delegate outcome.
	if err := cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: false, TaskType: "delegate", Duration: 100 * time.Millisecond}); err != nil {
		t.Fatalf("RecordOutcome failed: %v", err)
	}
	if cl.delegateOk != 1 {
		t.Fatalf("expected delegateOk=1, got %d", cl.delegateOk)
	}
	if cl.delegateFail != 1 {
		t.Fatalf("expected delegateFail=1, got %d", cl.delegateFail)
	}
}

func TestCentralBrainLearnerExportMetrics(t *testing.T) {
	cl := NewCentralBrainLearner()
	ctx := context.Background()

	// Before any outcomes.
	m := cl.ExportMetrics()
	if m.BrainKind != agent.KindCentral {
		t.Fatalf("expected BrainKind=%s, got %s", agent.KindCentral, m.BrainKind)
	}
	if m.TaskCount != 0 {
		t.Fatalf("expected TaskCount=0, got %d", m.TaskCount)
	}
	if m.SuccessRate != 0 {
		t.Fatalf("expected SuccessRate=0, got %f", m.SuccessRate)
	}

	// After mixed outcomes including delegates to drive ConfidenceTrend.
	_ = cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: true, TaskType: "delegate", Duration: 100 * time.Millisecond})
	_ = cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: true, TaskType: "delegate", Duration: 200 * time.Millisecond})
	_ = cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: false, TaskType: "delegate", Duration: 300 * time.Millisecond})

	m = cl.ExportMetrics()
	if m.TaskCount != 3 {
		t.Fatalf("expected TaskCount=3, got %d", m.TaskCount)
	}
	if m.SuccessRate != 2.0/3.0 {
		t.Fatalf("expected SuccessRate=%f, got %f", 2.0/3.0, m.SuccessRate)
	}
	if m.AvgLatencyMs <= 0 {
		t.Fatalf("expected AvgLatencyMs > 0, got %f", m.AvgLatencyMs)
	}
	if m.ConfidenceTrend == 0 {
		t.Fatalf("expected non-zero ConfidenceTrend, got %f", m.ConfidenceTrend)
	}
}

func TestCentralBrainLearnerAdapt(t *testing.T) {
	cl := NewCentralBrainLearner()
	if err := cl.Adapt(context.Background()); err != nil {
		t.Fatalf("Adapt should return nil, got %v", err)
	}
}

func TestCentralBrainLearnerConcurrency(t *testing.T) {
	cl := NewCentralBrainLearner()
	ctx := context.Background()

	// Concurrent writes to verify mutex protection.
	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func(success bool) {
			_ = cl.RecordOutcome(ctx, kernel.TaskOutcome{Success: success, Duration: 10 * time.Millisecond})
			done <- struct{}{}
		}(i%2 == 0)
	}
	for i := 0; i < 100; i++ {
		<-done
	}

	m := cl.ExportMetrics()
	if m.TaskCount != 100 {
		t.Fatalf("expected TaskCount=100, got %d", m.TaskCount)
	}
	if m.SuccessRate != 0.5 {
		t.Fatalf("expected SuccessRate=0.5, got %f", m.SuccessRate)
	}
}
