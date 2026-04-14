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
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
)

// LLMProxy handles llm.complete and llm.stream reverse RPC requests from
// sidecar processes. It selects the provider and model based on the brain
// kind of the calling sidecar.
type LLMProxy struct {
	// ProviderFactory returns a configured llm.Provider for a given
	// brain kind. The factory is responsible for selecting the correct
	// model from the config (e.g., models["code"] → "deepseek-v3").
	ProviderFactory func(kind agent.Kind) llm.Provider
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
type llmCompleteResponse struct {
	ID         string             `json:"id"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Content    []llm.ContentBlock `json:"content"`
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

	chatReq := &llm.ChatRequest{
		BrainID:   string(kind),
		System:    req.System,
		Messages:  req.Messages,
		Tools:     req.Tools,
		Model:     req.Model,
		MaxTokens: maxTokens,
	}

	resp, err := provider.Complete(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("LLMProxy: provider.Complete: %w", err)
	}

	return &llmCompleteResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Content:    resp.Content,
	}, nil
}
