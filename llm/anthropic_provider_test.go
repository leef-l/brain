package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Cassette replay tests — non-streaming Complete path
// ---------------------------------------------------------------------------

func TestAnthropicProvider_Complete_Text_Cassette(t *testing.T) {
	client, err := cassetteClient("complete_text")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test", WithHTTPClient(client))

	resp, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_test_001" {
		t.Errorf("ID=%q, want msg_test_001", resp.ID)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason=%q, want end_turn", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" {
		t.Fatalf("Content=%+v, want 1 text block", resp.Content)
	}
	if !strings.Contains(resp.Content[0].Text, "Hello") {
		t.Errorf("Text=%q, want contains 'Hello'", resp.Content[0].Text)
	}
	if resp.Usage.InputTokens != 12 {
		t.Errorf("InputTokens=%d, want 12", resp.Usage.InputTokens)
	}
}

func TestAnthropicProvider_Complete_ToolUse_Cassette(t *testing.T) {
	client, err := cassetteClient("complete_tool_use")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test", WithHTTPClient(client))

	resp, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Read main.go"}}}},
		Tools: []ToolSchema{{
			Name:        "code.read_file",
			Description: "Read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason=%q, want tool_use", resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content len=%d, want 2", len(resp.Content))
	}
	if resp.Content[1].Type != "tool_use" {
		t.Errorf("Content[1].Type=%q, want tool_use", resp.Content[1].Type)
	}
	if resp.Content[1].ToolName != "code.read_file" {
		t.Errorf("ToolName=%q, want code.read_file", resp.Content[1].ToolName)
	}
	if resp.Content[1].ToolUseID != "toolu_test_001" {
		t.Errorf("ToolUseID=%q, want toolu_test_001", resp.Content[1].ToolUseID)
	}
	if resp.Usage.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens=%d, want 10", resp.Usage.CacheReadTokens)
	}
}

// ---------------------------------------------------------------------------
// Cassette replay tests — error responses
// ---------------------------------------------------------------------------

func TestAnthropicProvider_Complete_Error401_Cassette(t *testing.T) {
	client, err := cassetteClient("error_401")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test", WithHTTPClient(client))

	_, err = p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error=%q, want contains '401'", err.Error())
	}
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Errorf("error=%q, want contains 'authentication_error'", err.Error())
	}
}

func TestAnthropicProvider_Complete_Error429_Cassette(t *testing.T) {
	client, err := cassetteClient("error_429")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test", WithHTTPClient(client))

	_, err = p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error=%q, want contains '429'", err.Error())
	}
}

func TestAnthropicProvider_Complete_Error500_Cassette(t *testing.T) {
	client, err := cassetteClient("error_500")
	if err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test", WithHTTPClient(client))

	_, err = p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error=%q, want contains '500'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Inline cassette tests — no file dependencies
// ---------------------------------------------------------------------------

func TestAnthropicProvider_Complete_MultipleToolUse(t *testing.T) {
	ic := newInlineCassette().addComplete(200, map[string]interface{}{
		"id":          "msg_multi_001",
		"type":        "message",
		"model":       "claude-test",
		"role":        "assistant",
		"stop_reason": "tool_use",
		"content": []map[string]interface{}{
			{"type": "text", "text": "I'll read both files."},
			{"type": "tool_use", "id": "tu1", "name": "code.read_file", "input": map[string]string{"path": "a.go"}},
			{"type": "tool_use", "id": "tu2", "name": "code.read_file", "input": map[string]string{"path": "b.go"}},
		},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
	})

	p := NewAnthropicProvider("http://fake", "sk-test", "claude-test",
		WithHTTPClient(ic.client()))

	resp, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "read a.go and b.go"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 3 {
		t.Fatalf("Content len=%d, want 3", len(resp.Content))
	}
	toolUseCount := 0
	for _, c := range resp.Content {
		if c.Type == "tool_use" {
			toolUseCount++
		}
	}
	if toolUseCount != 2 {
		t.Errorf("tool_use count=%d, want 2", toolUseCount)
	}
}

func TestAnthropicProvider_Complete_MaxTokensDefault(t *testing.T) {
	// Verify that MaxTokens defaults to 4096 when not set.
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_x", "type": "message", "model": "test",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-test", "test-model")
	_, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(capturedBody, &req); err != nil {
		t.Fatal(err)
	}
	maxTokens, ok := req["max_tokens"].(float64)
	if !ok || int(maxTokens) != 4096 {
		t.Errorf("max_tokens=%v, want 4096", req["max_tokens"])
	}
}

