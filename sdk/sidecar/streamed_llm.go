package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// CallKernelStreamed 向 Kernel 发起流式 LLM 调用，同时消费实时 delta 事件并通过
// brain/progress 逐事件推回 Host。返回聚合后的完整文本。
//
// 这是 sidecar 实现"真实流式"的标准方式：替代直接 CallKernel("llm.stream") 等聚合
// 响应的做法，让 delta 在毫秒级延迟内到达客户端。
//
// 当 executionID 为空时，自动退回到 llm.complete（非流式）。
func CallKernelStreamed(ctx context.Context, caller KernelCaller, req *llm.ChatRequest, executionID string) (string, error) {
	if caller == nil {
		return "", fmt.Errorf("CallKernelStreamed: caller is nil")
	}

	// 非流式场景退回到 llm.complete
	if executionID == "" {
		wire := chatRequestToWire(req)
		var resp llmResponse
		if err := caller.CallKernel(ctx, "llm.complete", wire, &resp); err != nil {
			return "", err
		}
		return extractResponseText(&resp), nil
	}

	provider := NewKernelLLMProvider(caller, "kernel", executionID)
	reader, err := provider.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("CallKernelStreamed: provider.Stream: %w", err)
	}
	defer reader.Close()

	EmitProgress(ctx, ProgressEvent{
		Kind:        "llm_start",
		ExecutionID: executionID,
	})

	var contentBuilder strings.Builder

	for {
		ev, err := reader.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("CallKernelStreamed: stream read: %w", err)
		}

		switch ev.Type {
		case llm.EventContentDelta:
			var delta struct {
				Text string `json:"text"`
				Kind string `json:"kind"`
			}
			if json.Unmarshal(ev.Data, &delta) == nil && delta.Text != "" {
				contentBuilder.WriteString(delta.Text)
				// NOTE: Host already publishes llm.content_delta directly to the
				// EventBus in LLMProxy.handleStream(). If we EmitProgress("content")
				// here, the client would receive duplicate deltas. We intentionally
				// skip content delta progress and only emit lifecycle events
				// (llm_start / llm_end / tool_call_delta) so the client knows the
				// sidecar is actively consuming the stream without double-printing
				// tokens.
			}

		case llm.EventToolCallDelta:
			EmitProgress(ctx, ProgressEvent{
				Kind:        "tool_call_delta",
				ExecutionID: executionID,
				Detail:      string(ev.Data),
			})

		case llm.EventMessageDelta:
			EmitProgress(ctx, ProgressEvent{
				Kind:        "llm_delta",
				ExecutionID: executionID,
				Detail:      string(ev.Data),
			})

		case llm.EventMessageEnd:
			EmitProgress(ctx, ProgressEvent{
				Kind:        "llm_end",
				ExecutionID: executionID,
			})
		}
	}

	return contentBuilder.String(), nil
}

// extractResponseText 从 llmResponse 中提取所有文本/thinking 内容。
func extractResponseText(resp *llmResponse) string {
	var parts []string
	for _, b := range resp.Content {
		if b.Type == "text" || b.Type == "thinking" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}
