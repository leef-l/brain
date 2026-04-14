package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/llm"
)

func TestExecuteRun_CancelledRunStaysCancelled(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          "msg_test_001",
			"type":        "message",
			"model":       "claude-sonnet-4-20250514",
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []map[string]string{
				{"type": "text", "text": "done"},
			},
			"usage": map[string]int{
				"input_tokens":                1,
				"output_tokens":               1,
				"cache_read_input_tokens":     0,
				"cache_creation_input_tokens": 0,
			},
		})
	}))
	defer provider.Close()

	runtime, err := (&fileCLIRuntimeBackend{dataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	runRec, err := runtime.RunStore.create("central", "sleep", string(modeDefault), t.TempDir())
	if err != nil {
		t.Fatalf("create run record: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &runEntry{
		ID:        runRec.ID,
		Status:    "running",
		Brain:     "central",
		Prompt:    "sleep",
		CreatedAt: time.Now().UTC(),
		cancel:    cancel,
	}
	mgr.runs.Store(entry.ID, entry)

	done := make(chan struct{})
	go func() {
		defer close(done)
		executeRun(ctx, entry, mgr, runtime, providerSession{
			Provider: llm.NewAnthropicProvider(provider.URL, "test-key", "claude-sonnet-4-20250514"),
			Name:     "anthropic",
			Model:    "claude-sonnet-4-20250514",
		}, createRunRequest{
			Prompt:   "sleep and then maybe cancel",
			Brain:    "central",
			MaxTurns: 2,
		}, runRec, nil, modeDefault)
	}()

	time.Sleep(50 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/runs/"+entry.ID, nil)
	handleCancelRun(rec, req, mgr, entry.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status=%d, want 200", rec.Code)
	}

	var cancelResp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelResp); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if got := cancelResp["status"]; got != "cancelled" {
		t.Fatalf("cancel response status=%q, want cancelled", got)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executeRun did not finish after cancellation")
	}

	snap := entry.snapshot()
	if snap.Status != "cancelled" {
		t.Fatalf("final status=%q, want cancelled", snap.Status)
	}
}

func TestHandleCreateRun_RestrictedRejectDoesNotPersistRun(t *testing.T) {
	runtime, err := (&fileCLIRuntimeBackend{dataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	body := bytes.NewBufferString(`{"prompt":"hello","brain":"central","model_config":{"provider":"mock"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", body)
	rec := httptest.NewRecorder()

	handleCreateRun(rec, req, mgr, runtime, nil, 1, modeRestricted, t.TempDir(), serveWorkdirPolicyConfined)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	if runs := runtime.RunStore.list(0, "all"); len(runs) != 0 {
		t.Fatalf("unexpected persisted runs after rejected request: %d", len(runs))
	}
}

func TestHandleCreateRun_ConcurrencyRejectDoesNotPersistRun(t *testing.T) {
	runtime, err := (&fileCLIRuntimeBackend{dataDir: t.TempDir()}).Open("central")
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}

	mgr := &runManager{store: runtime.RunStore, rootCtx: context.Background()}
	block := make(chan struct{})
	if !mgr.reserveSlot(1) {
		t.Fatal("failed to reserve initial slot")
	}
	mgr.launchReserved(&runEntry{
		ID:        "existing-run",
		Status:    "running",
		Brain:     "central",
		Prompt:    "hold",
		CreatedAt: time.Now().UTC(),
	}, func() {
		<-block
	})
	defer func() {
		close(block)
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mgr.wait(waitCtx); err != nil {
			t.Fatalf("wait running entry: %v", err)
		}
	}()

	body := bytes.NewBufferString(`{"prompt":"hello","brain":"central","model_config":{"provider":"mock"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", body)
	rec := httptest.NewRecorder()

	handleCreateRun(rec, req, mgr, runtime, nil, 1, modeDefault, t.TempDir(), serveWorkdirPolicyConfined)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rec.Code)
	}
	if runs := runtime.RunStore.list(0, "all"); len(runs) != 0 {
		t.Fatalf("unexpected persisted runs after concurrency rejection: %d", len(runs))
	}
}
