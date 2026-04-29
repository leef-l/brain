package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

type startHumanDemoTool struct {
	orchestrator *kernel.Orchestrator
	env          *env.Environment
	humanCoord   *ChatHumanCoordinator
	delegate     func(context.Context, *kernel.DelegateRequest) (*kernel.DelegateResult, error)
}

func NewStartHumanDemoTool(orch *kernel.Orchestrator, e *env.Environment, coord *ChatHumanCoordinator) tool.Tool {
	t := &startHumanDemoTool{
		orchestrator: orch,
		env:          e,
		humanCoord:   coord,
	}
	if orch != nil {
		t.delegate = orch.Delegate
	}
	return t
}

func (t *startHumanDemoTool) Name() string { return "central.start_human_demo" }

func (t *startHumanDemoTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "central.start_human_demo",
		Description: "强制启动一个面向人工演示/学习的浏览器任务：打开指定页面（可选），立即进入 human.request_takeover 录制模式，等待用户在浏览器里演示操作并通过 /resume 继续。",
		Brain:       "central",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "可选。要打开并进入人工演示模式的网址。留空表示在当前页面进入人工演示。"
				},
				"guidance": {
					"type": "string",
					"description": "可选。展示给人工操作者的指导语。"
				},
				"reason": {
					"type": "string",
					"description": "可选。human.request_takeover 的 reason。默认 user_demo。"
				}
			}
		}`),
	}
}

func (t *startHumanDemoTool) Risk() tool.Risk { return tool.RiskMedium }

func (t *startHumanDemoTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	var input struct {
		URL      string `json:"url"`
		Guidance string `json:"guidance"`
		Reason   string `json:"reason"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return &tool.Result{
				Output:  json.RawMessage(fmt.Sprintf(`"invalid arguments: %v"`, err)),
				IsError: true,
			}, nil
		}
	}
	if t.orchestrator == nil || t.delegate == nil {
		return &tool.Result{
			Output:  json.RawMessage(`"no orchestrator available (solo mode, no specialist brains)"`),
			IsError: true,
		}, nil
	}
	if t.humanCoord != nil {
		if req, ok := t.humanCoord.Pending(); ok {
			msg := "a human takeover is already pending"
			if strings.TrimSpace(req.URL) != "" {
				msg += " for " + strings.TrimSpace(req.URL)
			}
			msg += "; finish it with /resume or /abort before starting another human demo"
			return &tool.Result{
				Output:  json.RawMessage(fmt.Sprintf("%q", msg)),
				IsError: true,
			}, nil
		}
	}

	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "user_demo"
	}
	guidance := strings.TrimSpace(input.Guidance)
	if guidance == "" {
		guidance = "用户要亲自演示操作，请保持浏览器窗口打开并等待 /resume。"
	}

	instruction := buildHumanDemoInstruction(input.URL, reason, guidance)
	req := &kernel.DelegateRequest{
		TaskID:      "human-demo-browser",
		TargetKind:  agent.KindBrowser,
		Instruction: instruction,
		Subtask: &protocol.SubtaskContext{
			RenderMode: "headed",
		},
	}
	if t.env != nil {
		req.Execution = t.env.ExecutionSpec()
	}

	result, err := t.delegate(ctx, req)
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"failed to start human demo: %v"`, err)),
			IsError: true,
		}, nil
	}
	if result == nil {
		return &tool.Result{
			Output:  json.RawMessage(`"human demo delegate returned nil result"`),
			IsError: true,
		}, nil
	}
	if result.Status == "failed" || result.Status == "canceled" {
		msg := strings.TrimSpace(result.Error)
		if msg == "" {
			msg = "human demo did not complete"
		}
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`"human demo failed: %s"`, msg)),
			IsError: true,
		}, nil
	}
	if result.Output != nil {
		return &tool.Result{Output: result.Output}, nil
	}
	out, _ := json.Marshal(map[string]string{
		"status": "started",
		"mode":   "human_demo",
	})
	return &tool.Result{Output: out}, nil
}

func buildHumanDemoInstruction(url, reason, guidance string) string {
	var parts []string
	if strings.TrimSpace(url) != "" {
		parts = append(parts, fmt.Sprintf("打开网址 %s。", strings.TrimSpace(url)))
	}
	parts = append(parts,
		fmt.Sprintf("到位后立即调用 human.request_takeover，reason=%q，guidance=%q。", reason, guidance),
		"在人工 /resume 之前不要继续任何自动化操作。",
		"人工 /resume 后只做必要的页面读取与简短总结，用于确认演示已完成；不要再次发起 human.request_takeover。",
	)
	return strings.Join(parts, " ")
}
