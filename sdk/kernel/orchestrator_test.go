package kernel

import (
	"context"
	"encoding/json"
	"fmt"
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

// testPool wraps a scriptedRunner to implement BrainPool for tests.
type testPool struct {
	runner    *scriptedRunner
	available map[agent.Kind]bool
	regs      []BrainRegistration
	mu        sync.Mutex
	active    map[agent.Kind]agent.Agent
}

func newTestPool(runner *scriptedRunner) *testPool {
	return &testPool{
		runner:    runner,
		available: make(map[agent.Kind]bool),
		active:    make(map[agent.Kind]agent.Agent),
	}
}

func (p *testPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()
	if ag, ok := p.active[kind]; ok && ag != nil {
		p.mu.Unlock()
		return ag, nil
	}
	p.mu.Unlock()

	desc := agent.Descriptor{Kind: kind, LLMAccess: agent.LLMAccessProxied}
	ag, err := p.runner.Start(ctx, kind, desc)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.active[kind] = ag
	p.mu.Unlock()
	return ag, nil
}

func (p *testPool) Status() map[agent.Kind]BrainStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[agent.Kind]BrainStatus)
	for kind := range p.available {
		result[kind] = BrainStatus{Kind: kind, Running: p.active[kind] != nil}
	}
	return result
}

func (p *testPool) AutoStart(ctx context.Context) {}

func (p *testPool) AvailableKinds() []agent.Kind {
	kinds := make([]agent.Kind, 0, len(p.available))
	for kind := range p.available {
		kinds = append(kinds, kind)
	}
	return kinds
}

func (p *testPool) Registrations() []BrainRegistration {
	return append([]BrainRegistration(nil), p.regs...)
}

func (p *testPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	agents := make(map[agent.Kind]agent.Agent)
	for k, v := range p.active {
		agents[k] = v
	}
	p.active = make(map[agent.Kind]agent.Agent)
	p.mu.Unlock()
	for kind, ag := range agents {
		if ag != nil {
			ag.Shutdown(ctx)
		}
		p.runner.Stop(ctx, kind)
	}
	return nil
}

func (p *testPool) RemoveBrain(kind agent.Kind) {
	p.mu.Lock()
	ag, ok := p.active[kind]
	if ok {
		delete(p.active, kind)
	}
	p.mu.Unlock()
	if ok && ag != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ag.Shutdown(ctx)
		p.runner.Stop(ctx, kind)
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

	pool := newTestPool(runner)
	pool.available[agent.KindBrowser] = true

	orch := NewOrchestratorWithPool(pool, runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindBrowser {
			return "/bin/brain-browser", nil
		}
		return "", nil
	}, OrchestratorConfig{})
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

