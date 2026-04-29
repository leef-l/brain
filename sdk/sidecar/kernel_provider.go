package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
)

// kernelLLMProvider 实现 llm.Provider，把 Complete 请求通过反向 RPC
// 转发到 Kernel 的 llm.complete 方法。这样 sidecar 里的 sdk/loop.Runner
// 就能透明地调用主进程的 LLM Provider（Anthropic/DeepSeek/Mock 等）。
//
// Stream 支持两种模式：
//   1. 真正的跨端实时流式（默认）：sidecar 生成 stream_id，host 在读取 provider
//      StreamReader 的过程中，通过 llm/stream/delta 通知逐事件推送给 sidecar，
//      sidecar 通过 channelStreamReader 实时消费。零 RTT，速度最快。
//   2. 聚合模式（向后兼容）：旧 sidecar 不带 stream_id 时，host 聚合全部事件后
//      一次性返回 llmCompleteResponse。
type kernelLLMProvider struct {
	caller      KernelCaller
	name        string
	executionID string
}

// NewKernelLLMProvider 构造一个调用 Kernel.llm.complete 的 Provider。
// executionID 用于端到端流式事件关联；为空时不启用流式输出。
func NewKernelLLMProvider(caller KernelCaller, name string, executionID string) llm.Provider {
	if name == "" {
		name = "kernel"
	}
	return &kernelLLMProvider{caller: caller, name: name, executionID: executionID}
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
	if p.caller == nil {
		return nil, fmt.Errorf("kernelLLMProvider: KernelCaller is nil")
	}

	streamID := generateStreamID()
	ch := make(chan llm.StreamEvent, 256)
	registerStreamChan(streamID, ch)

	wire := chatRequestToWire(req)
	wire.StreamID = streamID
	wire.ExecutionID = p.executionID

	var resp llmResponse
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "kernelProvider: stream goroutine panic: %v\n", r)
			}
			unregisterStreamChan(streamID)
			close(ch)
		}()
		_ = p.caller.CallKernel(ctx, protocol.MethodLLMStream, wire, &resp)
	}()

	return &channelStreamReader{ch: ch}, nil
}

// staticStreamReader 把聚合的 llmResponse 按流式事件协议拆分发包。
type staticStreamReader struct {
	resp   *llmResponse
	model  string
	done   bool
	idx    int
	closed bool
}

func (s *staticStreamReader) Next(ctx context.Context) (llm.StreamEvent, error) {
	if s.closed {
		return llm.StreamEvent{}, fmt.Errorf("stream closed")
	}
	if s.done {
		return llm.StreamEvent{}, io.EOF
	}

	events := s.buildEvents()
	if s.idx < len(events) {
		ev := events[s.idx]
		s.idx++
		return ev, nil
	}
	s.done = true
	return llm.StreamEvent{}, io.EOF
}

func (s *staticStreamReader) Close() error {
	s.closed = true
	return nil
}

func (s *staticStreamReader) buildEvents() []llm.StreamEvent {
	var events []llm.StreamEvent
	model := s.resp.Model
	if model == "" {
		model = s.model
	}

	// message.start
	startData, _ := json.Marshal(map[string]string{"id": s.resp.ID, "model": model, "role": "assistant"})
	events = append(events, llm.StreamEvent{Type: llm.EventMessageStart, Data: startData})

	// content / tool_call deltas
	for _, cb := range s.resp.Content {
		switch cb.Type {
		case "text":
			delta, _ := json.Marshal(map[string]string{"text": cb.Text, "kind": "text"})
			events = append(events, llm.StreamEvent{Type: llm.EventContentDelta, Data: delta})
		case "tool_use":
			delta, _ := json.Marshal(map[string]interface{}{
				"id":   cb.ID,
				"name": cb.Name,
				"input": cb.Input,
			})
			events = append(events, llm.StreamEvent{Type: llm.EventToolCallDelta, Data: delta})
		}
	}

	// message.delta (stop_reason + usage)
	msgDelta := map[string]interface{}{
		"stop_reason": s.resp.StopReason,
	}
	if s.resp.Usage != nil {
		msgDelta["usage"] = s.resp.Usage
	}
	deltaData, _ := json.Marshal(msgDelta)
	events = append(events, llm.StreamEvent{Type: llm.EventMessageDelta, Data: deltaData})

	// message.end
	events = append(events, llm.StreamEvent{Type: llm.EventMessageEnd, Data: []byte("{}")})
	return events
}

// chatRequestToWire 把 llm.ChatRequest 转为 sidecar 内部 llmRequest。
func chatRequestToWire(req *llm.ChatRequest) llmRequest {
	out := llmRequest{
		Model:      req.Model,
		ToolChoice: req.ToolChoice,
		MaxTokens:  req.MaxTokens,
	}
	// executionID is set by the caller (kernelLLMProvider.Stream) after this
	// conversion, since it lives on the provider struct rather than the request.
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
