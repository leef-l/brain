package sidecar

import (
	"context"
	"fmt"

	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

// HumanTakeoverBridge 是 sidecar 侧的 tool.HumanTakeoverCoordinator 实现。
// 它把 human.request_takeover 的请求通过反向 RPC 转发到 kernel 进程,
// kernel 那边把请求路由到真正的协调器(serve 的 HostHumanTakeoverCoordinator,
// chat 的 ChatHumanCoordinator)。
//
// 这让 sidecar 里任意工具调用 human.request_takeover 都能阻塞到人类
// /resume 或 /abort,和主进程的交互通道对齐。
type HumanTakeoverBridge struct {
	caller KernelCaller
}

// NewHumanTakeoverBridge 绑定一个 KernelCaller。
func NewHumanTakeoverBridge(caller KernelCaller) *HumanTakeoverBridge {
	return &HumanTakeoverBridge{caller: caller}
}

// RequestTakeover 实现 tool.HumanTakeoverCoordinator。
func (b *HumanTakeoverBridge) RequestTakeover(ctx context.Context, req tool.HumanTakeoverRequest) tool.HumanTakeoverResponse {
	if b == nil || b.caller == nil {
		return tool.HumanTakeoverResponse{
			Outcome: tool.HumanOutcomeAborted,
			Note:    "bridge: no kernel caller",
		}
	}
	var resp tool.HumanTakeoverResponse
	if err := b.caller.CallKernel(ctx, protocol.MethodHumanRequestTakeover, req, &resp); err != nil {
		return tool.HumanTakeoverResponse{
			Outcome: tool.HumanOutcomeAborted,
			Note:    fmt.Sprintf("bridge: kernel call failed: %v", err),
		}
	}
	return resp
}