func TestOrchestratorGetOrStartSidecar_RegistersReverseHandlersOnlyOncePerRPCSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindBrowser, &scriptedSidecar{})

	pool := newTestPool(runner)
	pool.available[agent.KindBrowser] = true

	orch := NewOrchestratorWithPool(pool, runner, &LLMProxy{}, func(kind agent.Kind) (string, error) {
		if kind == agent.KindBrowser {
			return "/bin/brain-browser", nil
		}
		return "", nil
	}, OrchestratorConfig{})
	orch.available[agent.KindBrowser] = true

	ag1, err := orch.getOrStartSidecar(ctx, agent.KindBrowser)
	if err != nil {
		t.Fatalf("first getOrStartSidecar() err = %v", err)
	}
	ag2, err := orch.getOrStartSidecar(ctx, agent.KindBrowser)
	if err != nil {
		t.Fatalf("second getOrStartSidecar() err = %v", err)
	}
	if ag1 != ag2 {
		t.Fatal("expected pooled sidecar instance to be reused")
	}
	if got := runner.starts[agent.KindBrowser]; got != 1 {
		t.Fatalf("runner starts = %d, want 1", got)
	}
	if got := len(orch.reverseHandlersRegistered); got != 1 {
		t.Fatalf("reverseHandlersRegistered = %d, want 1", got)
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

	pool := newTestPool(runner)
	pool.available[agent.KindBrowser] = true

	orch := NewOrchestratorWithPool(pool, runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindBrowser {
			return "/bin/brain-browser", nil
		}
		return "", nil
	}, OrchestratorConfig{})
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

	pool := newTestPool(runner)
	pool.available[agent.KindCode] = true

	orch := &Orchestrator{
		runner:   runner,
		llmProxy: &LLMProxy{ProviderFactory: func(kind agent.Kind) llm.Provider { return provider }},
		available: map[agent.Kind]bool{
			agent.KindCode: true,
		},
		pool: pool,
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

func TestOrchestratorDelegate_ReusedSidecarDoesNotPanicAcrossBrains(t *testing.T) {
	for _, kind := range []agent.Kind{
		agent.KindBrowser,
		agent.KindCode,
		agent.KindVerifier,
		agent.KindFault,
	} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			provider := llm.NewMockProvider("mock")
			provider.QueueText("delegated once by " + string(kind))
			provider.QueueText("delegated twice by " + string(kind))

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
						"max_tokens": 64,
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
			runner.queue(kind, script)

			pool := newTestPool(runner)
			pool.available[kind] = true

			orch := &Orchestrator{
				runner:   runner,
				llmProxy: &LLMProxy{ProviderFactory: func(agent.Kind) llm.Provider { return provider }},
				available: map[agent.Kind]bool{
					kind: true,
				},
				pool: pool,
			}

			first, err := orch.Delegate(ctx, &SubtaskRequest{
				TaskID:      "reuse-1-" + string(kind),
				TargetKind:  kind,
				Instruction: "first delegate for " + string(kind),
			})
			if err != nil {
				t.Fatalf("first Delegate() err = %v", err)
			}
			if first.Status != "completed" {
				t.Fatalf("first status = %q, want completed", first.Status)
			}

			second, err := orch.Delegate(ctx, &SubtaskRequest{
				TaskID:      "reuse-2-" + string(kind),
				TargetKind:  kind,
				Instruction: "second delegate for " + string(kind),
			})
			if err != nil {
				t.Fatalf("second Delegate() err = %v", err)
			}
			if second.Status != "completed" {
				t.Fatalf("second status = %q, want completed", second.Status)
			}

			if got := runner.startCount(kind); got != 1 {
				t.Fatalf("startCount(%s) = %d, want 1 for pooled sidecar reuse", kind, got)
			}
			if got := len(orch.reverseHandlersRegistered); got != 1 {
				t.Fatalf("reverseHandlersRegistered = %d, want 1", got)
			}

			requests := provider.Requests()
			if len(requests) != 2 {
				t.Fatalf("provider requests = %d, want 2", len(requests))
			}
			for i, req := range requests {
				if req.BrainID != string(kind) {
					t.Fatalf("request[%d].BrainID=%q, want %q", i, req.BrainID, kind)
				}
			}

			script.mu.Lock()
			defer script.mu.Unlock()
			if len(script.executeCalls) != 2 {
				t.Fatalf("executeCalls = %d, want 2", len(script.executeCalls))
			}
		})
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

	pool := newTestPool(runner)
	pool.available[agent.KindCode] = true

	orch := &Orchestrator{
		runner: runner,
		available: map[agent.Kind]bool{
			agent.KindCode: true,
		},
		pool: pool,
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

func TestNewOrchestratorWithConfig_OnlyConfiguredBrains(t *testing.T) {
	runner := newScriptedRunner()

	// Only register "code" and "quant" — browser/verifier/fault should NOT be available.
	cfg := OrchestratorConfig{
		Brains: []BrainRegistration{
			{Kind: agent.KindCode, Binary: "/does/not/exist"},
			{Kind: agent.KindQuant, Binary: "/does/not/exist"},
		},
	}

	orch := NewOrchestratorWithConfig(runner, nil, nil, cfg)

	// Neither should be available (binaries don't exist on disk).
	if orch.CanDelegate(agent.KindCode) {
		t.Fatal("code should NOT be available — binary does not exist")
	}
	if orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should NOT be available — binary does not exist")
	}
	// Non-configured brains should also be unavailable.
	if orch.CanDelegate(agent.KindBrowser) {
		t.Fatal("browser should NOT be available — not in config")
	}

	// Registrations should be stored.
	if reg := orch.Registration(agent.KindCode); reg == nil {
		t.Fatal("expected code registration")
	}
	if reg := orch.Registration(agent.KindQuant); reg == nil {
		t.Fatal("expected quant registration")
	}
	if reg := orch.Registration(agent.KindBrowser); reg != nil {
		t.Fatal("browser should NOT be registered")
	}
}

