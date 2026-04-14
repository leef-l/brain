package recovery

import (
	"context"
	"testing"

	qexec "github.com/leef-l/brain/internal/quant/execution"
)

func TestManagerReconcile(t *testing.T) {
	adapter := qexec.NewPaperExecutorAdapter("acct-a")
	adapter.MarkRecovering("acct-a", "restart")

	manager := NewManager(adapter)
	report, err := manager.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovered != 1 {
		t.Fatalf("recovered=%d, want 1", report.Recovered)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results=%d, want 1", len(report.Results))
	}
	if report.Results[0].Status != qexec.AccountStatusActive {
		t.Fatalf("status=%q, want active", report.Results[0].Status)
	}

	snap, ok := adapter.Account("acct-a")
	if !ok {
		t.Fatal("account snapshot missing")
	}
	if snap.Status != qexec.AccountStatusActive {
		t.Fatalf("snapshot status=%q, want active", snap.Status)
	}
}
