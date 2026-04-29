package braintesting

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// makeEvent is a helper that builds a simple CassetteEvent for tests.
func makeEvent(t *testing.T, evType string, i int) CassetteEvent {
	t.Helper()
	payload, _ := json.Marshal(map[string]int{"seq": i})
	return CassetteEvent{
		Type:      evType,
		Timestamp: time.Now().UTC(),
		Payload:   json.RawMessage(payload),
	}
}

// TestRecorder_RecordBeforeStart verifies that Record returns an error when
// called without a prior Start.
func TestRecorder_RecordBeforeStart(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileCassetteRecorder(dir)
	if err != nil {
		t.Fatalf("NewFileCassetteRecorder: %v", err)
	}
	err = rec.Record(context.Background(), makeEvent(t, "llm_request", 0))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Errorf("expected CodeInvariantViolated, got %q", be.ErrorCode)
	}
}

// TestRecorder_NormalFlow verifies that Start/Record/Finish succeed and
// produce a cassette file.
func TestRecorder_NormalFlow(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileCassetteRecorder(dir)
	if err != nil {
		t.Fatalf("NewFileCassetteRecorder: %v", err)
	}
	ctx := context.Background()

	if err := rec.Start(ctx, "normal"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := rec.Record(ctx, makeEvent(t, "llm_request", i)); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}
	if err := rec.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// TestRecorder_DuplicateStart verifies that a second Start call returns
// CodeInvariantViolated.
func TestRecorder_DuplicateStart(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewFileCassetteRecorder(dir)
	if err != nil {
		t.Fatalf("NewFileCassetteRecorder: %v", err)
	}
	ctx := context.Background()

	if err := rec.Start(ctx, "dup"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	err = rec.Start(ctx, "dup2")
	if err == nil {
		t.Fatal("expected error on second Start, got nil")
	}
	_ = rec.Finish(ctx)
	be, ok := err.(*brainerrors.BrainError)
	if !ok || be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Errorf("expected CodeInvariantViolated, got %v", err)
	}
}

// TestRecorder_InvalidNames verifies that invalid cassette names are rejected
// with CodeToolInputInvalid.
func TestRecorder_InvalidNames(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"", "empty"},
		{"/absolute", "absolute path"},
		{"../traversal", "double-dot traversal"},
		{"foo/../bar", "embedded double-dot"},
		{"has space", "space in name"},
		{"has!bang", "bang character"},
	}
	dir := t.TempDir()
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rec, err := NewFileCassetteRecorder(dir)
			if err != nil {
				t.Fatalf("NewFileCassetteRecorder: %v", err)
			}
			err = rec.Start(ctx, tc.name)
			if err == nil {
				t.Fatalf("expected error for name %q, got nil", tc.name)
			}
			be, ok := err.(*brainerrors.BrainError)
			if !ok || be.ErrorCode != brainerrors.CodeToolInputInvalid {
				t.Errorf("name=%q: expected CodeToolInputInvalid, got %v", tc.name, err)
			}
		})
	}
}

// TestRoundTrip writes 10 events with a recorder then reads them back with a
// player, verifying the payload matches exactly.
func TestRoundTrip(t *testing.T) {
	const cassName = "round-trip"
	dir := t.TempDir()
	ctx := context.Background()

	rec, err := NewFileCassetteRecorder(dir)
	if err != nil {
		t.Fatalf("NewFileCassetteRecorder: %v", err)
	}
	if err := rec.Start(ctx, cassName); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const n = 10
	var sent []CassetteEvent
	for i := 0; i < n; i++ {
		ev := makeEvent(t, "tool_call", i)
		sent = append(sent, ev)
		if err := rec.Record(ctx, ev); err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}
	if err := rec.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	player := NewFileCassettePlayer(dir)
	if err := player.Load(ctx, cassName); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for i := 0; i < n; i++ {
		got, err := player.Next(ctx)
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		if got.Type != sent[i].Type {
			t.Errorf("[%d] Type: want %q, got %q", i, sent[i].Type, got.Type)
		}
		if string(got.Payload) != string(sent[i].Payload) {
			t.Errorf("[%d] Payload: want %s, got %s", i, sent[i].Payload, got.Payload)
		}
	}
}

