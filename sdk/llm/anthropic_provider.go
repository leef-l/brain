package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	caps       Capabilities
}

// AnthropicOption configures an AnthropicProvider.
type AnthropicOption func(*AnthropicProvider)

// WithHTTPClient sets a custom http.Client (useful for testing).
func WithHTTPClient(c *http.Client) AnthropicOption {
	return func(p *AnthropicProvider) { p.httpClient = c }
}

// WithAnthropicCapabilities overrides the auto-inferred Capabilities.
// Useful when the assembling layer (cmd/brain/provider) has more accurate
// knowledge of the deployment than the model-name heuristic.
func WithAnthropicCapabilities(c Capabilities) AnthropicOption {
	return func(p *AnthropicProvider) { p.caps = c }
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
		baseURL:    strings.TrimRight(baseURL, "/"),
		authToken:  authToken,
		model:      model,
		httpClient: newDefaultHTTPClient(),
		caps:       InferCapabilities(baseURL, model),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Capabilities implements CapabilityAware.
func (p *AnthropicProvider) Capabilities() Capabilities { return p.caps }

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
	// Anthropic API max_tokens 必传字段,不能省。但 4096 太低,
	// 写长 plan + 多 tool_use 容易截断。Claude Sonnet/Opus 4.x 都支持 64K
	// output;给个保守又够用的 32K 默认,调用方需要更高显式传。
	if ar.MaxTokens <= 0 {
		ar.MaxTokens = 32768
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
	// req.CacheControl 标记 L2_task / L3_history 层的 cache 边界,
	// Anthropic 要求把 cache_control:{type:ephemeral} 加到目标 message
	// 的最后一个 content block 上。聚合每个 message index 是否需要打标。
	cacheMsgIdx := make(map[int]bool, len(req.CacheControl))
	for _, cp := range req.CacheControl {
		if cp.Layer == "L2_task" || cp.Layer == "L3_history" {
			if cp.Index >= 0 && cp.Index < len(req.Messages) {
				cacheMsgIdx[cp.Index] = true
			}
		}
	}

	for i, m := range req.Messages {
		content := p.buildContentBlocks(m.Content)
		if cacheMsgIdx[i] && len(content) > 0 {
			// 最后一个 block 加 cache_control。
			content[len(content)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
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

	// ToolChoice — gated by Capabilities.ToolChoiceSupport so Anthropic-
	// compatible third-party proxies (e.g. some Tencent / AWS Bedrock
	// gateways) that don't honor the field don't trip on it.
	if tc := buildAnthropicToolChoice(req.ToolChoice, p.caps.ToolChoiceSupport); tc != nil {
		ar.ToolChoice = tc
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
		case "thinking":
			out = append(out, map[string]interface{}{
				"type":      "thinking",
				"thinking":  b.Text,
			})
		case "tool_use":
			block := map[string]interface{}{
				"type": "tool_use",
				"id":   b.ToolUseID,
				"name": sanitizeToolName(b.ToolName),
			}
			// 关键:b.Input 为 nil 或长度 0(空非 nil) 都视作"无 input",
			// 否则空 RawMessage 进入 map 后整体 marshal 会报
			// "MarshalJSON for type json.RawMessage: unexpected end of JSON input"。
			// 用户日志中观察到的 "result marshal failed" 真凶之一。
			if len(b.Input) > 0 && json.Valid(b.Input) {
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
			// 同上:空非 nil 也跳过(string([]byte{}) == "" 倒不会爆 marshal,
			// 但写入空字符串 content 会让 Anthropic 后端拒绝;为干净起见也过滤)。
			if len(b.Output) > 0 {
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
		Type             string          `json:"type"`
		Text             string          `json:"text"`
		Thinking         string          `json:"thinking"`
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		ToolName         string          `json:"tool_name"`
		Input            json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(ar.Content, &rawBlocks); err != nil {
		return nil, fmt.Errorf("invalid anthropic content: %w; body=%s", err, truncateBody(rawBody, 512))
	}
	for _, rb := range rawBlocks {
		cb := ContentBlock{Type: rb.Type}
		switch rb.Type {
		case "text":
			cb.Text = rb.Text
		case "thinking":
			cb.Text = rb.Thinking
		case "redacted_thinking":
			cb.Text = "[redacted thinking]"
		case "tool_use":
			toolName := rb.Name
			if strings.TrimSpace(toolName) == "" {
				toolName = rb.ToolName
			}
			cb.ToolUseID = rb.ID
			cb.ToolName = restoreToolName(toolName)
			// 防御:走 sanitize 一遍。Anthropic API 一般保证返回合法 JSON,
			// 但同款 helper 给所有 provider 用,统一不容易出错。
			cb.Input = sanitizeToolArguments(string(rb.Input))
		}
		resp.Content = append(resp.Content, cb)
	}
	if err := ValidateToolUseResponse("anthropic", resp); err != nil {
		return nil, fmt.Errorf("%w; body=%s", err, truncateBody(rawBody, 512))
	}

	return resp, nil
}

// truncateBody returns b as string, truncated to at most n bytes.
//
// 截断点退到 UTF-8 字符边界,避免日志/错误中出现 0xEF 0xBF 0xBD 的
// 替换字符或半角乱码(中文/emoji 错误响应里很常见)。
func truncateBody(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + "...[truncated]"
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
	body        io.ReadCloser
	scanner     *bufio.Scanner
	mu          sync.Mutex
	closed      bool
	toolUse     map[int]*streamToolUse
	readTimeout time.Duration
}

type streamToolUse struct {
	toolUseID string
	toolName  string
	input     strings.Builder
}

func newSSEReader(body io.ReadCloser) *sseReader {
	return &sseReader{
		body:        body,
		scanner:     bufio.NewScanner(body),
		toolUse:     make(map[int]*streamToolUse),
		readTimeout: 30 * time.Second,
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

		line, err := r.scanLine()
		if err != nil {
			if err == io.EOF {
				return StreamEvent{}, io.EOF
			}
			return StreamEvent{}, fmt.Errorf("anthropic: stream scan: %w", err)
		}

		// SSE format: "event: <type>\ndata: <json>\n\n"
		if strings.HasPrefix(line, "event: ") {
			eventType := strings.TrimPrefix(line, "event: ")

			// Read data line
			dataLine, err := r.scanLine()
			if err != nil {
				if err == io.EOF {
					return StreamEvent{}, io.EOF
				}
				return StreamEvent{}, fmt.Errorf("anthropic: stream scan data line: %w", err)
			}
			var data json.RawMessage
			if strings.HasPrefix(dataLine, "data: ") {
				// 兼容服务端发 "data:  <json>"(多空格)或 "data: \t..."
				// 等非标准格式:统一 TrimSpace 后再当 RawMessage。
				data = json.RawMessage(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: ")))
			}

			ev, ok := r.mapSSEEvent(eventType, data)
			if ok {
				return ev, nil
			}
			continue
		}

		// Some providers send "data: " lines directly
		if strings.HasPrefix(line, "data: ") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
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
		case "thinking":
			return StreamEvent{
				Type: EventContentDelta,
				Data: marshalRaw(map[string]string{"text": start.ContentBlock.Text, "kind": "thinking"}),
			}, true
		case "redacted_thinking":
			return StreamEvent{
				Type: EventContentDelta,
				Data: marshalRaw(map[string]string{"text": "[redacted thinking]", "kind": "thinking"}),
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
			// 先发一个"只带 tool_name + tool_use_id"的 delta,让下游
			// runner 立刻建立 currentToolCall。这样即便某些第三方代理
			// 不发 content_block_stop(导致 Input 从没 flush),也不会
			// 出现 "stop_reason=tool_use without tool_use block" 错误;
			// runner 在收到 input_json_delta 时会往 currentToolCall.Input
			// append。
			return StreamEvent{
				Type: EventToolCallDelta,
				Data: marshalRaw(map[string]interface{}{
					"tool_use_id": block.toolUseID,
					"tool_name":   block.toolName,
					"input":       json.RawMessage("{}"),
				}),
			}, true
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
		case "thinking_delta":
			return StreamEvent{
				Type: EventContentDelta,
				Data: marshalRaw(map[string]string{"text": delta.Delta.Text, "kind": "thinking"}),
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
				// 走 sanitize 路径,把可能含控制字符 / 半截 JSON 的累积
				// input_json_delta 标准化为有效 RawMessage,与 mapChunk 一致。
				"input": sanitizeToolArguments(input),
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

func (r *sseReader) scanLine() (string, error) {
	type result struct {
		line string
		ok   bool
	}
	done := make(chan result, 1)
	go func() {
		// 同 openai_provider scanLine 的加固:scanner.Scan() 罕见 panic
		// 不能让整个 brain 进程崩,转成 EOF 让上层走正常错误路径。
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Fprintf(os.Stderr, "anthropic_provider: scanLine goroutine panic recovered: %v\n", rec)
				select {
				case done <- result{ok: false}:
				default:
				}
			}
		}()
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

func (r *sseReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.body.Close()
}

// newDefaultHTTPClient returns an http.Client with fine-grained timeouts.
//
// Total timeout 0 (disabled):用 ctx 超时控制整体上限,避免 reasoning 模型
// (mimo / deepseek-reasoner / qwen-reasoner)在思考阶段 server 端不输出
// 任何字节时被 http.Client.Timeout 切断。
//
// ResponseHeaderTimeout 提到 5min:reasoning 模型常见思考 1-3 分钟才开始
// 流式返回第一个 token,60s 太紧。streaming 启动后真实进度由 SSE 行间隔
// 控制(readTimeout 30s 见 openai_provider.go:517)。
//
// 对话式 / 短回答场景体验:headers 通常 < 5s 返回,长 ResponseHeaderTimeout
// 不影响快路径。
func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 0, // 无总超时 — 流式响应可能写很久,靠 SSE 行级 + 累计静默 timer 兜底
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second, // 60s 拿到 headers — 之前 5min 太宽松
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
			DisableCompression:    true, // SSE 流不压缩,避免 gzip 缓冲让心跳/事件延迟到达
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
		},
	}
}

// buildAnthropicToolChoice converts the brain-internal tool_choice string
// to Anthropic's wire shape, gated by the provider's ToolChoiceSupport.
// Returns nil when the field MUST be omitted.
//
// Wire mapping per Anthropic API:
//
//	"auto"     → {"type":"auto"}
//	"required" → {"type":"any"}      ← Anthropic uses "any", not "required"
//	"none"     → {"type":"none"}
//	<name>     → {"type":"tool","name":"<name>"}
func buildAnthropicToolChoice(value string, support ToolChoiceMode) json.RawMessage {
	if support == ToolChoiceNone {
		return nil
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	switch v {
	case "auto":
		if support >= ToolChoiceAuto {
			data, _ := json.Marshal(map[string]string{"type": "auto"})
			return data
		}
	case "none":
		if support >= ToolChoiceAuto {
			data, _ := json.Marshal(map[string]string{"type": "none"})
			return data
		}
	case "required":
		if support >= ToolChoiceRequired {
			data, _ := json.Marshal(map[string]string{"type": "any"})
			return data
		}
	default:
		if support >= ToolChoiceSpecific {
			data, _ := json.Marshal(map[string]string{
				"type": "tool",
				"name": sanitizeToolName(v),
			})
			return data
		}
		// Degrade to "any" (= required) if specific not supported but required is.
		if support >= ToolChoiceRequired {
			data, _ := json.Marshal(map[string]string{"type": "any"})
			return data
		}
	}
	return nil
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
