package kernel

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/executionpolicy"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/sidecar"
)

type scriptedSidecar struct {
	mu           sync.Mutex
	executeCalls []sidecar.ExecuteRequest
	toolCalls    []protocol.ToolCallRequest
	onExecute    func(context.Context, protocol.BidirRPC, sidecar.ExecuteRequest) (interface{}, error)
	onToolCall   func(context.Context, protocol.BidirRPC, protocol.ToolCallRequest) (interface{}, error)
}

type scriptedRunner struct {
	mu      sync.Mutex
	starts  map[agent.Kind]int
	stops   map[agent.Kind]int
	scripts map[agent.Kind][]*scriptedSidecar
	agents  map[agent.Kind]*testRPCAgent
}

type testRPCAgent struct {
	kind      agent.Kind
	desc      agent.Descriptor
	rpc       protocol.BidirRPC
	shutdown  func(context.Context) error
	processMu sync.Mutex
	exited    bool
}

func (a *testRPCAgent) Kind() agent.Kind             { return a.kind }
func (a *testRPCAgent) Descriptor() agent.Descriptor { return a.desc }
func (a *testRPCAgent) Ready(context.Context) error  { return nil }
func (a *testRPCAgent) RPC() interface{}             { return a.rpc }

func (a *testRPCAgent) Shutdown(ctx context.Context) error {
	if a.shutdown == nil {
		return nil
	}
	err := a.shutdown(ctx)
	a.processMu.Lock()
	a.exited = true
	a.processMu.Unlock()
	return err
}

func (a *testRPCAgent) ProcessExited() bool {
	a.processMu.Lock()
	defer a.processMu.Unlock()
	return a.exited
}

func newScriptedRunner() *scriptedRunner {
	return &scriptedRunner{
		starts:  make(map[agent.Kind]int),
		stops:   make(map[agent.Kind]int),
		scripts: make(map[agent.Kind][]*scriptedSidecar),
		agents:  make(map[agent.Kind]*testRPCAgent),
	}
}

func (r *scriptedRunner) queue(kind agent.Kind, script *scriptedSidecar) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts[kind] = append(r.scripts[kind], script)
}

func (r *scriptedRunner) Start(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error) {
	r.mu.Lock()
	r.starts[kind]++
	queue := r.scripts[kind]
	if len(queue) == 0 {
		r.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("no scripted sidecar queued"))
	}
	script := queue[0]
	r.scripts[kind] = queue[1:]
	r.mu.Unlock()

	kernelRPC, sidecarRPC, cleanup, err := newFullDuplexRPC(ctx)
	if err != nil {
		return nil, err
	}

	sidecarRPC.Handle(protocol.MethodInitialize, func(context.Context, json.RawMessage) (interface{}, error) {
		return &protocol.InitializeResponse{
			ProtocolVersion:   "1.0",
			BrainVersion:      "test-" + string(kind),
			BrainCapabilities: map[string]bool{"streaming": true},
			SupportedTools:    desc.SupportedTools,
		}, nil
	})
	sidecarRPC.Handle(protocol.MethodShutdown, func(context.Context, json.RawMessage) (interface{}, error) {
		return map[string]bool{"ok": true}, nil
	})
	sidecarRPC.Handle("brain/execute", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req sidecar.ExecuteRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}

		script.mu.Lock()
		script.executeCalls = append(script.executeCalls, req)
		script.mu.Unlock()

		if script.onExecute == nil {
			return &sidecar.ExecuteResult{
				Status:  "completed",
				Summary: "ok",
				Turns:   1,
			}, nil
		}
		return script.onExecute(ctx, sidecarRPC, req)
	})
	sidecarRPC.Handle("tools/call", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req protocol.ToolCallRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}

		script.mu.Lock()
		script.toolCalls = append(script.toolCalls, req)
		script.mu.Unlock()

		if script.onToolCall == nil {
			return &protocol.ToolCallResult{
				Tool:    req.Name,
				Output:  json.RawMessage(`{"status":"ok"}`),
				Content: []protocol.ToolCallContent{{Type: "text", Text: `{"status":"ok"}`}},
			}, nil
		}
		return script.onToolCall(ctx, sidecarRPC, req)
	})

	ag := &testRPCAgent{
		kind: kind,
		desc: agent.Descriptor{
			Kind:           kind,
			LLMAccess:      desc.LLMAccess,
			SupportedTools: desc.SupportedTools,
		},
		rpc: kernelRPC,
	}
	ag.shutdown = func(context.Context) error {
		cleanup()
		return nil
	}

	r.mu.Lock()
	r.agents[kind] = ag
	r.mu.Unlock()
	return ag, nil
}