// TestPlayer_Rewind verifies that Rewind resets the cursor to position 0.
func TestPlayer_Rewind(t *testing.T) {
	const cassName = "rewind-test"
	dir := t.TempDir()
	ctx := context.Background()

	rec, _ := NewFileCassetteRecorder(dir)
	_ = rec.Start(ctx, cassName)
	for i := 0; i < 3; i++ {
		_ = rec.Record(ctx, makeEvent(t, "llm_response", i))
	}
	_ = rec.Finish(ctx)

	player := NewFileCassettePlayer(dir)
	if err := player.Load(ctx, cassName); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first, _ := player.Next(ctx)
	_ = first
	if err := player.Rewind(ctx); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	// After rewind, Next should return the first event again.
	afterRewind, err := player.Next(ctx)
	if err != nil {
		t.Fatalf("Next after Rewind: %v", err)
	}
	if afterRewind.Type != first.Type || string(afterRewind.Payload) != string(first.Payload) {
		t.Errorf("Rewind did not reset cursor: got %v, want %v", afterRewind, first)
	}
}

// TestPlayer_NextEOF verifies that Next returns CodeRecordNotFound once the
// cassette is exhausted.
func TestPlayer_NextEOF(t *testing.T) {
	const cassName = "eof-test"
	dir := t.TempDir()
	ctx := context.Background()

	rec, _ := NewFileCassetteRecorder(dir)
	_ = rec.Start(ctx, cassName)
	_ = rec.Record(ctx, makeEvent(t, "tool_result", 0))
	_ = rec.Finish(ctx)

	player := NewFileCassettePlayer(dir)
	_ = player.Load(ctx, cassName)
	_, _ = player.Next(ctx) // consume the one event

	_, err := player.Next(ctx)
	if err == nil {
		t.Fatal("expected EOF error, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok || be.ErrorCode != brainerrors.CodeRecordNotFound {
		t.Errorf("expected CodeRecordNotFound, got %v", err)
	}
}

// TestPlayer_NextBeforeLoad verifies that Next before Load returns
// CodeInvariantViolated.
func TestPlayer_NextBeforeLoad(t *testing.T) {
	player := NewFileCassettePlayer(t.TempDir())
	_, err := player.Next(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok || be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Errorf("expected CodeInvariantViolated, got %v", err)
	}
}

// TestRecorder_ConcurrentRecord launches 5 goroutines each recording 20
// events. The mutex in FileCassetteRecorder must prevent races and all 100
// events should appear in the cassette.
func TestRecorder_ConcurrentRecord(t *testing.T) {
	const cassName = "concurrent"
	dir := t.TempDir()
	ctx := context.Background()

	rec, err := NewFileCassetteRecorder(dir)
	if err != nil {
		t.Fatalf("NewFileCassetteRecorder: %v", err)
	}
	if err := rec.Start(ctx, cassName); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const goroutines = 5
	const eventsEach = 20
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsEach; i++ {
				payload, _ := json.Marshal(map[string]int{"g": g, "i": i})
				ev := CassetteEvent{
					Type:      fmt.Sprintf("tool_call_%d", g),
					Timestamp: time.Now().UTC(),
					Payload:   json.RawMessage(payload),
				}
				if err := rec.Record(ctx, ev); err != nil {
					errs[g] = err
					return
				}
			}
		}()
	}
	wg.Wait()

	for g, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: Record error: %v", g, e)
		}
	}

	if err := rec.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Verify total event count via the player.
	player := NewFileCassettePlayer(dir)
	if err := player.Load(ctx, cassName); err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	for {
		_, err := player.Next(ctx)
		if err != nil {
			break
		}
		count++
	}
	expected := goroutines * eventsEach
	if count != expected {
		t.Errorf("expected %d events, got %d", expected, count)
	}
}