func TestAnthropicProvider_Complete_SystemBlocks(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_sys", "type": "message", "model": "test",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-test", "test-model")
	_, err := p.Complete(context.Background(), &ChatRequest{
		System: []SystemBlock{
			{Text: "You are a coding assistant.", Cache: true},
			{Text: "Be concise.", Cache: false},
		},
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var req map[string]json.RawMessage
	json.Unmarshal(capturedBody, &req)

	var system []map[string]interface{}
	json.Unmarshal(req["system"], &system)
	if len(system) != 2 {
		t.Fatalf("system blocks=%d, want 2", len(system))
	}
	// First block should have cache_control
	if _, ok := system[0]["cache_control"]; !ok {
		t.Error("system[0] missing cache_control")
	}
	// Second block should NOT have cache_control
	if _, ok := system[1]["cache_control"]; ok {
		t.Error("system[1] should not have cache_control")
	}
}

func TestAnthropicProvider_Complete_ToolChoice(t *testing.T) {
	cases := []struct {
		choice   string
		wantType string
		wantName string
	}{
		{"auto", "auto", ""},
		{"none", "none", ""},
		{"required", "any", ""},
		{"code.read_file", "tool", "code.read_file"},
	}

	for _, tc := range cases {
		t.Run(tc.choice, func(t *testing.T) {
			var capturedBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id": "msg_tc", "type": "message", "model": "test",
					"stop_reason": "end_turn",
					"content":     []map[string]string{{"type": "text", "text": "ok"}},
					"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
				})
			}))
			defer server.Close()

			p := NewAnthropicProvider(server.URL, "sk-test", "test-model")
			_, err := p.Complete(context.Background(), &ChatRequest{
				Messages:   []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
				ToolChoice: tc.choice,
			})
			if err != nil {
				t.Fatal(err)
			}

			var req map[string]json.RawMessage
			json.Unmarshal(capturedBody, &req)

			var toolChoice map[string]string
			json.Unmarshal(req["tool_choice"], &toolChoice)
			if toolChoice["type"] != tc.wantType {
				t.Errorf("tool_choice.type=%q, want %q", toolChoice["type"], tc.wantType)
			}
			if tc.wantName != "" && toolChoice["name"] != tc.wantName {
				t.Errorf("tool_choice.name=%q, want %q", toolChoice["name"], tc.wantName)
			}
		})
	}
}

func TestAnthropicProvider_Complete_Headers(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_h", "type": "message", "model": "test",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-my-key", "test-model")
	_, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("x-api-key"); got != "sk-my-key" {
		t.Errorf("x-api-key=%q, want sk-my-key", got)
	}
	if got := gotHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version=%q, want 2023-06-01", got)
	}
	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type=%q, want application/json", got)
	}
}

func TestAnthropicProvider_Name(t *testing.T) {
	p := NewAnthropicProvider("http://fake", "sk-test", "test")
	if p.Name() != "anthropic" {
		t.Errorf("Name()=%q, want anthropic", p.Name())
	}
}

// ---------------------------------------------------------------------------
// Stream cassette tests
// ---------------------------------------------------------------------------