func TestOrchestratorCallTool_ForwardsExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	script := &scriptedSidecar{
		onToolCall: func(_ context.Context, _ protocol.BidirRPC, req protocol.ToolCallRequest) (interface{}, error) {
			return &protocol.ToolCallResult{
				Tool:    req.Name,
				Output:  append(json.RawMessage(nil), req.Arguments...),
				Content: []protocol.ToolCallContent{{Type: "text", Text: string(req.Arguments)}},
				IsError: false,
			}, nil
		},
	}
	runner.queue(agent.KindBrowser, script)

	orch := NewOrchestrator(runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindBrowser {
			return "/bin/brain-browser", nil
		}
		return "", nil
	})
	orch.available[agent.KindBrowser] = true

	allowRead := []string{"docs/**/*.md"}
	req := &protocol.SpecialistToolCallRequest{
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.eval",
		Arguments:  json.RawMessage(`{"expression":"document.title"}`),
		Execution: &executionpolicy.ExecutionSpec{
			Workdir: "/tmp/work",
			FilePolicy: &executionpolicy.FilePolicySpec{
				AllowRead: allowRead,
			},
		},
	}

	result, err := orch.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned error result: %+v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `{"expression":"document.title"}` {
		t.Fatalf("unexpected tool result: %+v", result)
	}

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.toolCalls) != 1 {
		t.Fatalf("toolCalls=%d, want 1", len(script.toolCalls))
	}
	got := script.toolCalls[0]
	if got.Name != "browser.eval" {
		t.Fatalf("tool name=%q, want browser.eval", got.Name)
	}
	if got.Execution == nil || got.Execution.Workdir != "/tmp/work" {
		t.Fatalf("execution=%+v, want workdir /tmp/work", got.Execution)
	}
	if got.Execution.FilePolicy == nil || len(got.Execution.FilePolicy.AllowRead) != 1 || got.Execution.FilePolicy.AllowRead[0] != "docs/**/*.md" {
		t.Fatalf("execution.file_policy=%+v", got.Execution.FilePolicy)
	}
}

func TestHandleSpecialistCallToolFrom_DeniesUnauthorizedCaller(t *testing.T) {
	orch := NewOrchestrator(nil, nil, nil)
	handler := orch.HandleSpecialistCallToolFrom(agent.KindCode)

	_, err := handler(context.Background(), json.RawMessage(`{
		"target_kind":"browser",
		"tool_name":"browser.eval",
		"arguments":{"expression":"document.title"}
	}`))
	if err == nil {
		t.Fatal("expected authorization error")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error=%v, want authorization failure", err)
	}
}

