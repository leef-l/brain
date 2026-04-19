package sidecar

import (
	"context"
	"fmt"

	"github.com/leef-l/brain/sdk/llm"
)

// kernelLLMProvider 实现 llm.Provider，把 Complete 请求通过反向 RPC
// 转发到 Kernel 的 llm.complete 方法。这样 sidecar 里的 sdk/loop.Runner
// 就能透明地调用主进程的 LLM Provider（Anthropic/DeepSeek/Mock 等）。
//
// Stream 暂未实现（Kernel 侧没有 llm.stream 反向路由）；RunAgentLoop 默认
// 用 Stream=false 路径，因此这里不提供流式。如调用方强制 Stream，会返回错误。
type kernelLLMProvider struct {
	caller KernelCaller
	name   string
}

// NewKernelLLMProvider 构造一个调用 Kernel.llm.complete 的 Provider。
func NewKernelLLMProvider(caller KernelCaller, name string) llm.Provider {
	if name == "" {
		name = "kernel"
	}
	return &kernelLLMProvider{caller: caller, name: name}
}

func (p *kernelLLMProvider) Name() string { return p.name }

func (p *kernelLLMProvider) Complete(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if p.caller == nil {
		return nil, fmt.Errorf("kernelLLMProvider: KernelCaller is nil")
	}
	wire := chatRequestToWire(req)
	var resp llmResponse
	if err := p.caller.CallKernel(ctx, "llm.complete", wire, &resp); err != nil {
		return nil, err
	}
	return wireToChatResponse(&resp, req.Model), nil
}

func (p *kernelLLMProvider) Stream(ctx context.Context, req *llm.ChatRequest) (llm.StreamReader, error) {
	return nil, fmt.Errorf("kernelLLMProvider: streaming not supported via reverse RPC; use Stream=false")
}

// chatRequestToWire 把 llm.ChatRequest 转为 sidecar 内部 llmRequest。
func chatRequestToWire(req *llm.ChatRequest) llmRequest {
	out := llmRequest{MaxTokens: req.MaxTokens}
	for _, sb := range req.System {
		out.System = append(out.System, systemBlock{Text: sb.Text})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, messageFromLLM(m))
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, toolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out
}

func messageFromLLM(m llm.Message) message {
	cbs := make([]contentBlock, 0, len(m.Content))
	for _, b := range m.Content {
		cbs = append(cbs, contentBlockFromLLM(b))
	}
	return message{Role: m.Role, Content: cbs}
}

func contentBlockFromLLM(b llm.ContentBlock) contentBlock {
	cb := contentBlock{Type: b.Type, Text: b.Text}
	if b.ToolUseID != "" {
		cb.ToolUseID = b.ToolUseID
		cb.ID = b.ToolUseID
	}
	if b.ToolName != "" {
		cb.ToolName = b.ToolName
		cb.Name = b.ToolName
	}
	if len(b.Input) > 0 {
		cb.Input = b.Input
	}
	if len(b.Output) > 0 {
		cb.Output = b.Output
		cb.Content = marshalToolOutput(b.Output)
	}
	cb.IsError = b.IsError
	return cb
}

func wireToChatResponse(resp *llmResponse, model string) *llm.ChatResponse {
	out := &llm.ChatResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		StopReason: resp.StopReason,
	}
	if out.Model == "" {
		out.Model = model
	}
	for _, b := range resp.Content {
		out.Content = append(out.Content, contentBlockToLLM(b))
	}
	if resp.Usage != nil {
		out.Usage = llm.Usage{
			InputTokens:         resp.Usage.InputTokens,
			OutputTokens:        resp.Usage.OutputTokens,
			CacheReadTokens:     resp.Usage.CacheReadTokens,
			CacheCreationTokens: resp.Usage.CacheCreationTokens,
			CostUSD:             resp.Usage.CostUSD,
		}
	}
	return out
}

func contentBlockToLLM(b contentBlock) llm.ContentBlock {
	out := llm.ContentBlock{Type: b.Type, Text: b.Text}
	if b.ToolUseID != "" {
		out.ToolUseID = b.ToolUseID
	}
	if b.ID != "" && out.ToolUseID == "" {
		out.ToolUseID = b.ID
	}
	if b.ToolName != "" {
		out.ToolName = b.ToolName
	} else if b.Name != "" {
		out.ToolName = b.Name
	}
	if len(b.Input) > 0 {
		out.Input = b.Input
	}
	if len(b.Output) > 0 {
		out.Output = b.Output
	} else if len(b.Content) > 0 {
		out.Output = b.Content
	}
	out.IsError = b.IsError
	return out
}