func TestAnthropicProvider_Stream_TextMessage(t *testing.T) {
	sseBody := buildSSEBody([]string{
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_s1","model":"claude-test"}}`),
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`),
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`),
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`),
		sseEvent("message_stop", `{"type":"message_stop"}`),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write(sseBody)
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-test", "claude-test")
	reader, err := p.Stream(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Stream:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	var events []StreamEvent
	for {
		ev, err := reader.Next(context.Background())
		if err != nil {
			break
		}
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("no events received")
	}

	// Verify we got the expected event types
	hasStart := false
	hasDelta := false
	hasEnd := false
	for _, ev := range events {
		switch ev.Type {
		case EventMessageStart:
			hasStart = true
		case EventContentDelta:
			hasDelta = true
		case EventMessageEnd:
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("missing message.start event")
	}
	if !hasDelta {
		t.Error("missing content.delta event")
	}
	if !hasEnd {
		t.Error("missing message.end event")
	}
}

func TestAnthropicProvider_Stream_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]string{
				"type":    "rate_limit_error",
				"message": "Rate limited",
			},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-test", "claude-test")
	_, err := p.Stream(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Stream:   true,
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error=%q, want contains '429'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Cassette recorder test — verify the recording mechanism works
// ---------------------------------------------------------------------------

func TestCassetteRecorder_RoundTrip(t *testing.T) {
	// Set up a real server that returns a fixed response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_rec", "type": "message", "model": "test",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": "recorded"}},
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 3},
		})
	}))
	defer server.Close()

	// Create a recorder transport wrapping the test server's transport.
	recorder := newRecordTransport(server.Client().Transport)
	client := &http.Client{Transport: recorder}

	p := NewAnthropicProvider(server.URL, "sk-test", "test", WithHTTPClient(client))
	resp, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "msg_rec" {
		t.Errorf("ID=%q, want msg_rec", resp.ID)
	}

	// Verify recording captured the exchange.
	recorder.mu.Lock()
	recorded := recorder.recorded
	recorder.mu.Unlock()

	if len(recorded) != 1 {
		t.Fatalf("recorded=%d exchanges, want 1", len(recorded))
	}
	if recorded[0].Response.StatusCode != 200 {
		t.Errorf("recorded status=%d, want 200", recorded[0].Response.StatusCode)
	}

	// Save and reload to verify the full cassette lifecycle.
	path := t.TempDir() + "/test.cassette.json"
	if err := recorder.saveCassette("test", path); err != nil {
		t.Fatal(err)
	}
	c, err := loadCassette(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "test" {
		t.Errorf("cassette name=%q, want test", c.Name)
	}
	if len(c.Exchanges) != 1 {
		t.Errorf("cassette exchanges=%d, want 1", len(c.Exchanges))
	}

	// Replay the saved cassette.
	replayClient := &http.Client{Transport: newReplayTransport(c)}
	p2 := NewAnthropicProvider("http://fake", "sk-test", "test", WithHTTPClient(replayClient))
	resp2, err := p2.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp2.ID != "msg_rec" {
		t.Errorf("replayed ID=%q, want msg_rec", resp2.ID)
	}
}

func TestAnthropicProvider_Complete_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	p := NewAnthropicProvider("http://fake", "sk-test", "test")
	_, err := p.Complete(ctx, &ChatRequest{
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestAnthropicProvider_Complete_ToolResultMessage(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg_tr", "type": "message", "model": "test",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": "done"}},
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	p := NewAnthropicProvider(server.URL, "sk-test", "test")
	_, err := p.Complete(context.Background(), &ChatRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "read a.go"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ToolUseID: "tu1", ToolName: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
			}},
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "tu1", Output: json.RawMessage(`"file contents"`)},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the request body contains all 3 messages with correct structure.
	var req struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content json.RawMessage   `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages=%d, want 3", len(req.Messages))
	}

	// Check the tool_result message
	var blocks []map[string]interface{}
	json.Unmarshal(req.Messages[2].Content, &blocks)
	if len(blocks) == 0 {
		t.Fatal("no content blocks in tool_result message")
	}
	if blocks[0]["type"] != "tool_result" {
		t.Errorf("type=%v, want tool_result", blocks[0]["type"])
	}
	if blocks[0]["tool_use_id"] != "tu1" {
		t.Errorf("tool_use_id=%v, want tu1", blocks[0]["tool_use_id"])
	}
}
