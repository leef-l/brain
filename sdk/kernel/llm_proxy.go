// Package kernel — LLMProxy handles reverse-RPC LLM calls from sidecar
// processes (llm.complete, llm.stream) and routes them to the correct
// provider with the model selected by brainKind.
//
// See 20-协议规格.md §10.1 and 22-Agent-Loop规格.md §5.
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
)

// LLMProxy handles llm.complete and llm.stream reverse RPC requests from
// sidecar processes. It selects the provider and model based on the brain
// kind of the calling sidecar.
//
// Model resolution priority (highest wins):
//  1. Explicit model in the sidecar's llm.complete request
//  2. ModelForKind lookup (populated from BrainRegistration.Model)
//  3. Provider's default model (set at construction time)
type LLMProxy struct {
	// ProviderFactory returns a configured llm.Provider for a given
	// brain kind. The factory is responsible for selecting the correct
	// provider (e.g., Anthropic, OpenAI). When a single provider serves
	// all brains, the factory can return the same instance for every kind.
	ProviderFactory func(kind agent.Kind) llm.Provider

	// ModelForKind maps a brain kind to its configured model ID. This is
	// populated from BrainRegistration.Model by the Orchestrator. When
	// set, it overrides the provider's default model but is itself
	// overridden by an explicit model in the sidecar's request.
	ModelForKind map[agent.Kind]string

	// EventBus 用于将 LLM 流式事件实时发布到统一事件总线。
	// 非 nil 时，handleStream 会把每个 StreamEvent 转换为 events.Event 并 Publish。
	EventBus events.EventBus

	// ExecutionID 关联当前 contract/execution 的 ID，用于事件路由。
	// handleStream 发布事件时将此 ID 写入 Event.ExecutionID。
	ExecutionID string
}

// llmCompleteRequest is the payload sent by a sidecar in an llm.complete
// reverse RPC call.
type llmCompleteRequest struct {
	System      []llm.SystemBlock `json:"system,omitempty"`
	Messages    []llm.Message     `json:"messages"`
	Tools       []llm.ToolSchema  `json:"tools,omitempty"`
	Model       string            `json:"model,omitempty"`
	ToolChoice  string            `json:"tool_choice,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	StreamID    string            `json:"stream_id,omitempty"`
	ExecutionID string            `json:"execution_id,omitempty"`
}

// llmCompleteResponse is the payload returned to the sidecar.
// Usage 在 v1.1 起从主进程带回，让 sidecar Agent Loop 的 Budget/CheckCost
// 能真正生效；此前版本字段缺失导致 LLM call 成本盲区。
type llmCompleteResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Content    []llm.ContentBlock `json:"content"`
	// Message is the concatenated text from all text/thinking content blocks.
	// Provided for backward compatibility with sidecars that expect a plain
	// string "content" field rather than the full []ContentBlock array.
	Message    string             `json:"message"`
	Usage      *llmUsageWire      `json:"usage,omitempty"`
}

// llmUsageWire 是主进程 llm.complete 响应里可选的 Usage 字段，
// 与 sidecar 侧 loop.go 中的同名结构镜像。
type llmUsageWire struct {
	InputTokens         int     `json:"input_tokens,omitempty"`
	OutputTokens        int     `json:"output_tokens,omitempty"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// RegisterHandlers installs llm.complete and llm.stream handlers on the
// given BidirRPC session for a sidecar of the specified kind.
// It is safe to call multiple times on the same rpc — duplicate
// registrations are silently skipped.
func (p *LLMProxy) RegisterHandlers(rpc protocol.BidirRPC, kind agent.Kind) {
	// Defensive: recover from the race where two goroutines pass
	// HandlerExists and then both call Handle (which panics on duplicate).
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "LLMProxy: RegisterHandlers recovered from panic: %v\n", r)
		}
	}()

	if !rpc.HandlerExists(protocol.MethodLLMComplete) {
		fmt.Fprintf(os.Stderr, "LLMProxy: registering %s\n", protocol.MethodLLMComplete)
		rpc.Handle(protocol.MethodLLMComplete, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return p.handleComplete(ctx, kind, params)
		})
	} else {
		fmt.Fprintf(os.Stderr, "LLMProxy: %s already registered\n", protocol.MethodLLMComplete)
	}

	// llm.stream — real streaming: read all events from provider.Stream and
	// aggregate into a single llmCompleteResponse for the sidecar.
	// When the sidecar provides a stream_id we also push incremental deltas
	// via Notify so the sidecar can consume tokens in real time.
	if !rpc.HandlerExists(protocol.MethodLLMStream) {
		fmt.Fprintf(os.Stderr, "LLMProxy: registering %s\n", protocol.MethodLLMStream)
		rpc.Handle(protocol.MethodLLMStream, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return p.handleStream(ctx, kind, params, rpc)
		})
	} else {
		fmt.Fprintf(os.Stderr, "LLMProxy: %s already registered\n", protocol.MethodLLMStream)
	}
}

