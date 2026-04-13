package skeleton

import (
	"context"
	"testing"

	"github.com/leef-l/brain/llm"
)

// ---------------------------------------------------------------------------
// MockProvider — Complete 往返
// ---------------------------------------------------------------------------

func TestMockProviderComplete(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("hello world")

	resp, err := mp.Complete(context.Background(), &llm.ChatRequest{
		RunID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) == 0 {
		t.Fatal("response should have content")
	}
	if resp.Content[0].Text != "hello world" {
		t.Errorf("text = %q", resp.Content[0].Text)
	}
}

// ---------------------------------------------------------------------------
// MockProvider — 空队列报错
// ---------------------------------------------------------------------------

func TestMockProviderEmptyQueue(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	_, err := mp.Complete(context.Background(), &llm.ChatRequest{})
	if err == nil {
		t.Fatal("empty queue should return error")
	}
}

// ---------------------------------------------------------------------------
// MockProvider — FIFO 顺序
// ---------------------------------------------------------------------------

func TestMockProviderFIFO(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("first")
	mp.QueueText("second")
	mp.QueueText("third")

	for _, want := range []string{"first", "second", "third"} {
		resp, err := mp.Complete(context.Background(), &llm.ChatRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content[0].Text != want {
			t.Errorf("got %q, want %q", resp.Content[0].Text, want)
		}
	}
}

// ---------------------------------------------------------------------------
// MockProvider — Reset
// ---------------------------------------------------------------------------

func TestMockProviderReset(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("queued")
	mp.Complete(context.Background(), &llm.ChatRequest{})
	mp.Reset()

	if len(mp.Requests()) != 0 {
		t.Error("Reset should clear request log")
	}

	_, err := mp.Complete(context.Background(), &llm.ChatRequest{})
	if err == nil {
		t.Error("queue should be empty after Reset")
	}
}

// ---------------------------------------------------------------------------
// MockProvider — Name
// ---------------------------------------------------------------------------

func TestMockProviderName(t *testing.T) {
	mp := llm.NewMockProvider("claude-3")
	if mp.Name() != "claude-3" {
		t.Errorf("Name = %q", mp.Name())
	}
}

// ---------------------------------------------------------------------------
// MockProvider — Stream 事件顺序
// ---------------------------------------------------------------------------

func TestMockProviderStreamEventOrder(t *testing.T) {
	mp := llm.NewMockProvider("test-model")
	mp.QueueText("streamed")

	reader, err := mp.Stream(context.Background(), &llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	var events []llm.StreamEventType
	for {
		ev, err := reader.Next(context.Background())
		if err != nil {
			break
		}
		events = append(events, ev.Type)
		if ev.Type == llm.EventMessageEnd {
			break
		}
	}

	if len(events) < 3 {
		t.Fatalf("events len = %d, want >= 3", len(events))
	}
	if events[0] != llm.EventMessageStart {
		t.Errorf("first event = %q, want message.start", events[0])
	}
	if events[len(events)-1] != llm.EventMessageEnd {
		t.Errorf("last event = %q, want message.end", events[len(events)-1])
	}
}

// ---------------------------------------------------------------------------
// Usage 字段
// ---------------------------------------------------------------------------

func TestUsageFields(t *testing.T) {
	u := llm.Usage{
		InputTokens:        100,
		OutputTokens:       50,
		CacheCreationTokens: 10,
		CacheReadTokens:    20,
		CostUSD:            0.005,
	}
	if u.InputTokens != 100 {
		t.Errorf("InputTokens = %d", u.InputTokens)
	}
	if u.CostUSD != 0.005 {
		t.Errorf("CostUSD = %f", u.CostUSD)
	}
}

// ---------------------------------------------------------------------------
// BudgetSnapshot 字段
// ---------------------------------------------------------------------------

func TestBudgetSnapshotFields(t *testing.T) {
	snap := llm.BudgetSnapshot{
		TurnsRemaining:   5,
		CostUSDRemaining: 2.5,
		TokensRemaining:  1000,
	}
	if snap.TurnsRemaining != 5 {
		t.Errorf("TurnsRemaining = %d", snap.TurnsRemaining)
	}
}

// ---------------------------------------------------------------------------
// StreamEventType 常量
// ---------------------------------------------------------------------------

func TestStreamEventTypeConstants(t *testing.T) {
	types := []llm.StreamEventType{
		llm.EventMessageStart,
		llm.EventContentDelta,
		llm.EventToolCallDelta,
		llm.EventMessageDelta,
		llm.EventMessageEnd,
	}
	seen := make(map[llm.StreamEventType]bool)
	for _, et := range types {
		if et == "" {
			t.Error("StreamEventType should not be empty")
		}
		if seen[et] {
			t.Errorf("duplicate StreamEventType: %q", et)
		}
		seen[et] = true
	}
}
