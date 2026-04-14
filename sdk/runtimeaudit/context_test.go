package runtimeaudit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

type collectSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *collectSink) AppendEvent(_ context.Context, ev Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func TestWithSinkAndEmit(t *testing.T) {
	sink := &collectSink{}
	ctx := WithSink(context.Background(), sink)
	Emit(ctx, Event{Type: "test", Message: "hello"})

	if len(sink.events) != 1 {
		t.Fatalf("got %d events, want 1", len(sink.events))
	}
	if sink.events[0].Type != "test" {
		t.Errorf("Type = %q, want %q", sink.events[0].Type, "test")
	}
	if sink.events[0].Message != "hello" {
		t.Errorf("Message = %q, want %q", sink.events[0].Message, "hello")
	}
}

func TestEmitWithoutSink(t *testing.T) {
	// Should not panic
	Emit(context.Background(), Event{Type: "test", Message: "noop"})
}

func TestEmitNilContext(t *testing.T) {
	// Should not panic
	Emit(nil, Event{Type: "test"})
}

func TestWithSinkNilInputs(t *testing.T) {
	ctx := context.Background()
	// nil sink returns original ctx
	got := WithSink(ctx, nil)
	if got != ctx {
		t.Error("WithSink(ctx, nil) should return original ctx")
	}
	// nil ctx returns nil
	got = WithSink(nil, &collectSink{})
	if got != nil {
		t.Error("WithSink(nil, sink) should return nil")
	}
}

func TestSinkFunc(t *testing.T) {
	var called bool
	fn := SinkFunc(func(_ context.Context, ev Event) {
		called = true
		if ev.Type != "func_test" {
			t.Errorf("Type = %q", ev.Type)
		}
	})
	fn.AppendEvent(context.Background(), Event{Type: "func_test"})
	if !called {
		t.Error("SinkFunc was not called")
	}
}

func TestSinkFuncNil(t *testing.T) {
	var fn SinkFunc
	// nil SinkFunc should not panic
	fn.AppendEvent(context.Background(), Event{Type: "test"})
}

func TestEventData(t *testing.T) {
	data := json.RawMessage(`{"key":"value"}`)
	sink := &collectSink{}
	ctx := WithSink(context.Background(), sink)
	Emit(ctx, Event{Type: "data_test", Message: "msg", Data: data})

	if len(sink.events) != 1 {
		t.Fatal("expected 1 event")
	}
	var m map[string]string
	if err := json.Unmarshal(sink.events[0].Data, &m); err != nil {
		t.Fatal(err)
	}
	if m["key"] != "value" {
		t.Errorf("Data key = %q", m["key"])
	}
}

func TestMultipleEmits(t *testing.T) {
	sink := &collectSink{}
	ctx := WithSink(context.Background(), sink)
	for i := 0; i < 10; i++ {
		Emit(ctx, Event{Type: "multi", Message: "msg"})
	}
	if len(sink.events) != 10 {
		t.Errorf("got %d events, want 10", len(sink.events))
	}
}