func TestHandleSpecialistCallToolFrom_AllowsVerifierBrowserRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindBrowser, &scriptedSidecar{})

	orch := NewOrchestrator(runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindBrowser {
			return "/bin/brain-browser", nil
		}
		return "", nil
	})
	orch.available[agent.KindBrowser] = true

	handler := orch.HandleSpecialistCallToolFrom(agent.KindVerifier)
	resp, err := handler(ctx, json.RawMessage(`{
		"target_kind":"browser",
		"tool_name":"browser.screenshot",
		"arguments":{"selector":"#chart"}
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if result.Tool != "browser.screenshot" {
		t.Fatalf("result.Tool=%q, want browser.screenshot", result.Tool)
	}
}

func (r *scriptedRunner) Stop(ctx context.Context, kind agent.Kind) error {
	r.mu.Lock()
	r.stops[kind]++
	ag := r.agents[kind]
	delete(r.agents, kind)
	r.mu.Unlock()

	if ag == nil {
		return nil
	}
	return ag.Shutdown(ctx)
}

func (r *scriptedRunner) startCount(kind agent.Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[kind]
}

func (r *scriptedRunner) stopCount(kind agent.Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stops[kind]
}

func newFullDuplexRPC(ctx context.Context) (protocol.BidirRPC, protocol.BidirRPC, func(), error) {
	kernelToSidecarR, kernelToSidecarW := io.Pipe()
	sidecarToKernelR, sidecarToKernelW := io.Pipe()

	kernelRPC := protocol.NewBidirRPC(
		protocol.RoleKernel,
		protocol.NewFrameReader(sidecarToKernelR),
		protocol.NewFrameWriter(kernelToSidecarW),
	)
	sidecarRPC := protocol.NewBidirRPC(
		protocol.RoleSidecar,
		protocol.NewFrameReader(kernelToSidecarR),
		protocol.NewFrameWriter(sidecarToKernelW),
	)

	if err := kernelRPC.Start(ctx); err != nil {
		return nil, nil, nil, err
	}
	if err := sidecarRPC.Start(ctx); err != nil {
		_ = kernelRPC.Close()
		return nil, nil, nil, err
	}

	cleanup := func() {
		_ = kernelRPC.Close()
		_ = sidecarRPC.Close()
		_ = kernelToSidecarR.Close()
		_ = kernelToSidecarW.Close()
		_ = sidecarToKernelR.Close()
		_ = sidecarToKernelW.Close()
	}
	return kernelRPC, sidecarRPC, cleanup, nil
}

func TestOrchestratorHandleSubtaskDelegate_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	provider := llm.NewMockProvider("mock")
	provider.QueueText("delegated by code brain")

	runner := newScriptedRunner()
	script := &scriptedSidecar{
		onExecute: func(ctx context.Context, sidecarRPC protocol.BidirRPC, req sidecar.ExecuteRequest) (interface{}, error) {
			var resp struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text,omitempty"`
				} `json:"content"`
			}

			llmReq := map[string]interface{}{
				"messages": []llm.Message{
					{
						Role: "user",
						Content: []llm.ContentBlock{
							{Type: "text", Text: req.Instruction},
						},
					},
				},
				"max_tokens": 128,
			}
			if err := sidecarRPC.Call(ctx, protocol.MethodLLMComplete, llmReq, &resp); err != nil {
				return nil, err
			}

			summary := ""
			if len(resp.Content) > 0 {
				summary = resp.Content[0].Text
			}
			return &sidecar.ExecuteResult{
				Status:  "completed",
				Summary: summary,
				Turns:   1,
			}, nil
		},
	}
	runner.queue(agent.KindCode, script)

	orch := &Orchestrator{
		runner:   runner,
		llmProxy: &LLMProxy{ProviderFactory: func(kind agent.Kind) llm.Provider { return provider }},
		available: map[agent.Kind]bool{
			agent.KindCode: true,
		},
		active: make(map[agent.Kind]agent.Agent),
	}

	kernelRPC, centralRPC, cleanup, err := newFullDuplexRPC(ctx)
	if err != nil {
		t.Fatalf("newFullDuplexRPC: %v", err)
	}
	defer cleanup()

	kernelRPC.Handle(protocol.MethodSubtaskDelegate, orch.HandleSubtaskDelegate())

	req := &SubtaskRequest{
		TaskID:      "task-code-1",
		TargetKind:  agent.KindCode,
		Instruction: "fix the orchestrator tests",
		Context:     json.RawMessage(`{"files":["kernel/orchestrator.go"]}`),
	}

	var result SubtaskResult
	if err := centralRPC.Call(ctx, protocol.MethodSubtaskDelegate, req, &result); err != nil {
		t.Fatalf("subtask.delegate: %v", err)
	}

	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed (error=%s)", result.Status, result.Error)
	}

	var output sidecar.ExecuteResult
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if output.Summary != "delegated by code brain" {
		t.Fatalf("summary=%q, want delegated by code brain", output.Summary)
	}
	if output.Turns != 1 {
		t.Fatalf("turns=%d, want 1", output.Turns)
	}

	if runner.startCount(agent.KindCode) != 1 {
		t.Fatalf("startCount=%d, want 1", runner.startCount(agent.KindCode))
	}

	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d, want 1", len(requests))
	}
	if requests[0].BrainID != string(agent.KindCode) {
		t.Fatalf("provider BrainID=%q, want %q", requests[0].BrainID, agent.KindCode)
	}

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.executeCalls) != 1 {
		t.Fatalf("executeCalls=%d, want 1", len(script.executeCalls))
	}
	if script.executeCalls[0].TaskID != req.TaskID {
		t.Fatalf("TaskID=%q, want %q", script.executeCalls[0].TaskID, req.TaskID)
	}
	if script.executeCalls[0].Instruction != req.Instruction {
		t.Fatalf("Instruction=%q, want %q", script.executeCalls[0].Instruction, req.Instruction)
	}
}

func TestOrchestratorDelegate_RetriesAfterSidecarCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindCode, &scriptedSidecar{
		onExecute: func(context.Context, protocol.BidirRPC, sidecar.ExecuteRequest) (interface{}, error) {
			return nil, brainerrors.New(brainerrors.CodeSidecarCrashed,
				brainerrors.WithMessage("simulated crash"))
		},
	})
	runner.queue(agent.KindCode, &scriptedSidecar{
		onExecute: func(context.Context, protocol.BidirRPC, sidecar.ExecuteRequest) (interface{}, error) {
			return &sidecar.ExecuteResult{
				Status:  "completed",
				Summary: "retry succeeded",
				Turns:   1,
			}, nil
		},
	})

	orch := &Orchestrator{
		runner: runner,
		available: map[agent.Kind]bool{
			agent.KindCode: true,
		},
		active: make(map[agent.Kind]agent.Agent),
	}

	result, err := orch.Delegate(ctx, &SubtaskRequest{
		TaskID:      "retry-1",
		TargetKind:  agent.KindCode,
		Instruction: "retry after crash",
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed (error=%s)", result.Status, result.Error)
	}

	var output sidecar.ExecuteResult
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if output.Summary != "retry succeeded" {
		t.Fatalf("summary=%q, want retry succeeded", output.Summary)
	}

	if runner.startCount(agent.KindCode) != 2 {
		t.Fatalf("startCount=%d, want 2", runner.startCount(agent.KindCode))
	}
	if runner.stopCount(agent.KindCode) != 1 {
		t.Fatalf("stopCount=%d, want 1", runner.stopCount(agent.KindCode))
	}
}
