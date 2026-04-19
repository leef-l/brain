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

// OpenAIProvider implements Provider for OpenAI-compatible APIs, including
// DeepSeek, Qwen, Doubao, and any endpoint that speaks the
// /v1/chat/completions wire format.
//
// Zero third-party dependencies — uses only net/http + encoding/json.
type OpenAIProvider struct {
	baseURL    string
	authToken  string
	model      string
	httpClient *http.Client
}

// OpenAIOption configures an OpenAIProvider.
type OpenAIOption func(*OpenAIProvider)

// WithOpenAIHTTPClient sets a custom http.Client (useful for testing).
func WithOpenAIHTTPClient(c *http.Client) OpenAIOption {
	return func(p *OpenAIProvider) { p.httpClient = c }
}

// NewOpenAIProvider creates a provider that talks to an OpenAI-compatible
// chat completions endpoint.
//
//   - baseURL: e.g. "https://api.deepseek.com" or "https://api.openai.com"
//   - authToken: the API key / bearer token
//   - model: the model identifier (e.g. "deepseek-chat", "deepseek-reasoner")
func NewOpenAIProvider(baseURL, authToken, model string, opts ...OpenAIOption) *OpenAIProvider {
	p := &OpenAIProvider{
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

func (p *OpenAIProvider) Name() string { return "openai" }

// ---------------------------------------------------------------------------
// Complete — non-streaming
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) Complete(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	apiReq := p.buildAPIRequest(req, false)
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, p.readAPIError(resp)
	}

	var apiResp openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	return p.toResponse(&apiResp)
}

// ---------------------------------------------------------------------------
// Stream — SSE-based streaming
// ---------------------------------------------------------------------------

func (p *OpenAIProvider) Stream(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	apiReq := p.buildAPIRequest(req, true)
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.readAPIError(resp)
	}

	return newOpenAISSEReader(resp.Body), nil
}

