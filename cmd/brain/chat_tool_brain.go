package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// brainManageTool lets the LLM list, start, and stop specialist brains.
type brainManageTool struct {
	orchestrator *kernel.Orchestrator
}

func newBrainManageTool(orch *kernel.Orchestrator) tool.Tool {
	return &brainManageTool{orchestrator: orch}
}

func (t *brainManageTool) Name() string { return "central.brain_manage" }

func (t *brainManageTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "central.brain_manage",
		Description: "管理专精大脑 sidecar 的生命周期：查看所有大脑状态、启动或停止指定大脑。",
		Brain:       "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["list", "start", "stop"],
					"description": "操作类型：list=查看所有大脑状态，start=启动指定大脑，stop=停止指定大脑（kind='all' 停止全部）"
				},
				"kind": {
					"type": "string",
					"description": "大脑类型（如 data、quant、code 等），action=list 时可省略，action=stop 传 'all' 停止全部"
				}
			},
			"required": ["action"]
		}`),
	}
}

func (t *brainManageTool) Risk() tool.Risk { return tool.RiskSafe }

func (t *brainManageTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var params struct {
		Action string `json:"action"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %v"`, err)),
			IsError: true,
		}, nil
	}

	if t.orchestrator == nil {
		return &tool.Result{
			Output:  json.RawMessage(`"no orchestrator available (solo mode, no specialist brains)"`),
			IsError: true,
		}, nil
	}

	switch params.Action {
	case "list":
		return t.listBrains()
	case "start":
		if params.Kind == "" {
			return &tool.Result{
				Output:  json.RawMessage(`"start requires 'kind' parameter"`),
				IsError: true,
			}, nil
		}
		return t.startBrain(ctx, params.Kind)
	case "stop":
		if params.Kind == "" {
			return &tool.Result{
				Output:  json.RawMessage(`"stop requires 'kind' parameter (use 'all' to stop all)"`),
				IsError: true,
			}, nil
		}
		return t.stopBrain(ctx, params.Kind)
	default:
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"unknown action: %s, expected list/start/stop"`, params.Action)),
			IsError: true,
		}, nil
	}
}

func (t *brainManageTool) listBrains() (*tool.Result, error) {
	brains := t.orchestrator.ListBrains()
	if len(brains) == 0 {
		return &tool.Result{
			Output: json.RawMessage(`"no specialist brains available"`),
		}, nil
	}

	sort.Slice(brains, func(i, j int) bool { return brains[i].Kind < brains[j].Kind })

	type brainInfo struct {
		Kind    string `json:"kind"`
		Running bool   `json:"running"`
		Binary  string `json:"binary,omitempty"`
	}
	info := make([]brainInfo, len(brains))
	for i, b := range brains {
		info[i] = brainInfo{
			Kind:    string(b.Kind),
			Running: b.Running,
			Binary:  b.Binary,
		}
	}

	out, _ := json.Marshal(info)
	return &tool.Result{Output: out}, nil
}

func (t *brainManageTool) startBrain(ctx context.Context, kind string) (*tool.Result, error) {
	if err := t.orchestrator.StartBrain(ctx, agent.Kind(kind)); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"failed to start %s: %v"`, kind, err)),
			IsError: true,
		}, nil
	}
	out, _ := json.Marshal(map[string]string{
		"status": "started",
		"kind":   kind,
	})
	return &tool.Result{Output: out}, nil
}

func (t *brainManageTool) stopBrain(ctx context.Context, kind string) (*tool.Result, error) {
	if strings.ToLower(kind) == "all" {
		if err := t.orchestrator.Shutdown(ctx); err != nil {
			return &tool.Result{
				Output:  json.RawMessage(fmt.Sprintf(`"failed to stop all: %v"`, err)),
				IsError: true,
			}, nil
		}
		out, _ := json.Marshal(map[string]string{"status": "all stopped"})
		return &tool.Result{Output: out}, nil
	}

	if err := t.orchestrator.StopBrain(ctx, agent.Kind(kind)); err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"failed to stop %s: %v"`, kind, err)),
			IsError: true,
		}, nil
	}
	out, _ := json.Marshal(map[string]string{
		"status": "stopped",
		"kind":   kind,
	})
	return &tool.Result{Output: out}, nil
}