func TestOrchestratorRegister_HotPlug(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindQuant, &scriptedSidecar{})

	pool := newTestPool(runner)

	orch := NewOrchestratorWithPool(pool, runner, nil, nil, OrchestratorConfig{})

	// Initially quant is NOT available.
	if orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should NOT be available before registration")
	}

	// Register with binResolver that returns a valid path.
	orch.binResolver = func(kind agent.Kind) (string, error) {
		if kind == agent.KindQuant {
			return "/bin/sh", nil // exists on every unix system
		}
		return "", fmt.Errorf("unknown kind %s", kind)
	}

	found := orch.Register(BrainRegistration{
		Kind:  agent.KindQuant,
		Model: "claude-sonnet-4-6",
	})

	if !found {
		t.Fatal("Register should have found /bin/sh and marked quant available")
	}
	if !orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should be available after registration")
	}

	// Delegate should work.
	result, err := orch.Delegate(ctx, &SubtaskRequest{
		TaskID:      "quant-1",
		TargetKind:  agent.KindQuant,
		Instruction: "run backtest",
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status=%q, want completed (error=%s)", result.Status, result.Error)
	}

	// Unregister.
	orch.Unregister(agent.KindQuant)
	if orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should NOT be available after unregister")
	}
}

func TestNewOrchestratorWithPool_SyncsPoolCatalog(t *testing.T) {
	runner := newScriptedRunner()
	pool := newTestPool(runner)
	pool.available[agent.KindCode] = true
	pool.available[agent.KindQuant] = true
	pool.regs = []BrainRegistration{
		{Kind: agent.KindCode, Model: "code-model"},
		{Kind: agent.KindQuant, Model: "quant-model"},
	}

	llmProxy := &LLMProxy{}
	orch := NewOrchestratorWithPool(pool, runner, llmProxy, nil, OrchestratorConfig{})

	if !orch.CanDelegate(agent.KindCode) {
		t.Fatal("code should be available from pool catalog")
	}
	if !orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should be available from pool catalog")
	}
	if reg := orch.Registration(agent.KindCode); reg == nil || reg.Model != "code-model" {
		t.Fatalf("code registration=%+v, want model code-model", reg)
	}
	if reg := orch.Registration(agent.KindQuant); reg == nil || reg.Model != "quant-model" {
		t.Fatalf("quant registration=%+v, want model quant-model", reg)
	}
	if llmProxy.ModelForKind[agent.KindCode] != "code-model" {
		t.Fatalf("llm model for code=%q, want code-model", llmProxy.ModelForKind[agent.KindCode])
	}
	if llmProxy.ModelForKind[agent.KindQuant] != "quant-model" {
		t.Fatalf("llm model for quant=%q, want quant-model", llmProxy.ModelForKind[agent.KindQuant])
	}
}

func TestOrchestratorDegradationNotice_ConfigDriven(t *testing.T) {
	cfg := OrchestratorConfig{
		Brains: []BrainRegistration{
			{Kind: agent.KindCode, Binary: "/bin/sh"},    // exists
			{Kind: agent.KindQuant, Binary: "/no/exist"}, // does not exist
		},
	}

	orch := NewOrchestratorWithConfig(nil, nil, nil, cfg)

	notice := orch.DegradationNotice()
	if notice == "" {
		t.Fatal("expected non-empty degradation notice for missing quant binary")
	}
	if !strings.Contains(notice, "quant") {
		t.Fatalf("notice should mention quant: %s", notice)
	}
	// Code should be available (binary exists), so it should NOT be in the notice.
	if strings.Contains(notice, "code") {
		t.Fatalf("notice should NOT mention code (binary exists): %s", notice)
	}
}

