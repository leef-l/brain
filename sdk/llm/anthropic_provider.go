package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AnthropicProvider implements Provider for the Anthropic Messages API.
// It is compatible with any API endpoint that speaks the same wire format,
// including Tencent Cloud Coding Plan (api.lkeap.cloud.tencent.com).
//
// Zero third-party dependencies — uses only net/http + encoding/json.
type AnthropicProvider struct {
	baseURL    string
	authToken  string
	model      string
	httpClient *http.Client
}

// AnthropicOption configures an AnthropicProvider.
type AnthropicOption func(*AnthropicProvider)

// WithHTTPClient sets a custom http.Client (useful for testing).
func WithHTTPClient(c *http.Client) AnthropicOption {
	return func(p *AnthropicProvider) { p.httpClient = c }
}

// NewAnthropicProvider creates a provider that talks to the Anthropic
// Messages API (or a compatible endpoint).
//
//   - baseURL: e.g. "https://api.anthropic.com" or
//     "https://api.lkeap.cloud.tencent.com/coding/anthropic"
//   - authToken: the API key / bearer token
//   - model: the model identifier (e.g. "claude-sonnet-4-20250514", "glm5")
func NewAnthropicProvider(baseURL, authToken, model string, opts ...AnthropicOption) *AnthropicProvider {
	p := &AnthropicProvider{
		baseURL:   strings.TrimRight(baseURL, "/"),
		authToken: authToken,
		model:     model,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// ---------------------------------------------------------------------------
// Complete — non-streaming
// ---------------------------------------------------------------------------

func (p *AnthropicProvider) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	apiReq := p.buildAPIRequest(req, false)
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.readAPIError(resp)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read response: %w", err)
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	respOut, err := p.toResponse(&apiResp, respBody)
	if err != nil {
		return nil, err
	}
	return respOut, nil
}

// ---------------------------------------------------------------------------
// Stream — SSE-based streaming
// ---------------------------------------------------------------------------

func (p *AnthropicProvider) Stream(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	apiReq := p.buildAPIRequest(req, true)
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.readAPIError(resp)
	}

	return newSSEReader(resp.Body), nil
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type anthropicRequest struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     json.RawMessage    `json:"system,omitempty"`
	Messages   []anthropicMessage `json:"messages"`
	Tools      []anthropicToolDef `json:"tools,omitempty"`
	ToolChoice json.RawMessage    `json:"tool_choice,omitempty"`
	Stream     bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (p *AnthropicProvider) buildAPIRequest(req *ChatRequest, stream bool) *anthropicRequest {
	ar := &anthropicRequest{
		Model:     p.model,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
	if req.Model != "" {
		ar.Model = req.Model
	}
	if ar.MaxTokens <= 0 {
		ar.MaxTokens = 4096
	}

	// System blocks → Anthropic system parameter
	if len(req.System) > 0 {
		var blocks []map[string]interface{}
		for _, sb := range req.System {
			block := map[string]interface{}{
				"type": "text",
				"text": sb.Text,
			}
			if sb.Cache {
				block["cache_control"] = map[string]string{"type": "ephemeral"}
			}
			blocks = append(blocks, block)
		}
		ar.System, _ = json.Marshal(blocks)
	}

	// Messages
	for _, m := range req.Messages {
		content := p.buildContentBlocks(m.Content)
		raw, _ := json.Marshal(content)
		ar.Messages = append(ar.Messages, anthropicMessage{
			Role:    m.Role,
			Content: raw,
		})
	}

	// Tools — some API proxies (e.g. OneAPI/New API) reject dots in tool
	// names. We replace "." with "__" on the wire and reverse the mapping
	// when parsing tool_use responses.
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, anthropicToolDef{
			Name:        sanitizeToolName(t.Name),
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// ToolChoice
	if req.ToolChoice != "" {
		switch req.ToolChoice {
		case "auto", "required", "none":
			tc := map[string]string{"type": req.ToolChoice}
			if req.ToolChoice == "required" {
				tc["type"] = "any"
			}
			ar.ToolChoice, _ = json.Marshal(tc)
		default:
			tc := map[string]string{"type": "tool", "name": sanitizeToolName(req.ToolChoice)}
			ar.ToolChoice, _ = json.Marshal(tc)
		}
	}

	return ar
}

func (p *AnthropicProvider) buildContentBlocks(blocks []ContentBlock) []map[string]interface{} {
	var out []map[string]interface{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, map[string]interface{}{
				"type": "text",
				"text": b.Text,
			})
		case "tool_use":
			block := map[string]interface{}{
				"type": "tool_use",
				"id":   b.ToolUseID,
				"name": sanitizeToolName(b.ToolName),
			}
			if b.Input != nil {
				block["input"] = json.RawMessage(b.Input)
			} else {
				block["input"] = json.RawMessage("{}")
			}
			out = append(out, block)
		case "tool_result":
			block := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": b.ToolUseID,
			}
			if b.Output != nil {
				block["content"] = string(b.Output)
			}
			if b.IsError {
				block["is_error"] = true
			}
			out = append(out, block)
		}
	}
	if len(out) == 0 {
		return []map[string]interface{}{{"type": "text", "text": ""}}
	}
	return out
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

type anthropicResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (p *AnthropicProvider) toResponse(ar *anthropicResponse, rawBody []byte) (*ChatResponse, error) {
	resp := &ChatResponse{
		ID:         ar.ID,
		Model:      ar.Model,
		StopReason: ar.StopReason,
		Usage: Usage{
			InputTokens:         ar.Usage.InputTokens,
			OutputTokens:        ar.Usage.OutputTokens,
			CacheReadTokens:     ar.Usage.CacheReadTokens,
			CacheCreationTokens: ar.Usage.CacheCreationTokens,
		},
		FinishedAt: time.Now(),
	}

	var rawBlocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(ar.Content, &rawBlocks); err == nil {
		for _, rb := range rawBlocks {
			cb := ContentBlock{Type: rb.Type}
			switch rb.Type {
			case "text":
				cb.Text = rb.Text
			case "tool_use":
				toolName := rb.Name
				if strings.TrimSpace(toolName) == "" {
					toolName = rb.ToolName
				}
				cb.ToolUseID = rb.ID
				cb.ToolName = restoreToolName(toolName)
				cb.Input = rb.Input
			}
			resp.Content = append(resp.Content, cb)
		}
	}
	if err := ValidateToolUseResponse("anthropic", resp); err != nil {
		return nil, fmt.Errorf("%w; body=%s", err, string(rawBody))
	}

	return resp, nil
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

type anthropicAPIError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiErr anthropicAPIError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		return classifyHTTPError("anthropic", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}
	return classifyHTTPError("anthropic", resp.StatusCode, "", string(body))
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.authToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
}

// ---------------------------------------------------------------------------
// SSE Stream Reader
// ---------------------------------------------------------------------------

type sseReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	closed  bool
	toolUse map[int]*streamToolUse
}

