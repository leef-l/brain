package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	brainerrors "github.com/leef-l/brain/errors"
)

func TestMockProvider_CompleteRoundTrip(t *testing.T) {
	p := NewMockProvider("mock")
	p.QueueText("hello world")
	resp, err := p.Complete(context.Background(), &ChatRequest{RunID: "r1", TurnIndex: 1})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "hello world" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("want stop_reason=end_turn, got %q", resp.StopReason)
	}
	if got := p.Requests(); len(got) != 1 || got[0].RunID != "r1" {
		t.Fatalf("request log not captured: %+v", got)
	}
}

func TestMockProvider_CompleteEmptyQueue(t *testing.T) {
	p := NewMockProvider("mock")
	_, err := p.Complete(context.Background(), &ChatRequest{})
	if err == nil {
		t.Fatal("expected error on empty queue")
	}
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("want BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeLLMUpstream5xx {
		t.Fatalf("want %s, got %s", brainerrors.CodeLLMUpstream5xx, be.ErrorCode)
	}
}

func TestMockProvider_CompleteFIFOOrder(t *testing.T) {
	p := NewMockProvider("mock")
	p.QueueText("one")
	p.QueueText("two")
	p.QueueText("three")
	for _, want := range []string{"one", "two", "three"} {
		resp, err := p.Complete(context.Background(), &ChatRequest{})
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if resp.Content[0].Text != want {
			t.Fatalf("order broken: want %q got %q", want, resp.Content[0].Text)
		}
	}
}

func TestMockProvider_QueueToolUse(t *testing.T) {
	p := NewMockProvider("mock")
	p.QueueToolUse("code.echo", json.RawMessage(`{"text":"hi"}`))
	resp, err := p.Complete(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("want tool_use stop, got %q", resp.StopReason)
	}
	if resp.Content[0].Type != "tool_use" || resp.Content[0].ToolName != "code.echo" {
		t.Fatalf("unexpected content: %+v", resp.Content[0])
	}
}

func TestMockProvider_StreamEventOrder(t *testing.T) {
	p := NewMockProvider("mock", WithMockStreamChunkSize(3))
	p.QueueText("hello")
	rd, err := p.Stream(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()

	var seen []StreamEventType
	var textBuf string
	for {
		ev, err := rd.Next(context.Background())
		if err != nil {
			// Mock signals exhaustion via CodeUnknown — break out of loop.
			var be *brainerrors.BrainError
			if errors.As(err, &be) && be.ErrorCode == brainerrors.CodeUnknown {
				break
			}
			t.Fatalf("unexpected stream error: %v", err)
		}
		seen = append(seen, ev.Type)
		if ev.Type == EventContentDelta {
			var frag struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Data, &frag); err != nil {
				t.Fatalf("bad delta payload: %v", err)
			}
			textBuf += frag.Text
		}
	}
	if textBuf != "hello" {
		t.Fatalf("stream text reassembly: got %q", textBuf)
	}

	// Canonical order per 22 §7: message.start → content.delta* → message.delta → message.end.
	if len(seen) < 4 {
		t.Fatalf("too few events: %v", seen)
	}
	if seen[0] != EventMessageStart {
		t.Fatalf("want message.start first, got %s", seen[0])
	}
	if seen[len(seen)-1] != EventMessageEnd {
		t.Fatalf("want message.end last, got %s", seen[len(seen)-1])
	}
	if seen[len(seen)-2] != EventMessageDelta {
		t.Fatalf("want message.delta just before end, got %s", seen[len(seen)-2])
	}
}

func TestMockProvider_StreamToolCall(t *testing.T) {
	p := NewMockProvider("mock")
	p.QueueToolUse("code.echo", json.RawMessage(`{"x":1}`))
	rd, err := p.Stream(context.Background(), &ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()

	var sawToolDelta bool
	for {
		ev, err := rd.Next(context.Background())
		if err != nil {
			break
		}
		if ev.Type == EventToolCallDelta {
			sawToolDelta = true
		}
	}
	if !sawToolDelta {
		t.Fatal("expected at least one tool_call.delta event")
	}
}

func TestMockProvider_Reset(t *testing.T) {
	p := NewMockProvider("mock")
	p.QueueText("a")
	_, _ = p.Complete(context.Background(), &ChatRequest{RunID: "r1"})
	if len(p.Requests()) != 1 {
		t.Fatalf("expected 1 request before reset, got %d", len(p.Requests()))
	}
	p.Reset()
	if len(p.Requests()) != 0 {
		t.Fatalf("expected reset to clear requests, got %d", len(p.Requests()))
	}
	// Queue is also cleared → next Complete must fail.
	if _, err := p.Complete(context.Background(), &ChatRequest{}); err == nil {
		t.Fatal("expected empty-queue error after reset")
	}
}

func TestSplitRunes(t *testing.T) {
	cases := []struct {
		in        string
		chunkSize int
		want      []string
	}{
		{"", 3, nil},
		{"abcde", 0, []string{"a", "b", "c", "d", "e"}},
		{"abcde", 1, []string{"a", "b", "c", "d", "e"}},
		{"abcde", 2, []string{"ab", "cd", "e"}},
		{"abcde", 10, []string{"abcde"}},
		{"你好世界", 2, []string{"你好", "世界"}},
	}
	for _, c := range cases {
		got := splitRunes(c.in, c.chunkSize)
		if len(got) != len(c.want) {
			t.Errorf("splitRunes(%q,%d): len=%d want %d", c.in, c.chunkSize, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitRunes(%q,%d)[%d]=%q want %q", c.in, c.chunkSize, i, got[i], c.want[i])
			}
		}
	}
}
