package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
)

// mockStreamReader yields a fixed sequence of events and then io.EOF.
type mockStreamReader struct {
	events []llm.StreamEvent
	cursor int
}

func (r *mockStreamReader) Next(ctx context.Context) (llm.StreamEvent, error) {
	if r.cursor >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.cursor]
	r.cursor++
	return ev, nil
}

func (r *mockStreamReader) Close() error { return nil }

// mockStreamProvider is an llm.Provider that returns a fixed stream.
type mockStreamProvider struct {
	events []llm.StreamEvent
}

func (p *mockStreamProvider) Name() string { return "mock-stream" }

func (p *mockStreamProvider) Complete(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("not implemented")
}

func (p *mockStreamProvider) Stream(ctx context.Context, req *llm.ChatRequest) (llm.StreamReader, error) {
	return &mockStreamReader{events: p.events}, nil
}

// mockBidirRPC is a minimal BidirRPC implementation for testing.
type mockBidirRPC struct {
	notifies []struct {
		Method string
		Params interface{}
	}
	handlers map[string]protocol.HandlerFunc
}

func newMockBidirRPC() *mockBidirRPC {
	return &mockBidirRPC{handlers: make(map[string]protocol.HandlerFunc)}
}

func (m *mockBidirRPC) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	return nil
}

func (m *mockBidirRPC) Notify(ctx context.Context, method string, params interface{}) error {
	m.notifies = append(m.notifies, struct {
		Method string
		Params interface{}
	}{Method: method, Params: params})
	return nil
}

func (m *mockBidirRPC) Handle(method string, handler protocol.HandlerFunc) {
	m.handlers[method] = handler
}

func (m *mockBidirRPC) HandlerExists(method string) bool {
	_, ok := m.handlers[method]
	return ok
}

func (m *mockBidirRPC) Start(ctx context.Context) error { return nil }
func (m *mockBidirRPC) Close() error                    { return nil }
func (m *mockBidirRPC) Done() <-chan struct{}           { return nil }

func TestLLMProxy_handleStream_PublishesToEventBus(t *testing.T) {
	streamEvents := []llm.StreamEvent{
		{Type: llm.EventMessageStart, Data: mustJSON(map[string]string{"id": "msg-1", "model": "mock"})},
		{Type: llm.EventContentDelta, Data: mustJSON(map[string]string{"text": "h", "kind": "text"})},
		{Type: llm.EventContentDelta, Data: mustJSON(map[string]string{"text": "i", "kind": "text"})},
		{Type: llm.EventMessageDelta, Data: mustJSON(map[string]string{"stop_reason": "end_turn"})},
		{Type: llm.EventMessageEnd, Data: mustJSON(map[string]interface{}{"usage": map[string]int{"input_tokens": 1, "output_tokens": 2}})},
	}

	provider := &mockStreamProvider{events: streamEvents}
	bus := events.NewMemEventBus()
	proxy := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
		EventBus:        bus,
	}

	rpc := newMockBidirRPC()
	params, _ := json.Marshal(llmCompleteRequest{
		Messages:    []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}}},
		StreamID:    "stream-1",
		ExecutionID: "exec-test-1",
	})

	// Subscribe before handleStream so we catch all published events.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, subCancel := bus.Subscribe(ctx, "exec-test-1")
	defer subCancel()

	_, err := proxy.handleStream(context.Background(), agent.KindCode, params, rpc)
	if err != nil {
		t.Fatalf("handleStream: %v", err)
	}

	// Verify that llm/stream/delta notifications were sent to the sidecar.
	if len(rpc.notifies) == 0 {
		t.Fatal("expected at least one Notify to sidecar, got none")
	}
	for _, n := range rpc.notifies {
		if n.Method != protocol.MethodLLMStreamDelta {
			t.Errorf("notify method = %q, want %q", n.Method, protocol.MethodLLMStreamDelta)
		}
	}
	if len(rpc.notifies) != len(streamEvents) {
		t.Errorf("notify count = %d, want %d", len(rpc.notifies), len(streamEvents))
	}

	// Collect published events from the event bus.
	var gotEvents int
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			if ev.ExecutionID != "exec-test-1" {
				t.Errorf("execution_id = %q, want exec-test-1", ev.ExecutionID)
			}
			if ev.Type == "" {
				t.Error("event type is empty")
			}
			gotEvents++
		case <-time.After(200 * time.Millisecond):
			goto done
		}
	}
done:
	if gotEvents != len(streamEvents) {
		t.Fatalf("expected %d events in event bus, got %d", len(streamEvents), gotEvents)
	}
}

func TestLLMProxy_handleStream_WithoutEventBus(t *testing.T) {
	streamEvents := []llm.StreamEvent{
		{Type: llm.EventMessageStart, Data: mustJSON(map[string]string{"id": "msg-2"})},
		{Type: llm.EventMessageEnd, Data: mustJSON(map[string]interface{}{"usage": map[string]int{}})},
	}

	provider := &mockStreamProvider{events: streamEvents}
	proxy := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
		// EventBus is nil — should not panic.
	}

	rpc := newMockBidirRPC()
	params, _ := json.Marshal(llmCompleteRequest{
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "test"}}}},
		StreamID: "stream-2",
	})

	_, err := proxy.handleStream(context.Background(), agent.KindCode, params, rpc)
	if err != nil {
		t.Fatalf("handleStream: %v", err)
	}

	if len(rpc.notifies) != len(streamEvents) {
		t.Fatalf("expected %d notifies to sidecar, got %d", len(streamEvents), len(rpc.notifies))
	}
}

func TestLLMProxy_handleStream_MapsThinkingDelta(t *testing.T) {
	streamEvents := []llm.StreamEvent{
		{Type: llm.EventContentDelta, Data: mustJSON(map[string]string{"text": "think", "kind": "thinking"})},
	}

	provider := &mockStreamProvider{events: streamEvents}
	bus := events.NewMemEventBus()
	proxy := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
		EventBus:        bus,
	}

	rpc := newMockBidirRPC()
	params, _ := json.Marshal(llmCompleteRequest{
		Messages:    []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "test"}}}},
		StreamID:    "stream-3",
		ExecutionID: "exec-thinking",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, subCancel := bus.Subscribe(ctx, "exec-thinking")
	defer subCancel()

	_, err := proxy.handleStream(context.Background(), agent.KindCode, params, rpc)
	if err != nil {
		t.Fatalf("handleStream: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "llm.thinking_delta" {
			t.Errorf("event type = %q, want llm.thinking_delta", ev.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected thinking_delta event in event bus")
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}
