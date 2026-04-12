package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MockProvider is a deterministic, stdlib-only llm.Provider used by unit
// tests, `brain doctor` smoke checks, and the v0.1.0 reference Kernel. It
// replays a fixed queue of ChatResponse fixtures in FIFO order and is
// capable of emulating both the non-streaming Complete path and the
// five-event streaming contract from 22-Agent-Loop规格.md §5/§7.
//
// Typical use:
//
//	p := llm.NewMockProvider("mock")
//	p.QueueText("hello world")
//	resp, err := p.Complete(ctx, req)
//
// or, for streaming:
//
//	p := llm.NewMockProvider("mock")
//	p.QueueText("hello world")
//	rd, err := p.Stream(ctx, req)
//	// rd yields message.start → content.delta (one per char) → message.delta → message.end
//
// MockProvider also exposes a recorder API so tests can assert the
// ChatRequest the loop produced. See 28-SDK交付规范.md §9 "testable by
// default".
type MockProvider struct {
	name string

	mu       sync.Mutex
	queue    []*ChatResponse
	requests []*ChatRequest
	// streamChunkSize controls how many runes are emitted per content.delta
	// event in Stream. 0 means "one rune per delta" (finest granularity).
	streamChunkSize int
}

// MockProviderOption configures a MockProvider at construction.
type MockProviderOption func(*MockProvider)

// WithMockStreamChunkSize controls the content.delta granularity of the
// Stream path: chunkSize runes per event. 0 or negative disables chunking
// and emits one rune per event.
func WithMockStreamChunkSize(chunkSize int) MockProviderOption {
	return func(p *MockProvider) {
		if chunkSize > 0 {
			p.streamChunkSize = chunkSize
		}
	}
}

// NewMockProvider constructs a MockProvider with the given provider name.
// Name is returned verbatim from Name().
func NewMockProvider(name string, opts ...MockProviderOption) *MockProvider {
	p := &MockProvider{name: name}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the provider identifier passed to NewMockProvider.
func (p *MockProvider) Name() string { return p.name }

// Queue appends a fully-formed ChatResponse to the FIFO queue. Tests that
// need to control every field (StopReason, Usage, tool_use blocks, ...)
// SHOULD use Queue directly.
func (p *MockProvider) Queue(resp *ChatResponse) {
	if resp == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, resp)
}

// QueueText is a convenience for the common "emit a plain text message"
// case. It appends a ChatResponse whose only content block is a single
// text block with the given body. StopReason is set to "end_turn".
func (p *MockProvider) QueueText(text string) {
	p.Queue(&ChatResponse{
		ID:         "mock-" + randHex8(),
		Model:      "mock-model",
		StopReason: "end_turn",
		Content: []ContentBlock{
			{Type: "text", Text: text},
		},
		Usage:      Usage{InputTokens: len(text) / 4, OutputTokens: len(text) / 4},
		FinishedAt: time.Unix(0, 0).UTC(),
	})
}

// QueueToolUse is a convenience for scripting a tool_use response in the
// fixture queue. The Kernel's loop will see a single tool_use content
// block with the given tool name and JSON input.
func (p *MockProvider) QueueToolUse(toolName string, input json.RawMessage) {
	p.Queue(&ChatResponse{
		ID:         "mock-" + randHex8(),
		Model:      "mock-model",
		StopReason: "tool_use",
		Content: []ContentBlock{
			{
				Type:      "tool_use",
				ToolUseID: "mock-tu-" + randHex8(),
				ToolName:  toolName,
				Input:     input,
			},
		},
		Usage:      Usage{InputTokens: 1, OutputTokens: 1},
		FinishedAt: time.Unix(0, 0).UTC(),
	})
}

// Requests returns a snapshot of every ChatRequest the MockProvider has
// seen, in arrival order. The returned slice is a shallow copy safe for
// read-only inspection.
func (p *MockProvider) Requests() []*ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*ChatRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

// Reset clears both the queue and the request log. Useful inside table-
// driven tests that reuse a single MockProvider across cases.
func (p *MockProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = nil
	p.requests = nil
}

// Complete implements llm.Provider.Complete by popping the next queued
// ChatResponse. An empty queue returns CodeLLMUpstream5xx with the
// literal detail "mock: response queue empty" so callers can distinguish
// fixture shortfall from legitimate provider failures.
func (p *MockProvider) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("mock: context cancelled: "+err.Error()))
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	if len(p.queue) == 0 {
		p.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeLLMUpstream5xx,
			brainerrors.WithMessage("mock: response queue empty"))
	}
	resp := p.queue[0]
	p.queue = p.queue[1:]
	p.mu.Unlock()

	// Clone defensively so callers cannot tamper with queued fixtures.
	out := *resp
	out.Content = append([]ContentBlock(nil), resp.Content...)
	if out.FinishedAt.IsZero() {
		out.FinishedAt = time.Now().UTC()
	}
	return &out, nil
}