type streamToolUse struct {
	toolUseID string
	toolName  string
	input     strings.Builder
}

func newSSEReader(body io.ReadCloser) *sseReader {
	return &sseReader{
		body:    body,
		scanner: bufio.NewScanner(body),
		toolUse: make(map[int]*streamToolUse),
	}
}

func (r *sseReader) Next(ctx context.Context) (StreamEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return StreamEvent{}, io.EOF
	}

	for {
		select {
		case <-ctx.Done():
			return StreamEvent{}, ctx.Err()
		default:
		}

		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return StreamEvent{}, fmt.Errorf("anthropic: stream scan: %w", err)
			}
			return StreamEvent{}, io.EOF
		}

		line := r.scanner.Text()

		// SSE format: "event: <type>\ndata: <json>\n\n"
		if strings.HasPrefix(line, "event: ") {
			eventType := strings.TrimPrefix(line, "event: ")

			// Read data line
			if !r.scanner.Scan() {
				return StreamEvent{}, io.EOF
			}
			dataLine := r.scanner.Text()
			var data json.RawMessage
			if strings.HasPrefix(dataLine, "data: ") {
				data = json.RawMessage(strings.TrimPrefix(dataLine, "data: "))
			}

			ev, ok := r.mapSSEEvent(eventType, data)
			if ok {
				return ev, nil
			}
			continue
		}

		// Some providers send "data: " lines directly
		if strings.HasPrefix(line, "data: ") {
			raw := strings.TrimPrefix(line, "data: ")
			if raw == "[DONE]" {
				return StreamEvent{Type: EventMessageEnd}, nil
			}

			var envelope struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(raw), &envelope) == nil {
				ev, ok := r.mapSSEEvent(envelope.Type, json.RawMessage(raw))
				if ok {
					return ev, nil
				}
			}
		}
	}
}