func TestLLMProxy_ModelResolutionPriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Track what model the provider receives.
	var receivedModel string
	provider := llm.NewMockProvider("mock")
	provider.QueueText("ok")

	proxy := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
		ModelForKind: map[agent.Kind]string{
			agent.KindQuant: "claude-sonnet-4-6",
		},
	}

	// Case 1: No model in request → uses ModelForKind
	reqParams, _ := json.Marshal(llmCompleteRequest{
		Messages:  []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}}},
		MaxTokens: 64,
	})
	_, err := proxy.handleComplete(ctx, agent.KindQuant, reqParams)
	if err != nil {
		t.Fatalf("handleComplete: %v", err)
	}
	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	receivedModel = requests[0].Model
	if receivedModel != "claude-sonnet-4-6" {
		t.Fatalf("expected model=claude-sonnet-4-6 from ModelForKind, got %q", receivedModel)
	}

	// Case 2: Explicit model in request → overrides ModelForKind
	provider.QueueText("ok2")
	reqParams2, _ := json.Marshal(llmCompleteRequest{
		Messages:  []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}}},
		Model:     "claude-opus-4-6",
		MaxTokens: 64,
	})
	_, err = proxy.handleComplete(ctx, agent.KindQuant, reqParams2)
	if err != nil {
		t.Fatalf("handleComplete: %v", err)
	}
	requests = provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	receivedModel = requests[1].Model
	if receivedModel != "claude-opus-4-6" {
		t.Fatalf("expected model=claude-opus-4-6 from explicit request, got %q", receivedModel)
	}

	// Case 3: No ModelForKind, no request model → empty (provider default)
	provider.QueueText("ok3")
	proxy2 := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
	}
	_, err = proxy2.handleComplete(ctx, agent.KindCode, reqParams)
	if err != nil {
		t.Fatalf("handleComplete: %v", err)
	}
	requests = provider.Requests()
	receivedModel = requests[len(requests)-1].Model
	if receivedModel != "" {
		t.Fatalf("expected empty model (provider default), got %q", receivedModel)
	}
}

func TestOrchestratorConfig_SyncsModelsToLLMProxy(t *testing.T) {
	provider := llm.NewMockProvider("mock")
	proxy := &LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider { return provider },
	}

	cfg := OrchestratorConfig{
		Brains: []BrainRegistration{
			{Kind: agent.KindCode, Binary: "/bin/sh", Model: "claude-sonnet-4-6"},
			{Kind: agent.KindQuant, Binary: "/bin/sh", Model: "deepseek-v3"},
			{Kind: agent.KindData, Binary: "/bin/sh"}, // no model → data brain doesn't use LLM
		},
	}

	_ = NewOrchestratorWithConfig(nil, proxy, nil, cfg)

	// Verify models were synced.
	if proxy.ModelForKind == nil {
		t.Fatal("ModelForKind should have been initialized")
	}
	if m := proxy.ModelForKind[agent.KindCode]; m != "claude-sonnet-4-6" {
		t.Fatalf("code model=%q, want claude-sonnet-4-6", m)
	}
	if m := proxy.ModelForKind[agent.KindQuant]; m != "deepseek-v3" {
		t.Fatalf("quant model=%q, want deepseek-v3", m)
	}
	if m := proxy.ModelForKind[agent.KindData]; m != "" {
		t.Fatalf("data model=%q, want empty (no LLM)", m)
	}
}

// ---------------------------------------------------------------------------
// Quant system cross-brain authorization tests (Doc 35 §5.5)
// ---------------------------------------------------------------------------