// ---------------------------------------------------------------------------
// Request building
// ---------------------------------------------------------------------------

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Tools       []openaiToolDef `json:"tools,omitempty"`
	ToolChoice  interface{}     `json:"tool_choice,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function openaiToolFnCall `json:"function"`
}

type openaiToolFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiToolDef struct {
	Type     string         `json:"type"`
	Function openaiToolFunc `json:"function"`
}

type openaiToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (p *OpenAIProvider) buildAPIRequest(req *ChatRequest, stream bool) *openaiRequest {
	ar := &openaiRequest{
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

	// System blocks → system message
	if len(req.System) > 0 {
		var sb strings.Builder
		for i, s := range req.System {
			if i > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(s.Text)
		}
		ar.Messages = append(ar.Messages, openaiMessage{
			Role:    "system",
			Content: sb.String(),
		})
	}

	// Messages — convert from Brain internal format to OpenAI format.
	// OpenAI requires tool_result blocks as separate role=tool messages.
	ar.Messages = append(ar.Messages, p.convertMessages(req.Messages)...)

	// Tools
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, openaiToolDef{
			Type: "function",
			Function: openaiToolFunc{
				Name:        sanitizeToolName(t.Name),
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	// ToolChoice
	if req.ToolChoice != "" {
		switch req.ToolChoice {
		case "auto", "none", "required":
			ar.ToolChoice = req.ToolChoice
		default:
			ar.ToolChoice = map[string]interface{}{
				"type": "function",
				"function": map[string]string{
					"name": sanitizeToolName(req.ToolChoice),
				},
			}
		}
	}

	return ar
}

func (p *OpenAIProvider) buildAssistantMessage(m Message) openaiMessage {
	msg := openaiMessage{Role: "assistant"}

	var textParts []string
	var toolCalls []openaiToolCall

	for _, b := range m.Content {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking":
			// OpenAI protocol has no "thinking" role — merge into text so
			// the reasoning trace is preserved in conversation history.
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "tool_use":
			args := "{}"
			if b.Input != nil {
				args = string(b.Input)
			}
			toolCalls = append(toolCalls, openaiToolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: openaiToolFnCall{
					Name:      sanitizeToolName(b.ToolName),
					Arguments: args,
				},
			})
		}
	}

	if len(textParts) > 0 {
		msg.Content = strings.Join(textParts, "\n")
	}
	// OpenAI requires content field for assistant messages — some compatible
	// APIs reject the request if content is missing entirely.
	if msg.Content == nil {
		msg.Content = ""
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	return msg
}

// buildMessages handles the full conversion including tool_result → role=tool
// mapping that OpenAI requires.
func (p *OpenAIProvider) convertMessages(messages []Message) []openaiMessage {
	var out []openaiMessage

	for _, m := range messages {
		switch m.Role {
		case "user":
			// Split: text parts go as user message, tool_results go as tool messages
			var textParts []string
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					textParts = append(textParts, b.Text)
				case "tool_result":
					content := ""
					if b.Output != nil {
						content = string(b.Output)
					}
					out = append(out, openaiMessage{
						Role:       "tool",
						Content:    content,
						ToolCallID: b.ToolUseID,
					})
				}
			}
			if len(textParts) > 0 {
				out = append(out, openaiMessage{
					Role:    "user",
					Content: strings.Join(textParts, "\n"),
				})
			}
		case "assistant":
			out = append(out, p.buildAssistantMessage(m))
		}
	}

	return sanitizeToolCallSequence(out)
}

// sanitizeToolCallSequence ensures every assistant message with tool_calls is
// immediately followed by exactly one role=tool message per tool_call_id.
// If tool result messages are missing (e.g. due to a prior error or cancel),
// placeholder results are injected so the OpenAI API doesn't reject the
// request with HTTP 400.
func sanitizeToolCallSequence(msgs []openaiMessage) []openaiMessage {
	var result []openaiMessage

	for i := 0; i < len(msgs); i++ {
		msg := msgs[i]
		result = append(result, msg)

		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}

		// Collect the set of tool_call IDs that need results.
		needed := make(map[string]bool, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			needed[tc.ID] = true
		}

		// Consume following role=tool messages that match.
		for i+1 < len(msgs) && msgs[i+1].Role == "tool" {
			i++
			result = append(result, msgs[i])
			delete(needed, msgs[i].ToolCallID)
		}

		// Inject placeholder results for any missing tool_call IDs.
		for _, tc := range msg.ToolCalls {
			if needed[tc.ID] {
				result = append(result, openaiMessage{
					Role:       "tool",
					Content:    `"tool call was not executed"`,
					ToolCallID: tc.ID,
				})
			}
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

type openaiResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int          `json:"index"`
		Message      openaiChoice `json:"message"`
		FinishReason string       `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openaiChoice struct {
	Role             string           `json:"role"`
	Content          *string          `json:"content"`
	ReasoningContent *string          `json:"reasoning_content"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

func (p *OpenAIProvider) toResponse(ar *openaiResponse) (*ChatResponse, error) {
	resp := &ChatResponse{
		ID:    ar.ID,
		Model: ar.Model,
		Usage: Usage{
			InputTokens:  ar.Usage.PromptTokens,
			OutputTokens: ar.Usage.CompletionTokens,
		},
		FinishedAt: time.Now(),
	}

	if len(ar.Choices) > 0 {
		choice := ar.Choices[0]

		// Normalize stop reason
		switch choice.FinishReason {
		case "stop":
			resp.StopReason = "end_turn"
		case "tool_calls":
			resp.StopReason = "tool_use"
		case "length":
			resp.StopReason = "max_tokens"
		default:
			resp.StopReason = choice.FinishReason
		}

		// Reasoning content (DeepSeek-R1 / reasoner models)
		if choice.Message.ReasoningContent != nil && *choice.Message.ReasoningContent != "" {
			resp.Content = append(resp.Content, ContentBlock{
				Type: "thinking",
				Text: *choice.Message.ReasoningContent,
			})
		}

		// Text content
		if choice.Message.Content != nil && *choice.Message.Content != "" {
			resp.Content = append(resp.Content, ContentBlock{
				Type: "text",
				Text: *choice.Message.Content,
			})
		}

		// Tool calls
		for _, tc := range choice.Message.ToolCalls {
			resp.Content = append(resp.Content, ContentBlock{
				Type:      "tool_use",
				ToolUseID: tc.ID,
				ToolName:  restoreToolName(tc.Function.Name),
				Input:     json.RawMessage(tc.Function.Arguments),
			})
		}
	}

	if err := ValidateToolUseResponse("openai", resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

type openaiAPIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (p *OpenAIProvider) readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiErr openaiAPIError
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		return classifyHTTPError("openai", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
	}
	return classifyHTTPError("openai", resp.StatusCode, "", string(body))
}

func (p *OpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.authToken)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
}

// ---------------------------------------------------------------------------
// SSE Stream Reader (OpenAI format)
// ---------------------------------------------------------------------------

type openaiSSEReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	closed  bool
	toolUse map[int]*openaiStreamToolUse
	pending []StreamEvent // queued events to emit on subsequent Next() calls
}

type openaiStreamToolUse struct {
	toolCallID string
	toolName   string
	arguments  strings.Builder
}

func newOpenAISSEReader(body io.ReadCloser) *openaiSSEReader {
	return &openaiSSEReader{
		body:    body,
		scanner: bufio.NewScanner(body),
		toolUse: make(map[int]*openaiStreamToolUse),
	}
}

func (r *openaiSSEReader) Next(ctx context.Context) (StreamEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return StreamEvent{}, io.EOF
	}

	// Drain any queued events from a previous multi-tool-call flush.
	if len(r.pending) > 0 {
		ev := r.pending[0]
		r.pending = r.pending[1:]
		return ev, nil
	}

	for {
		select {
		case <-ctx.Done():
			return StreamEvent{}, ctx.Err()
		default:
		}

		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return StreamEvent{}, fmt.Errorf("openai: stream scan: %w", err)
			}
			// Flush any pending tool calls
			if ev, ok := r.flushToolCalls(); ok {
				return ev, nil // will be followed by EOF on next call
			}
			return StreamEvent{}, io.EOF
		}

		line := r.scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			// Flush pending tool calls before ending
			if ev, ok := r.flushToolCalls(); ok {
				return ev, nil
			}
			return StreamEvent{Type: EventMessageEnd}, nil
		}

		var chunk openaiStreamChunk
		if json.Unmarshal([]byte(raw), &chunk) != nil {
			continue
		}

		ev, ok := r.mapChunk(&chunk)
		if ok {
			return ev, nil
		}
	}
}

type openaiStreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string           `json:"role,omitempty"`
			Content          *string          `json:"content,omitempty"`
			ReasoningContent *string          `json:"reasoning_content,omitempty"`
			ToolCalls        []openaiStreamTC `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

type openaiStreamTC struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

func (r *openaiSSEReader) mapChunk(chunk *openaiStreamChunk) (StreamEvent, bool) {
	if len(chunk.Choices) == 0 {
		// Usage-only chunk at the end
		if chunk.Usage != nil {
			return StreamEvent{
				Type: EventMessageDelta,
				Data: marshalRaw(struct {
					Usage Usage `json:"usage"`
				}{
					Usage: Usage{
						InputTokens:  chunk.Usage.PromptTokens,
						OutputTokens: chunk.Usage.CompletionTokens,
					},
				}),
			}, true
		}
		return StreamEvent{}, false
	}

	delta := chunk.Choices[0].Delta
	finishReason := chunk.Choices[0].FinishReason

	// Role announcement → message start
	if delta.Role == "assistant" && delta.Content == nil && len(delta.ToolCalls) == 0 {
		return StreamEvent{
			Type: EventMessageStart,
			Data: marshalRaw(map[string]string{
				"id":    chunk.ID,
				"model": chunk.Model,
			}),
		}, true
	}

	// Reasoning content delta (DeepSeek-R1 / reasoner models)
	if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
		return StreamEvent{
			Type: EventContentDelta,
			Data: marshalRaw(map[string]string{"text": *delta.ReasoningContent}),
		}, true
	}

	// Text content delta
	if delta.Content != nil && *delta.Content != "" {
		return StreamEvent{
			Type: EventContentDelta,
			Data: marshalRaw(map[string]string{"text": *delta.Content}),
		}, true
	}

	// Tool call deltas
	for _, tc := range delta.ToolCalls {
		block, exists := r.toolUse[tc.Index]
		if !exists {
			block = &openaiStreamToolUse{
				toolCallID: tc.ID,
				toolName:   restoreToolName(tc.Function.Name),
			}
			r.toolUse[tc.Index] = block
		}
		if tc.Function.Arguments != "" {
			block.arguments.WriteString(tc.Function.Arguments)
		}
	}

	// Finish reason
	if finishReason != nil {
		stopReason := *finishReason
		switch stopReason {
		case "stop":
			stopReason = "end_turn"
		case "tool_calls":
			stopReason = "tool_use"
			// Flush all tool calls — enqueue them so they are emitted one
			// per Next() call (the previous code returned inside the loop,
			// dropping all but the first tool call).
			for idx, tc := range r.toolUse {
				delete(r.toolUse, idx)
				args := strings.TrimSpace(tc.arguments.String())
				if args == "" {
					args = "{}"
				}
				r.pending = append(r.pending, StreamEvent{
					Type: EventToolCallDelta,
					Data: marshalRaw(map[string]interface{}{
						"tool_use_id": tc.toolCallID,
						"tool_name":   tc.toolName,
						"input":       json.RawMessage(args),
					}),
				})
			}
			if len(r.pending) > 0 {
				ev := r.pending[0]
				r.pending = r.pending[1:]
				return ev, true
			}
		case "length":
			stopReason = "max_tokens"
		}

		return StreamEvent{
			Type: EventMessageDelta,
			Data: marshalRaw(struct {
				StopReason string `json:"stop_reason,omitempty"`
			}{
				StopReason: stopReason,
			}),
		}, true
	}

	return StreamEvent{}, false
}

// flushToolCalls enqueues all accumulated tool calls into r.pending and
// returns the first one. Subsequent calls to Next() will drain the rest.
func (r *openaiSSEReader) flushToolCalls() (StreamEvent, bool) {
	for idx, tc := range r.toolUse {
		delete(r.toolUse, idx)
		args := strings.TrimSpace(tc.arguments.String())
		if args == "" {
			args = "{}"
		}
		r.pending = append(r.pending, StreamEvent{
			Type: EventToolCallDelta,
			Data: marshalRaw(map[string]interface{}{
				"tool_use_id": tc.toolCallID,
				"tool_name":   tc.toolName,
				"input":       json.RawMessage(args),
			}),
		})
	}
	if len(r.pending) > 0 {
		ev := r.pending[0]
		r.pending = r.pending[1:]
		return ev, true
	}
	return StreamEvent{}, false
}

func (r *openaiSSEReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}
