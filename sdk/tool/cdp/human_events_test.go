package cdp

import (
	"context"
	"testing"
	"time"
)

// MemoryEventSource 是 P3.3 的测试事件源。这组测试保证它的 Start/Stop/Push
// 语义稳:多次 Start 幂等、Stop 后 Push 不 panic、已 Stop 再 Start 返回错。

func TestMemoryEventSourceStartReturnsChannel(t *testing.T) {
	src := NewMemoryEventSource(4)
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ch == nil {
		t.Fatal("nil channel from Start")
	}
	// Push → 能在 channel 里读出来
	src.Push(HumanEvent{Kind: HumanEventClick, BrainID: 1})
	select {
	case ev := <-ch:
		if ev.Kind != HumanEventClick || ev.BrainID != 1 {
			t.Errorf("event mismatch: %+v", ev)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for pushed event")
	}
	_ = src.Stop()
}

func TestMemoryEventSourceStopCloses(t *testing.T) {
	src := NewMemoryEventSource(2)
	ch, _ := src.Start(context.Background())
	src.Push(HumanEvent{Kind: HumanEventInput})
	_ = src.Stop()

	// Drain
	got := 0
	for range ch {
		got++
	}
	if got != 1 {
		t.Errorf("want 1 drained event before close, got %d", got)
	}

	// Push after Stop 不 panic
	src.Push(HumanEvent{Kind: HumanEventClick})

	// 再 Stop 幂等
	if err := src.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestMemoryEventSourceStartAfterStopErrors(t *testing.T) {
	src := NewMemoryEventSource(1)
	_, _ = src.Start(context.Background())
	_ = src.Stop()
	if _, err := src.Start(context.Background()); err == nil {
		t.Errorf("Start after Stop should error")
	}
}

func TestMemoryEventSourcePushFillsTimestamp(t *testing.T) {
	src := NewMemoryEventSource(1)
	ch, _ := src.Start(context.Background())
	src.Push(HumanEvent{Kind: HumanEventSubmit})
	ev := <-ch
	if ev.Timestamp.IsZero() {
		t.Errorf("Push should auto-fill timestamp on zero value")
	}
	_ = src.Stop()
}