// extractJSONFromText attempts to extract a valid JSON object or array from
// a text block that may contain surrounding natural language (common with
// reasoning models). It uses bracket matching to handle multiple JSON blobs
// and nested structures. If no valid JSON is found, the original text is
// returned.
func extractJSONFromText(text string) string {
	text = strings.TrimSpace(text)
	if json.Valid([]byte(text)) {
		return text
	}
	// Strip markdown code fences
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var out []string
		for i, line := range lines {
			if i == 0 && strings.HasPrefix(line, "```") {
				continue
			}
			if strings.TrimSpace(line) == "```" {
				continue
			}
			out = append(out, line)
		}
		text = strings.TrimSpace(strings.Join(out, "\n"))
		if json.Valid([]byte(text)) {
			return text
		}
	}
	// Find JSON by bracket matching, respecting string literals.
	for i := 0; i < len(text); i++ {
		if text[i] != '{' && text[i] != '[' {
			continue
		}
		start := i
		depth := 1
		inString := false
		escapeNext := false
		for j := i + 1; j < len(text); j++ {
			c := text[j]
			if escapeNext {
				escapeNext = false
				continue
			}
			if c == '\\' && inString {
				escapeNext = true
				continue
			}
			if c == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if c == '{' || c == '[' {
				depth++
			} else if c == '}' || c == ']' {
				depth--
				if depth == 0 && ((text[start] == '{' && c == '}') || (text[start] == '[' && c == ']')) {
					candidate := text[start : j+1]
					if json.Valid([]byte(candidate)) {
						return candidate
					}
					break // malformed at this position; try next opening bracket
				}
			}
		}
	}
	return text
}


// handleComplete processes an llm.complete reverse RPC request.
func (p *LLMProxy) handleComplete(ctx context.Context, kind agent.Kind, params json.RawMessage) (interface{}, error) {
	_ = kind
	if p.ProviderFactory == nil {
		return nil, fmt.Errorf("LLMProxy: no ProviderFactory configured")
	}

	provider := p.ProviderFactory(kind)
	if provider == nil {
		return nil, fmt.Errorf("LLMProxy: no provider for brain kind %s", kind)
	}

	var req llmCompleteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("LLMProxy: unmarshal request: %w", err)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Model resolution priority:
	// 1. Explicit model in the request (sidecar override)
	// 2. ModelForKind from BrainRegistration config
	// 3. Empty string → provider uses its default model
	model := req.Model
	if model == "" && p.ModelForKind != nil {
		if m, ok := p.ModelForKind[kind]; ok && m != "" {
			model = m
		}
	}

	chatReq := &llm.ChatRequest{
		BrainID:    string(kind),
		System:     req.System,
		Messages:   req.Messages,
		Tools:      req.Tools,
		Model:      model,
		ToolChoice: req.ToolChoice,
		MaxTokens:  maxTokens,
	}
	diaglog.Info("llm", "complete request",
		"kind", kind,
		"model", model,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"max_tokens", maxTokens,
	)

	resp, err := p.completeWithRetry(ctx, provider, kind, model, chatReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LLMProxy: completeWithRetry failed: %v\n", err)
		return nil, fmt.Errorf("LLMProxy: provider.Complete: %w", err)
	}

	diaglog.Info("llm", "complete ok",
		"kind", kind,
		"model", resp.Model,
		"stop_reason", resp.StopReason,
		"output_blocks", len(resp.Content),
	)

	// Post-process: reasoning models (deepseek-v4-pro, etc.) often emit
	// natural language thinking before/after the JSON. Extract the JSON
	// payload so sidecars don't have to deal with markdown wrapping.
	for i := range resp.Content {
		if resp.Content[i].Type == "text" {
			cleaned := extractJSONFromText(resp.Content[i].Text)
			if cleaned != resp.Content[i].Text {
				resp.Content[i].Text = cleaned
			}
		}
	}

	// Build the simplified Message string for backward-compatible sidecars.
	var msgParts []string
	for _, block := range resp.Content {
		if block.Type == "text" || block.Type == "thinking" {
			msgParts = append(msgParts, block.Text)
		}
	}

	return &llmCompleteResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Content:    resp.Content,
		Message:    strings.Join(msgParts, ""),
		Usage: &llmUsageWire{
			InputTokens:         resp.Usage.InputTokens,
			OutputTokens:        resp.Usage.OutputTokens,
			CacheReadTokens:     resp.Usage.CacheReadTokens,
			CacheCreationTokens: resp.Usage.CacheCreationTokens,
			CostUSD:             resp.Usage.CostUSD,
		},
	}, nil
}