func TestDefaultAuthorizer_QuantToDataAllowed(t *testing.T) {
	auth := DefaultSpecialistToolCallAuthorizer()
	ctx := context.Background()

	// Quant → Data: allowed tool prefixes
	for _, tool := range []string{
		"data.get_candles",
		"data.get_all_snapshots",
		"data.get_snapshot",
		"data.get_feature_vector",
		"data.active_instruments",
	} {
		if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindQuant, agent.KindData, tool); err != nil {
			t.Errorf("quant→data:%s should be allowed: %v", tool, err)
		}
	}

	// Quant → Data: disallowed tools
	for _, tool := range []string{
		"data.replay_start",
		"data.backfill_status",
	} {
		if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindQuant, agent.KindData, tool); err == nil {
			t.Errorf("quant→data:%s should be denied", tool)
		}
	}
}

func TestDefaultAuthorizer_QuantToCentralAllowed(t *testing.T) {
	auth := DefaultSpecialistToolCallAuthorizer()
	ctx := context.Background()

	// Quant → Central: allowed
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindQuant, agent.KindCentral, "central.review_trade"); err != nil {
		t.Errorf("quant→central:review_trade should be allowed: %v", err)
	}

	// Quant → Central: disallowed
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindQuant, agent.KindCentral, "central.delegate"); err == nil {
		t.Error("quant→central:delegate should be denied")
	}
}

func TestDefaultAuthorizer_DataToCentralAllowed(t *testing.T) {
	auth := DefaultSpecialistToolCallAuthorizer()
	ctx := context.Background()

	// Data → Central: allowed
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindData, agent.KindCentral, "central.data_alert"); err != nil {
		t.Errorf("data→central:data_alert should be allowed: %v", err)
	}

	// Data → Central: disallowed
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindData, agent.KindCentral, "central.review_trade"); err == nil {
		t.Error("data→central:review_trade should be denied for data brain")
	}
}

func TestDefaultAuthorizer_CrossBrainDenied(t *testing.T) {
	auth := DefaultSpecialistToolCallAuthorizer()
	ctx := context.Background()

	// Code → Data: not allowed (no rule)
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindCode, agent.KindData, "data.get_candles"); err == nil {
		t.Error("code→data should be denied")
	}

	// Data → Quant: not allowed
	if err := auth.AuthorizeSpecialistToolCall(ctx, agent.KindData, agent.KindQuant, "quant.global_portfolio"); err == nil {
		t.Error("data→quant should be denied")
	}
}

