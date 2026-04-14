package braintesting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/persistence"
)

func loadSignalTraceFixture(t *testing.T) *persistence.SignalTrace {
	t.Helper()

	path := filepath.Join("..", "test", "fixtures", "quant", "signal_trace.paper.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}

	var trace persistence.SignalTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	return &trace
}

func TestSignalTracePersistence_E2E(t *testing.T) {
	ctx := context.Background()
	fixture := loadSignalTraceFixture(t)

	mem := persistence.NewMemSignalTraceStore(nil)
	if err := mem.Save(ctx, fixture); err != nil {
		t.Fatalf("mem Save: %v", err)
	}
	memRows, err := mem.Query(ctx, persistence.SignalTraceFilter{Symbol: fixture.Symbol, Limit: 1})
	if err != nil {
		t.Fatalf("mem Query: %v", err)
	}
	if len(memRows) != 1 {
		t.Fatalf("mem rows=%d, want 1", len(memRows))
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "brain.json")
	fileStore, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	if err := fileStore.SignalTraceStore().Save(ctx, fixture); err != nil {
		t.Fatalf("file Save: %v", err)
	}
	reopened, err := persistence.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rows, err := reopened.SignalTraceStore().Query(ctx, persistence.SignalTraceFilter{Symbol: fixture.Symbol, Limit: 1})
	if err != nil {
		t.Fatalf("file Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("file rows=%d, want 1", len(rows))
	}
	if rows[0].TraceID != fixture.TraceID {
		t.Fatalf("TraceID=%q, want %q", rows[0].TraceID, fixture.TraceID)
	}
}