// Stream implements llm.Provider.Stream by popping the next queued
// ChatResponse and lowering it into the v1 five-event stream schema from
// 22-Agent-Loop规格.md §5/§7.
//
// Event order, per §7:
//  1. message.start
//  2. content.delta* (one per streamChunkSize-sized text chunk) and/or
//     tool_call.delta* (one per tool_use block)
//  3. message.delta (carrying stop_reason + usage snapshot)
//  4. message.end (with final usage)
//
// The returned StreamReader is buffered; Close is idempotent.
func (p *MockProvider) Stream(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("mock: context cancelled: "+err.Error()))
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	if len(p.queue) == 0 {
		p.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeLLMUpstream5xx,
			brainerrors.WithMessage("mock: response queue empty"))
	}
	resp := p.queue[0]
	p.queue = p.queue[1:]
	chunkSize := p.streamChunkSize
	p.mu.Unlock()

	events := lowerToStream(resp, chunkSize)
	return &mockStreamReader{events: events}, nil
}

// lowerToStream converts a ChatResponse into a concrete slice of
// StreamEvents following the frozen v1 order from §7. Pure function,
// safe to call outside the mutex.
func lowerToStream(resp *ChatResponse, chunkSize int) []StreamEvent {
	var events []StreamEvent

	// 1. message.start — empty payload; the Runner uses it to create its
	//    per-turn buffer.
	events = append(events, StreamEvent{
		Type: EventMessageStart,
		Data: mustJSON(map[string]string{"id": resp.ID, "model": resp.Model}),
	})

	// 2. Fan out each content block into deltas.
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			for _, chunk := range splitRunes(block.Text, chunkSize) {
				events = append(events, StreamEvent{
					Type: EventContentDelta,
					Data: mustJSON(map[string]string{"text": chunk}),
				})
			}
		case "tool_use":
			events = append(events, StreamEvent{
				Type: EventToolCallDelta,
				Data: mustJSON(map[string]interface{}{
					"tool_use_id": block.ToolUseID,
					"tool_name":   block.ToolName,
					"input":       block.Input,
				}),
			})
		}
	}

	// 3. message.delta — carries stop_reason + a running usage snapshot.
	events = append(events, StreamEvent{
		Type: EventMessageDelta,
		Data: mustJSON(map[string]interface{}{
			"stop_reason": resp.StopReason,
			"usage":       resp.Usage,
		}),
	})

	// 4. message.end — final usage; the Runner stamps the turn complete.
	events = append(events, StreamEvent{
		Type: EventMessageEnd,
		Data: mustJSON(map[string]interface{}{
			"usage": resp.Usage,
		}),
	})

	return events
}

// splitRunes splits s into runes grouped by chunkSize. chunkSize <= 0
// means "one rune per chunk" (max granularity). Empty string yields an
// empty slice (the caller skips delta emission entirely in that case).
func splitRunes(s string, chunkSize int) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	if chunkSize <= 0 {
		chunkSize = 1
	}
	out := make([]string, 0, (len(runes)+chunkSize-1)/chunkSize)
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

// mockStreamReader yields a fixed slice of StreamEvents and then returns
// io.EOF wrapped as a BrainError per the Provider contract.
type mockStreamReader struct {
	mu     sync.Mutex
	events []StreamEvent
	cursor int
	closed bool
}

func (r *mockStreamReader) Next(ctx context.Context) (StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return StreamEvent{}, brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("mock: context cancelled: "+err.Error()))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return StreamEvent{}, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("mock: Next on closed stream"))
	}
	if r.cursor >= len(r.events) {
		// End of stream — the Provider contract wraps io.EOF as a
		// BrainError so callers never unwrap a bare error sentinel.
		return StreamEvent{}, brainerrors.New(brainerrors.CodeUnknown,
			brainerrors.WithMessage("mock: stream exhausted (EOF)"))
	}
	ev := r.events[r.cursor]
	r.cursor++
	return ev, nil
}

func (r *mockStreamReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// --- helpers shared across mock_provider.go ---

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshalling only fails for pathological input (channels, cycles);
		// a deliberate panic is fine in the mock path.
		panic("brain/llm: mockProvider mustJSON failed: " + err.Error())
	}
	return b
}

// randHex8 returns 8 lowercase hex chars backed by crypto/rand.
func randHex8() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms.
		panic("brain/llm: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