func (p *LLMProxy) handleStream(ctx context.Context, kind agent.Kind, params json.RawMessage, rpc protocol.BidirRPC) (interface{}, error) {
	fmt.Fprintf(os.Stderr, "LLMProxy: handleStream start kind=%s eventBus=%v\n", kind, p.EventBus != nil)
	defer func() {
		fmt.Fprintf(os.Stderr, "LLMProxy: handleStream done kind=%s\n", kind)
	}()
	if p.ProviderFactory == nil {
		return nil, fmt.Errorf("LLMProxy: no ProviderFactory configured")
	}

	provider := p.ProviderFactory(kind)
	if provider == nil {
		return nil, fmt.Errorf("LLMProxy: no provider for brain kind %s", kind)
	}

	var req llmCompleteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("LLMProxy: unmarshal request: %w", err)
	}
	fmt.Fprintf(os.Stderr, "LLMProxy: handleStream execID=%q model=%q messages=%d\n", req.ExecutionID, req.Model, len(req.Messages))

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	model := req.Model
	if model == "" && p.ModelForKind != nil {
		if m, ok := p.ModelForKind[kind]; ok && m != "" {
			model = m
		}
	}

	chatReq := &llm.ChatRequest{
		BrainID:    string(kind),
		System:     req.System,
		Messages:   req.Messages,
		Tools:      req.Tools,
		Model:      model,
		ToolChoice: req.ToolChoice,
		MaxTokens:  maxTokens,
		Stream:     true,
	}

	diaglog.Info("llm", "stream request",
		"kind", kind,
		"model", model,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
	)

	reader, err := provider.Stream(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("LLMProxy: provider.Stream: %w", err)
	}
	defer reader.Close()

	var contentBuilder strings.Builder
	var thinkingBuilder strings.Builder
	var stopReason string
	var usage llmUsageWire

	for {
		event, err := reader.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("LLMProxy: stream read: %w", err)
		}

		// If sidecar requested real-time streaming, push every event as a
		// fire-and-forget notification so tokens arrive without waiting for
		// the full aggregation.
		if req.StreamID != "" {
			_ = rpc.Notify(ctx, protocol.MethodLLMStreamDelta, map[string]interface{}{
				"stream_id": req.StreamID,
				"event":     event,
			})
		}

		// Publish to unified event bus for HTTP SSE consumers.
		if p.EventBus != nil && req.ExecutionID != "" {
			evType := string(event.Type)
			switch event.Type {
			case llm.EventContentDelta:
				var delta struct {
					Text string `json:"text"`
					Kind string `json:"kind"`
				}
				if err := json.Unmarshal(event.Data, &delta); err == nil {
					if delta.Kind == "thinking" {
						evType = "llm.thinking_delta"
					} else {
						evType = "llm.content_delta"
					}
				}
			case llm.EventMessageStart:
				evType = "llm.message_start"
			case llm.EventToolCallDelta:
				evType = "llm.tool_call_delta"
			case llm.EventMessageDelta:
				evType = "llm.message_delta"
			case llm.EventMessageEnd:
				evType = "llm.message_end"
			}
			p.EventBus.Publish(ctx, events.Event{
				ExecutionID: req.ExecutionID,
				Type:        evType,
				Data:        event.Data,
			})
		} else {
			fmt.Fprintf(os.Stderr, "LLMProxy: SKIP publish eventbus=%v execID=%q event=%s\n", p.EventBus != nil, req.ExecutionID, event.Type)
		}

		switch event.Type {
		case llm.EventContentDelta:
			var delta struct {
				Text string `json:"text"`
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(event.Data, &delta); err == nil {
				if delta.Kind == "thinking" {
					thinkingBuilder.WriteString(delta.Text)
				} else {
					contentBuilder.WriteString(delta.Text)
				}
			}

		case llm.EventMessageDelta:
			var msgDelta struct {
				StopReason string         `json:"stop_reason"`
				Usage      llmUsageWire   `json:"usage"`
			}
			if err := json.Unmarshal(event.Data, &msgDelta); err == nil {
				stopReason = msgDelta.StopReason
				usage = msgDelta.Usage
			}

		case llm.EventMessageEnd:
			// stream finished
		}
	}

	diaglog.Info("llm", "stream ok",
		"kind", kind,
		"model", model,
		"stop_reason", stopReason,
		"output_chars", contentBuilder.Len(),
		"thinking_chars", thinkingBuilder.Len(),
	)

	var content []llm.ContentBlock
	var msgParts []string
	if contentBuilder.Len() > 0 {
		content = append(content, llm.ContentBlock{
			Type: "text",
			Text: contentBuilder.String(),
		})
		msgParts = append(msgParts, contentBuilder.String())
	}
	if thinkingBuilder.Len() > 0 {
		content = append(content, llm.ContentBlock{
			Type: "thinking",
			Text: thinkingBuilder.String(),
		})
		msgParts = append(msgParts, thinkingBuilder.String())
	}

	return &llmCompleteResponse{
		ID:         "stream-" + string(kind),
		Model:      model,
		StopReason: stopReason,
		Content:    content,
		Message:    strings.Join(msgParts, ""),
		Usage:      &usage,
	}, nil
}

