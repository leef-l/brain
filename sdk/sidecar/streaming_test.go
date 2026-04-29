package sidecar

import (
	"context"
	"io"
	"testing"

	"github.com/leef-l/brain/sdk/llm"
)

func TestChannelStreamReader_RealTimeEvents(t *testing.T) {
	streamID := generateStreamID()
	ch := make(chan llm.StreamEvent, 4)
	registerStreamChan(streamID, ch)
	defer unregisterStreamChan(streamID)

	reader := &channelStreamReader{ch: ch}

	// Push events simulating host-side real-time deltas
	pushStreamEvent(streamID, llm.StreamEvent{
		Type: llm.EventMessageStart,
		Data: []byte(`{"id":"test-1","model":"mock","role":"assistant"}`),
	})
	pushStreamEvent(streamID, llm.StreamEvent{
		Type: llm.EventContentDelta,
		Data: []byte(`{"text":"hello","kind":"text"}`),
	})
	pushStreamEvent(streamID, llm.StreamEvent{
		Type: llm.EventMessageEnd,
		Data: []byte(`{}`),
	})
	close(ch)

	ctx := context.Background()

	// 1. message.start
	ev, err := reader.Next(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != llm.EventMessageStart {
		t.Fatalf("first event type=%q, want message.start", ev.Type)
	}

	// 2. content.delta
	ev, err = reader.Next(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != llm.EventContentDelta {
		t.Fatalf("second event type=%q, want content.delta", ev.Type)
	}

	// 3. message.end
	ev, err = reader.Next(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != llm.EventMessageEnd {
		t.Fatalf("third event type=%q, want message.end", ev.Type)
	}

	// 4. EOF after channel close
	_, err = reader.Next(ctx)
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestPushStreamEvent_UnknownStream(t *testing.T) {
	// Pushing to an unregistered stream should not panic
	pushStreamEvent("nonexistent", llm.StreamEvent{
		Type: llm.EventContentDelta,
		Data: []byte(`{"text":"x"}`),
	})
}
