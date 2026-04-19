// Package kernel — LLMProxy handles reverse-RPC LLM calls from sidecar
// processes (llm.complete, llm.stream) and routes them to the correct
// provider with the model selected by brainKind.
//
// See 20-协议规格.md §10.1 and 22-Agent-Loop规格.md §5.
package kernel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
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
}

// llmCompleteRequest is the payload sent by a sidecar in an llm.complete
// reverse RPC call.
type llmCompleteRequest struct {
	System    []llm.SystemBlock `json:"system,omitempty"`
	Messages  []llm.Message     `json:"messages"`
	Tools     []llm.ToolSchema  `json:"tools,omitempty"`
	Model     string            `json:"model,omitempty"`
	MaxTokens int               `json:"max_tokens,omitempty"`
}

// llmCompleteResponse is the payload returned to the sidecar.
// Usage 在 v1.1 起从主进程带回，让 sidecar Agent Loop 的 Budget/CheckCost
// 能真正生效；此前版本字段缺失导致 LLM call 成本盲区。
type llmCompleteResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Content    []llm.ContentBlock `json:"content"`
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
func (p *LLMProxy) RegisterHandlers(rpc protocol.BidirRPC, kind agent.Kind) {
	rpc.Handle(protocol.MethodLLMComplete, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return p.handleComplete(ctx, kind, params)
	})

	// llm.stream — for now, fall back to non-streaming complete and
	// return the full response. Real streaming will be added when
	// sidecar-side Agent Loop needs incremental tokens.
	rpc.Handle(protocol.MethodLLMStream, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return p.handleComplete(ctx, kind, params)
	})
}

// handleComplete processes an llm.complete reverse RPC request.
func (p *LLMProxy) handleComplete(ctx context.Context, kind agent.Kind, params json.RawMessage) (interface{}, error) {
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
		BrainID:   string(kind),
		System:    req.System,
		Messages:  req.Messages,
		Tools:     req.Tools,
		Model:     model,
		MaxTokens: maxTokens,
	}
	diaglog.Info("llm", "complete request",
		"kind", kind,
		"model", model,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"max_tokens", maxTokens,
	)

	resp, err := provider.Complete(ctx, chatReq)
	if err != nil {
		diaglog.Error("llm", "complete failed", "kind", kind, "model", model, "err", err)
		return nil, fmt.Errorf("LLMProxy: provider.Complete: %w", err)
	}
	diaglog.Info("llm", "complete ok",
		"kind", kind,
		"model", resp.Model,
		"stop_reason", resp.StopReason,
		"output_blocks", len(resp.Content),
	)

	return &llmCompleteResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Content:    resp.Content,
		Usage: &llmUsageWire{
			InputTokens:         resp.Usage.InputTokens,
			OutputTokens:        resp.Usage.OutputTokens,
			CacheReadTokens:     resp.Usage.CacheReadTokens,
			CacheCreationTokens: resp.Usage.CacheCreationTokens,
			CostUSD:             resp.Usage.CostUSD,
		},
	}, nil
}
