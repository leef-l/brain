package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

func TestBuildHumanDemoInstruction(t *testing.T) {
	got := buildHumanDemoInstruction("https://example.com/login", "user_demo", "请演示滑块登录")
	if !strings.Contains(got, "https://example.com/login") {
		t.Fatalf("instruction = %q, want url", got)
	}
	if !strings.Contains(got, "human.request_takeover") {
		t.Fatalf("instruction = %q, want human.request_takeover", got)
	}
	if !strings.Contains(got, "/resume") {
		t.Fatalf("instruction = %q, want /resume guidance", got)
	}
}

func TestStartHumanDemoTool_DelegatesHeadedBrowserTakeover(t *testing.T) {
	var captured *kernel.SubtaskRequest
	tool := NewStartHumanDemoTool(nil, nil, nil).(*startHumanDemoTool)
	tool.orchestrator = &kernel.Orchestrator{}
	tool.delegate = func(_ context.Context, req *kernel.SubtaskRequest) (*kernel.SubtaskResult, error) {
		captured = req
		return &kernel.SubtaskResult{
			Status: "completed",
			Output: json.RawMessage(`{"status":"completed"}`),
		}, nil
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{
		"url":"https://example.com/admin",
		"guidance":"请演示登录并完成滑块"
	}`))
	if err != nil {
		t.Fatalf("Execute() err = %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("Execute() = %+v, want success", res)
	}
	if captured == nil {
		t.Fatal("delegate not called")
	}
	if captured.TargetKind != agent.KindBrowser {
		t.Fatalf("target_kind = %q, want browser", captured.TargetKind)
	}
	if captured.Subtask == nil || captured.Subtask.RenderMode != "headed" {
		t.Fatalf("subtask = %+v, want headed render mode", captured.Subtask)
	}
	if !strings.Contains(captured.Instruction, "human.request_takeover") {
		t.Fatalf("instruction = %q, want takeover call", captured.Instruction)
	}
	if !strings.Contains(captured.Instruction, "https://example.com/admin") {
		t.Fatalf("instruction = %q, want url", captured.Instruction)
	}
	if !strings.Contains(captured.Instruction, "不要再次发起 human.request_takeover") {
		t.Fatalf("instruction = %q, want no-repeat guard", captured.Instruction)
	}
}

func TestStartHumanDemoTool_RejectsWhenTakeoverPending(t *testing.T) {
	coord := NewChatHumanCoordinator()
	coord.pending = &pendingTakeover{
		req: tool.HumanTakeoverRequest{
			URL: "https://example.com/login",
		},
		respCh: make(chan tool.HumanTakeoverResponse, 1),
	}

	startTool := NewStartHumanDemoTool(nil, nil, coord).(*startHumanDemoTool)
	startTool.orchestrator = &kernel.Orchestrator{}
	startTool.delegate = func(_ context.Context, req *kernel.SubtaskRequest) (*kernel.SubtaskResult, error) {
		t.Fatalf("delegate should not be called while takeover is pending: %+v", req)
		return nil, nil
	}

	res, err := startTool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/login"}`))
	if err != nil {
		t.Fatalf("Execute() err = %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("Execute() = %+v, want error result", res)
	}
	if !strings.Contains(string(res.Output), "already pending") {
		t.Fatalf("output = %s, want pending warning", string(res.Output))
	}
}
