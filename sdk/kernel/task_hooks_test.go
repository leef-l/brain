package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/loop"
)

func TestLoadHookConfigMissingFile(t *testing.T) {
	cfg, err := LoadHookConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(cfg.Hooks) != 0 {
		t.Errorf("expected empty hooks, got %d", len(cfg.Hooks))
	}
}

func TestLoadHookConfigRejectsIncomplete(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte(`{"hooks":[{"command":"echo x"}]}`), 0o644)
	if _, err := LoadHookConfig(p); err == nil {
		t.Fatal("missing 'on' must error")
	}
}

func TestTransitionPublishesStateEvent(t *testing.T) {
	bus := events.NewMemEventBus()
	ch, unsub := bus.Subscribe(context.Background(), "")
	defer unsub()

	te := NewTaskExecution(TaskExecutionConfig{
		BrainID: "browser",
		Budget:  loop.Budget{},
		Bus:     bus,
	})
	if err := te.Transition(StateRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "task.state.running" {
			t.Errorf("type = %q", ev.Type)
		}
		if ev.ExecutionID != te.ID {
			t.Errorf("execution_id mismatch")
		}
		var data map[string]interface{}
		_ = json.Unmarshal(ev.Data, &data)
		if data["from"] != "pending" || data["to"] != "running" {
			t.Errorf("data = %v", data)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected event")
	}
}

func TestHookRunnerDispatchesMatchingEvents(t *testing.T) {
	// Use a temp file the hook will touch to prove it ran.
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ran.txt")

	cfg := &HookConfig{
		Hooks: []HookSpec{
			{
				On:        "task.state.completed",
				Brain:     "browser",
				Command:   "echo ok-$EXECUTION_ID > " + marker,
				TimeoutMS: 5000,
			},
		},
	}
	bus := events.NewMemEventBus()
	runner := NewHookRunner(bus, cfg, func(string, ...interface{}) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Stop()

	// 给订阅一点时间上线
	time.Sleep(20 * time.Millisecond)

	te := NewTaskExecution(TaskExecutionConfig{BrainID: "browser", Bus: bus})
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateCompleted)

	// 等 hook 执行
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			raw, _ := os.ReadFile(marker)
			if len(raw) > 0 && string(raw[:3]) == "ok-" {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("hook did not run; marker %s missing or empty", marker)
}

func TestHookRunnerFiltersByBrain(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ran.txt")
	cfg := &HookConfig{
		Hooks: []HookSpec{
			{On: "task.state.completed", Brain: "code", Command: "touch " + marker},
		},
	}
	bus := events.NewMemEventBus()
	runner := NewHookRunner(bus, cfg, func(string, ...interface{}) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = runner.Start(ctx)
	defer runner.Stop()
	time.Sleep(20 * time.Millisecond)

	// BrainID=browser,不应触发
	te := NewTaskExecution(TaskExecutionConfig{BrainID: "browser", Bus: bus})
	_ = te.Transition(StateRunning)
	_ = te.Transition(StateCompleted)

	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("hook fired for wrong brain")
	}
}
