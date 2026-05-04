package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
		httpClient: newDefaultHTTPClient(),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *OpenAIProvider) Name() string { return "openai" }

// isDeepSeek detects whether the endpoint is DeepSeek by baseURL or model name.
func (p *OpenAIProvider) isDeepSeek() bool {
	return strings.Contains(strings.ToLower(p.baseURL), "deepseek") ||
		strings.Contains(strings.ToLower(p.model), "deepseek")
}

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
	Role             string           `json:"role"`
	Content          interface{}      `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
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
	// 不再兜底设 4096。MaxTokens=0 时通过 omitempty 不传 provider,
	// 让模型按自身上限输出(主流 OpenAI 兼容服务通常 8K-128K)。
	// 历史:默认 4096 经常截断 tool_use JSON 导致工具参数残缺。

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

	// ToolChoice — many OpenAI-compatible APIs (DeepSeek, etc.) do not support
	// this field and return HTTP 400. The default behavior without tool_choice
	// is "auto", which is sufficient for the Brain agent loop.
	_ = req.ToolChoice

	return ar
}

func (p *OpenAIProvider) buildAssistantMessage(m Message) openaiMessage {
	msg := openaiMessage{Role: "assistant"}

	var textParts []string
	var thinkingParts []string
	var toolCalls []openaiToolCall

	for _, b := range m.Content {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking":
			if b.Text != "" {
				thinkingParts = append(thinkingParts, b.Text)
			}
		case "tool_use":
			// 关键:b.Input 长度 0(无论 nil 还是 []byte{})都填默认 "{}",
			// 避免后续序列化空字符串给 OpenAI 兼容 API 报 400 / marshal 错。
			args := "{}"
			if len(b.Input) > 0 && json.Valid(b.Input) {
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

	// DeepSeek reasoner API 要求：只要对话中存在 reasoning_content，
	// 后续请求必须原样在 reasoning_content 字段中传回，否则报 400。
	// 无论是否同时存在 tool_calls，都必须保留 reasoning_content。
	hasThinking := len(thinkingParts) > 0
	if hasThinking {
		rc := strings.Join(thinkingParts, "\n")
		msg.ReasoningContent = &rc
	}

	if len(textParts) > 0 {
		msg.Content = strings.Join(textParts, "\n")
	}
	// OpenAI requires content field for assistant messages — some compatible
	// APIs reject the request if content is missing entirely.
	// DeepSeek reasoner API 特有约束: assistant message 包含 reasoning_content
	// 时，content 必须为 null/省略，不能是空字符串，否则报 400。
	if msg.Content == nil {
		if !(p.isDeepSeek() && msg.ReasoningContent != nil) {
			msg.Content = ""
		}
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
					// 长度判断而非 nil 判断:空非 nil RawMessage 也视为无内容。
					content := ""
					if len(b.Output) > 0 {
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

	out = sanitizeEmptyAssistantMessages(out)
	return sanitizeToolCallSequence(out)
}

// sanitizeEmptyAssistantMessages 过滤掉 content 和 tool_calls 都为空的 assistant
// 消息。OpenAI/DeepSeek 等 API 对此报 HTTP 400
// "Invalid assistant message: content or tool_calls must be set"。
//
// 真实场景:LLM 在 thinking-only / 空响应后产生一条 empty assistant 消息
// 进入 messages 历史,下一轮请求带上它会被 API 拒绝整个请求。这条消息本身
// 没有信息量,直接丢弃即可。
func sanitizeEmptyAssistantMessages(msgs []openaiMessage) []openaiMessage {
	out := make([]openaiMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "assistant" {
			hasContent := false
			switch v := m.Content.(type) {
			case string:
				hasContent = strings.TrimSpace(v) != ""
			case nil:
				// content 是 nil
			default:
				// 其他类型 (slice/map) 视为有内容
				hasContent = true
			}
			hasReasoning := m.ReasoningContent != nil && strings.TrimSpace(*m.ReasoningContent) != ""
			hasToolCalls := len(m.ToolCalls) > 0
			if !hasContent && !hasToolCalls && !hasReasoning {
				// 完全空的 assistant 消息 — 丢弃
				continue
			}
		}
		out = append(out, m)
	}
	return out
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
		case "tool_calls", "function_call":
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
				Input:     sanitizeToolArguments(tc.Function.Arguments),
			})
		}

		// 兼容阿里云 qwen / 其他 OpenAI-compatible 代理:message.tool_calls 非空
		// 但 finish_reason="stop" 的情况,runner 按 StopReason=="tool_use" 才
		// dispatch tool,这里强制矫正,避免 tool call 被丢弃。
		if len(choice.Message.ToolCalls) > 0 && resp.StopReason != "tool_use" {
			resp.StopReason = "tool_use"
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
	body        io.ReadCloser
	scanner     *bufio.Scanner
	mu          sync.Mutex
	closed      bool
	toolUse     map[int]*openaiStreamToolUse
	pending     []StreamEvent // queued events to emit on subsequent Next() calls
	readTimeout time.Duration
	// lastEventAt 记录上一次产出有效事件(非心跳)的时刻。
	// 用于跨 Next() 调用累计"无事件持续时间",防止服务端只发心跳让单行
	// scanLine 30s 超时永远不触发的死锁。
	lastEventAt time.Time
}

type openaiStreamToolUse struct {
	toolCallID string
	toolName   string
	arguments  strings.Builder
}

func newOpenAISSEReader(body io.ReadCloser) *openaiSSEReader {
	return &openaiSSEReader{
		body:        body,
		scanner:     bufio.NewScanner(body),
		toolUse:     make(map[int]*openaiStreamToolUse),
		readTimeout: 30 * time.Second,
		lastEventAt: time.Now(),
	}
}

func (r *openaiSSEReader) Next(ctx context.Context) (StreamEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 跨 Next() 累计的"上次有效事件至今"超时:90s。
	// 单行 readTimeout 30s 是行级保护,但服务端如果一直发心跳/空行/注释行,
	// 每行都成功 scan,Next 永远拿不到 event。lastEventAt 跨 Next 调用累计
	// 静默时长,超过 90s 报错让上层换 provider 或重试。
	const maxStallDuration = 90 * time.Second

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

		// 跨 Next 累计静默检查:服务端可能一直发心跳让 scanLine 不超时,
		// 但 Next 永远拿不到真 event。lastEventAt 跨 Next 调用累计。
		if time.Since(r.lastEventAt) > maxStallDuration {
			return StreamEvent{}, fmt.Errorf("openai: stream stalled — no event produced in %v (server may be sending only heartbeats)", maxStallDuration)
		}

		line, err := r.scanLine()
		if err != nil {
			if err == io.EOF {
				// Flush any pending tool calls
				if ev, ok := r.flushToolCalls(); ok {
					return ev, nil // will be followed by EOF on next call
				}
				return StreamEvent{}, io.EOF
			}
			return StreamEvent{}, fmt.Errorf("openai: stream scan: %w", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			// Flush pending tool calls before ending
			if ev, ok := r.flushToolCalls(); ok {
				r.lastEventAt = time.Now()
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
			r.lastEventAt = time.Now()
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
			Data: marshalRaw(map[string]string{"text": *delta.ReasoningContent, "kind": "thinking"}),
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
		// 兼容不同实现:阿里云百炼 qwen / DeepSeek / 某些 OpenAI 代理
		// 可能用 function_call / FUNCTION_CALL / tool-calls / tool_use 等
		// 变体。只要 toolUse map 里有累积的 tool_use 数据,就当 tool_use
		// 处理,不管 finish_reason 叫什么名字。
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(stopReason, "-", "_"), " ", "_"))
		if len(r.toolUse) > 0 {
			switch normalized {
			case "stop", "end_turn", "length", "max_tokens":
				// provider 报了普通停止,但已经累积了 tool call,按 tool_use
				// 处理——不然 tool call 会丢。
				normalized = "tool_calls"
			}
		}
		switch normalized {
		case "stop":
			stopReason = "end_turn"
		case "tool_calls", "function_call", "tool_use":
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
						"input":       sanitizeToolArguments(args),
					}),
				})
			}
			if len(r.pending) > 0 {
				// 必须把 StopReason=tool_use 的 MessageDelta 也 enqueue,
				// 不能直接 return stopReason,否则 runner.go:320 检查
				// resp.StopReason != "tool_use" 会丢弃 tool call。
				r.pending = append(r.pending, StreamEvent{
					Type: EventMessageDelta,
					Data: marshalRaw(struct {
						StopReason string `json:"stop_reason,omitempty"`
					}{StopReason: "tool_use"}),
				})
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
// 若有 tool call 累积,末尾自动追加一条 EventMessageDelta 把 StopReason
// 设成 "tool_use"——某些服务端(阿里云 qwen)不发 finish_reason 或 fin-
// ish_reason 不是 tool_calls,没有这一步 runner 会把 tool call 当作普通
// 文本忽略,表现为"Plan 了但 tools=0"。
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
		// Append a message_delta with StopReason=tool_use so runner knows to
		// actually dispatch the tools.
		r.pending = append(r.pending, StreamEvent{
			Type: EventMessageDelta,
			Data: marshalRaw(struct {
				StopReason string `json:"stop_reason,omitempty"`
			}{StopReason: "tool_use"}),
		})
		ev := r.pending[0]
		r.pending = r.pending[1:]
		return ev, true
	}
	return StreamEvent{}, false
}

func (r *openaiSSEReader) scanLine() (string, error) {
	type result struct {
		line string
		ok   bool
	}
	done := make(chan result, 1)
	go func() {
		if r.scanner.Scan() {
			done <- result{line: r.scanner.Text(), ok: true}
		} else {
			done <- result{ok: false}
		}
	}()
	select {
	case res := <-done:
		if !res.ok {
			if err := r.scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		return res.line, nil
	case <-time.After(r.readTimeout):
		return "", fmt.Errorf("SSE line read timeout after %v", r.readTimeout)
	}
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

// sanitizeToolArguments 把 LLM 输出的 tool_call.arguments 字符串安全转为 json.RawMessage。
//
// 真实 bug:DeepSeek / OpenAI 兼容服务返回的 arguments 偶尔包含:
//   - 控制字符(\x00-\x1F 除 \t/\n/\r)
//   - 未转义的反斜杠 / 引号(LLM 写代码时常见,如 prompt 字段嵌真换行)
//   - 不完整 UTF-8(被 max_tokens 截断)
//   - 尾部不完整(stream 提前结束 → 缺少右括号 / 引号)
//
// 之前的代码 `json.RawMessage(args)` 直接强转,后续 sidecar / host 任何
// json.Marshal(result) 都会触发 RawMessage.MarshalJSON 校验失败,
// 报 "result marshal failed: json: error calling MarshalJSON for type
// json.RawMessage"。整个 run 失败,但用户看不出根因。
//
// 之前的修复:fallback 到 {}。问题:14K nodes 数组完全丢失 →
// submit_workflow 收到空 nodes,workflow has no nodes 报错。
//
// 修复策略(优先级):
//   1. 校验是否合法 JSON → 直接用
//   2. 解析到 map 再回写(自动 escape) → 直接用
//   3. **新增**:含控制字符 → 转义后重试
//   4. **新增**:看起来被截断(unbalanced braces/quotes) → 尝试补齐尾部
//   5. 还失败 → fallback {} 兜底
func sanitizeToolArguments(args string) json.RawMessage {
	if args == "" {
		return json.RawMessage("{}")
	}

	// 1. 合法 JSON 直通
	if cleaned, ok := tryParseAndRemarshal(args); ok {
		return cleaned
	}

	// 2. 控制字符修复:把字面控制字符(真换行/真制表符)转义为 \n / \t / \uXXXX
	if escaped := escapeControlCharsInJSON(args); escaped != args {
		if cleaned, ok := tryParseAndRemarshal(escaped); ok {
			fmt.Fprintf(os.Stderr, "openai_provider: tool arguments recovered after control-char escape, len=%d\n", len(args))
			return cleaned
		}
	}

	// 3. 截断修复:补齐缺失的右括号/引号
	if completed, ok := tryCompleteJSON(args); ok {
		if cleaned, ok2 := tryParseAndRemarshal(completed); ok2 {
			fmt.Fprintf(os.Stderr, "openai_provider: tool arguments recovered after truncation repair, len=%d\n", len(args))
			return cleaned
		}
	}

	// 4. 控制字符 + 截断双修复
	if escaped := escapeControlCharsInJSON(args); escaped != args {
		if completed, ok := tryCompleteJSON(escaped); ok {
			if cleaned, ok2 := tryParseAndRemarshal(completed); ok2 {
				fmt.Fprintf(os.Stderr, "openai_provider: tool arguments recovered after escape+truncation repair, len=%d\n", len(args))
				return cleaned
			}
		}
	}

	// 5. 兜底:返回 {},打详细日志(含原文片段帮助排查)
	preview := args
	if len(preview) > 200 {
		preview = preview[:100] + "...[" + fmt.Sprintf("%d truncated", len(args)-200) + "]..." + preview[len(preview)-100:]
	}
	fmt.Fprintf(os.Stderr, "openai_provider: tool arguments invalid JSON unrecoverable, fallback to {}. len=%d preview=%q\n", len(args), preview)
	return json.RawMessage("{}")
}

// tryParseAndRemarshal 校验 + 重 Marshal。成功返回干净 RawMessage,失败返回 nil/false。
func tryParseAndRemarshal(args string) (json.RawMessage, bool) {
	var probe json.RawMessage
	if err := json.Unmarshal([]byte(args), &probe); err == nil {
		if cleaned, err2 := json.Marshal(probe); err2 == nil {
			return cleaned, true
		}
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal([]byte(args), &asMap); err == nil {
		if cleaned, err2 := json.Marshal(asMap); err2 == nil {
			return cleaned, true
		}
	}
	return nil, false
}

// escapeControlCharsInJSON 在 JSON 字符串值内部把字面控制字符转义。
// 状态机识别 string token (用 " 包裹,处理 \" 转义),仅在 string 内做替换。
// 字符串外的控制字符(JSON 结构本身的 \n / \t)直接保留。
//
// 处理:
//   - 0x09 真制表符 → \t
//   - 0x0A 真换行 → \n
//   - 0x0D 真回车 → \r
//   - 其他 0x00-0x1F → \uXXXX
func escapeControlCharsInJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			b.WriteByte(c)
			continue
		}
		// inString
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			b.WriteByte(c)
			escaped = true
			continue
		}
		if c == '"' {
			inString = false
			b.WriteByte(c)
			continue
		}
		// 真实控制字符 → 转义
		if c < 0x20 {
			switch c {
			case '\t':
				b.WriteString(`\t`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// tryCompleteJSON 检测尾部不平衡(缺右括号/方括号/引号),尝试补齐。
// 仅在结构层面补,不动字符串内容。复杂转义场景可能补不准,补完仍解析失败由上层兜底。
//
// 算法:
//   - 扫描整串,跟踪字符串 in/out 状态、{ }、[ ] 计数
//   - 末尾若 inString 真,补一个 "
//   - 若有未平衡的 { 或 [,按反向 LIFO 补 } 或 ]
func tryCompleteJSON(s string) (string, bool) {
	type frame struct{ open byte }
	var stack []frame
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, frame{open: '{'})
		case '[':
			stack = append(stack, frame{open: '['})
		case '}':
			if len(stack) == 0 || stack[len(stack)-1].open != '{' {
				return "", false // 结构错乱无法修
			}
			stack = stack[:len(stack)-1]
		case ']':
			if len(stack) == 0 || stack[len(stack)-1].open != '[' {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if !inString && len(stack) == 0 {
		return s, false // 没什么可补
	}
	var b strings.Builder
	b.WriteString(s)
	// 移除末尾可能的 trailing comma:`{...,` 补 `}` 会非法。简单去掉末尾空白后逗号。
	trimmed := strings.TrimRightFunc(b.String(), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if strings.HasSuffix(trimmed, ",") {
		trimmed = trimmed[:len(trimmed)-1]
	}
	b.Reset()
	b.WriteString(trimmed)
	if inString {
		b.WriteByte('"')
	}
	// 反向补 } / ]
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].open == '{' {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
	}
	return b.String(), true
}