func (r *sseReader) mapSSEEvent(eventType string, data json.RawMessage) (StreamEvent, bool) {
	switch eventType {
	case "message_start":
		var start struct {
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &start) == nil {
			return StreamEvent{
				Type: EventMessageStart,
				Data: marshalRaw(map[string]string{
					"id":    start.Message.ID,
					"model": start.Message.Model,
				}),
			}, true
		}
		return StreamEvent{Type: EventMessageStart, Data: data}, true
	case "content_block_start":
		var start struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				ToolName string          `json:"tool_name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content_block"`
		}
		if json.Unmarshal(data, &start) != nil {
			return StreamEvent{}, false
		}
		switch start.ContentBlock.Type {
		case "text":
			if start.ContentBlock.Text == "" {
				return StreamEvent{}, false
			}
			return StreamEvent{
				Type: EventContentDelta,
				Data: marshalRaw(map[string]string{"text": start.ContentBlock.Text}),
			}, true
		case "tool_use":
			toolName := start.ContentBlock.Name
			if strings.TrimSpace(toolName) == "" {
				toolName = start.ContentBlock.ToolName
			}
			block := &streamToolUse{
				toolUseID: start.ContentBlock.ID,
				toolName:  restoreToolName(toolName),
			}
			input := strings.TrimSpace(string(start.ContentBlock.Input))
			if input != "" && input != "null" {
				block.input.WriteString(input)
			}
			r.toolUse[start.Index] = block
			return StreamEvent{}, false
		default:
			return StreamEvent{}, false
		}
	case "content_block_delta":
		var delta struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal(data, &delta) != nil {
			return StreamEvent{}, false
		}
		switch delta.Delta.Type {
		case "text_delta":
			return StreamEvent{
				Type: EventContentDelta,
				Data: marshalRaw(map[string]string{"text": delta.Delta.Text}),
			}, true
		case "input_json_delta":
			if block := r.toolUse[delta.Index]; block != nil {
				block.input.WriteString(delta.Delta.PartialJSON)
			}
			return StreamEvent{}, false
		default:
			return StreamEvent{}, false
		}
	case "content_block_stop":
		var stop struct {
			Index int `json:"index"`
		}
		if json.Unmarshal(data, &stop) != nil {
			return StreamEvent{}, false
		}
		block := r.toolUse[stop.Index]
		if block == nil {
			return StreamEvent{}, false
		}
		delete(r.toolUse, stop.Index)

		input := strings.TrimSpace(block.input.String())
		if input == "" {
			input = "{}"
		}

		return StreamEvent{
			Type: EventToolCallDelta,
			Data: marshalRaw(map[string]interface{}{
				"tool_use_id": block.toolUseID,
				"tool_name":   block.toolName,
				"input":       json.RawMessage(input),
			}),
		}, true
	case "message_delta":
		var delta struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage Usage `json:"usage"`
		}
		if json.Unmarshal(data, &delta) == nil {
			return StreamEvent{
				Type: EventMessageDelta,
				Data: marshalRaw(struct {
					StopReason string `json:"stop_reason,omitempty"`
					Usage      Usage  `json:"usage,omitempty"`
				}{
					StopReason: delta.Delta.StopReason,
					Usage:      delta.Usage,
				}),
			}, true
		}
		return StreamEvent{Type: EventMessageDelta, Data: data}, true
	case "message_stop":
		return StreamEvent{Type: EventMessageEnd, Data: data}, true
	case "ping":
		return StreamEvent{}, false // skip
	case "error":
		return StreamEvent{Type: EventMessageEnd, Data: data}, true
	default:
		return StreamEvent{}, false
	}
}

func marshalRaw(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func (r *sseReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}

// sanitizeToolName replaces dots with double underscores for API proxies
// (e.g. OneAPI/New API) that reject dots in tool names.
func sanitizeToolName(name string) string {
	return strings.ReplaceAll(name, ".", "__")
}

// restoreToolName reverses sanitizeToolName.
func restoreToolName(name string) string {
	return strings.ReplaceAll(name, "__", ".")
}