// Complete executes a non-streaming LLM completion request directly.
// It resolves the provider and model from req.BrainID, matching the
// behavior of the reverse-RPC llm.complete handler.
func (p *LLMProxy) Complete(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if p.ProviderFactory == nil {
		return nil, fmt.Errorf("LLMProxy: no ProviderFactory configured")
	}
	kind := agent.Kind(req.BrainID)
	provider := p.ProviderFactory(kind)
	if provider == nil {
		return nil, fmt.Errorf("LLMProxy: no provider for brain kind %s", kind)
	}

	// Model resolution priority:
	// 1. Explicit model in the request
	// 2. ModelForKind lookup
	// 3. Empty string -> provider uses its default
	model := req.Model
	if model == "" && p.ModelForKind != nil {
		if m, ok := p.ModelForKind[kind]; ok && m != "" {
			model = m
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	chatReq := &llm.ChatRequest{
		BrainID:         req.BrainID,
		RunID:           req.RunID,
		TurnIndex:       req.TurnIndex,
		System:          req.System,
		Messages:        req.Messages,
		Tools:           req.Tools,
		Model:           model,
		ToolChoice:      req.ToolChoice,
		MaxTokens:       maxTokens,
		CacheControl:    req.CacheControl,
		TurnTimeout:     req.TurnTimeout,
		RemainingBudget: req.RemainingBudget,
	}

	resp, err := p.completeWithRetry(ctx, provider, kind, model, chatReq)
	if err != nil {
		return nil, fmt.Errorf("LLMProxy: provider.Complete: %w", err)
	}
	return resp, nil
}

func (p *LLMProxy) completeWithRetry(ctx context.Context, provider llm.Provider, kind agent.Kind, model string, chatReq *llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := provider.Complete(ctx, chatReq)
	if err == nil {
		return resp, nil
	}
	diaglog.Error("llm", "complete failed", "kind", kind, "model", model, "err", err)
	if !shouldRetryLLMError(err) {
		return nil, err
	}

	diaglog.Warn("llm", "complete retrying", "kind", kind, "model", model, "backoff_ms", 300)
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	resp, retryErr := provider.Complete(ctx, chatReq)
	if retryErr != nil {
		diaglog.Error("llm", "complete retry failed", "kind", kind, "model", model, "err", retryErr)
		return nil, retryErr
	}
	diaglog.Info("llm", "complete retry ok", "kind", kind, "model", model)
	return resp, nil
}

func shouldRetryLLMError(err error) bool {
	var be *brainerrors.BrainError
	if errors.As(err, &be) {
		return be.Retryable && (be.ErrorCode == brainerrors.CodeLLMUpstream5xx || be.ErrorCode == brainerrors.CodeLLMRateLimitedShortterm)
	}
	return false
}
