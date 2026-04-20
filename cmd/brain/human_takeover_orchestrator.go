package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// installHumanTakeoverBridge 把 orchestrator 的反向 RPC handler 连到
// tool.currentTakeover()。sidecar 里的 human.request_takeover 工具通过
// HumanTakeoverBridge 反向 RPC 过来,我们这里调 tool 包已注入的协调器
// (ChatHumanCoordinator / HostHumanTakeoverCoordinator),返回 outcome。
//
// 避免 sdk/kernel 直接依赖 sdk/tool(已有反向依赖 tool->kernel),把胶水
// 放在 cmd/brain 层。
func installHumanTakeoverBridge(orch *kernel.Orchestrator) {
	if orch == nil {
		return
	}
	orch.SetHumanTakeoverHandler(func(ctx context.Context, callerKind string, params json.RawMessage) (interface{}, error) {
		coord := tool.CurrentHumanTakeoverCoordinator()
		if coord == nil {
			return tool.HumanTakeoverResponse{
				Outcome: tool.HumanOutcomeAborted,
				Note:    "no human coordinator configured in host",
			}, nil
		}
		var req tool.HumanTakeoverRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("unmarshal HumanTakeoverRequest: %w", err)
		}
		if req.BrainKind == "" {
			req.BrainKind = callerKind
		}
		return coord.RequestTakeover(ctx, req), nil
	})
}