func TestSpecialistCallTool_QuantToData_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindData, &scriptedSidecar{
		onToolCall: func(_ context.Context, _ protocol.BidirRPC, req protocol.ToolCallRequest) (interface{}, error) {
			return &protocol.ToolCallResult{
				Tool:    req.Name,
				Output:  json.RawMessage(`{"instrument_id":"BTC-USDT-SWAP","count":100}`),
				Content: []protocol.ToolCallContent{{Type: "text", Text: `{"instrument_id":"BTC-USDT-SWAP","count":100}`}},
			}, nil
		},
	})

	pool := newTestPool(runner)
	pool.available[agent.KindData] = true

	orch := NewOrchestratorWithPool(pool, runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindData {
			return "/bin/brain-data", nil
		}
		return "", fmt.Errorf("not found")
	}, OrchestratorConfig{})
	orch.available[agent.KindData] = true

	// Use HandleSpecialistCallToolFrom as the quant brain would
	handler := orch.HandleSpecialistCallToolFrom(agent.KindQuant)
	resp, err := handler(ctx, json.RawMessage(`{
		"target_kind": "data",
		"tool_name": "data.get_candles",
		"arguments": {"instrument_id": "BTC-USDT-SWAP", "timeframe": "1m"}
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	result, ok := resp.(*protocol.ToolCallResult)
	if !ok {
		t.Fatalf("response type=%T, want *protocol.ToolCallResult", resp)
	}
	if result.Tool != "data.get_candles" {
		t.Fatalf("result.Tool=%q, want data.get_candles", result.Tool)
	}
	if result.IsError {
		t.Fatalf("result is error: %+v", result)
	}
}

func TestSpecialistCallTool_QuantToCentral_ReviewTrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindCentral, &scriptedSidecar{
		onToolCall: func(_ context.Context, _ protocol.BidirRPC, req protocol.ToolCallRequest) (interface{}, error) {
			return &protocol.ToolCallResult{
				Tool:    req.Name,
				Output:  json.RawMessage(`{"approved":true,"confidence":0.85}`),
				Content: []protocol.ToolCallContent{{Type: "text", Text: `{"approved":true}`}},
			}, nil
		},
	})

	pool := newTestPool(runner)
	pool.available[agent.KindCentral] = true

	orch := NewOrchestratorWithPool(pool, runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindCentral {
			return "/bin/brain-central", nil
		}
		return "", fmt.Errorf("not found")
	}, OrchestratorConfig{})
	orch.available[agent.KindCentral] = true

	handler := orch.HandleSpecialistCallToolFrom(agent.KindQuant)
	resp, err := handler(ctx, json.RawMessage(`{
		"target_kind": "central",
		"tool_name": "central.review_trade",
		"arguments": {"symbol": "BTC-USDT-SWAP", "direction": "long", "quantity": 0.1}
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	result := resp.(*protocol.ToolCallResult)
	if result.Tool != "central.review_trade" {
		t.Fatalf("result.Tool=%q, want central.review_trade", result.Tool)
	}
}

func TestSpecialistCallTool_DataToCentral_DataAlert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runner := newScriptedRunner()
	runner.queue(agent.KindCentral, &scriptedSidecar{})

	pool := newTestPool(runner)
	pool.available[agent.KindCentral] = true

	orch := NewOrchestratorWithPool(pool, runner, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindCentral {
			return "/bin/brain-central", nil
		}
		return "", fmt.Errorf("not found")
	}, OrchestratorConfig{})
	orch.available[agent.KindCentral] = true

	handler := orch.HandleSpecialistCallToolFrom(agent.KindData)
	resp, err := handler(ctx, json.RawMessage(`{
		"target_kind": "central",
		"tool_name": "central.data_alert",
		"arguments": {"level": "warning", "type": "price_spike", "symbol": "BTC-USDT-SWAP"}
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	result := resp.(*protocol.ToolCallResult)
	if result.Tool != "central.data_alert" {
		t.Fatalf("result.Tool=%q, want central.data_alert", result.Tool)
	}
}

func TestSpecialistCallTool_UnauthorizedRoute_Denied(t *testing.T) {
	orch := NewOrchestrator(nil, nil, nil)

	// Code brain trying to call data.get_candles — should be denied
	handler := orch.HandleSpecialistCallToolFrom(agent.KindCode)
	_, err := handler(context.Background(), json.RawMessage(`{
		"target_kind": "data",
		"tool_name": "data.get_candles",
		"arguments": {}
	}`))
	if err == nil {
		t.Fatal("expected authorization error for code→data")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error=%v, want 'not allowed'", err)
	}

	// Quant trying to replay data — should be denied (not in prefix list)
	handler = orch.HandleSpecialistCallToolFrom(agent.KindQuant)
	_, err = handler(context.Background(), json.RawMessage(`{
		"target_kind": "data",
		"tool_name": "data.replay_start",
		"arguments": {}
	}`))
	if err == nil {
		t.Fatal("expected authorization error for quant→data.replay_start")
	}
}

func TestNewOrchestrator_BackwardCompatible(t *testing.T) {
	// When no config is provided, all built-in kinds should be probed.
	orch := NewOrchestrator(nil, nil, func(kind agent.Kind) (string, error) {
		if kind == agent.KindCode {
			return "/bin/sh", nil // exists
		}
		return "", fmt.Errorf("not found")
	})

	if !orch.CanDelegate(agent.KindCode) {
		t.Fatal("code should be available via backward-compatible binResolver")
	}
	// Data and quant are now in BuiltinKinds but no binary found.
	if orch.CanDelegate(agent.KindData) {
		t.Fatal("data should NOT be available — no binary")
	}
	if orch.CanDelegate(agent.KindQuant) {
		t.Fatal("quant should NOT be available — no binary")
	}
}
