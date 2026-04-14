package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/internal/central"
	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/reviewrun"
	"github.com/leef-l/brain/internal/quantcontracts"
	"github.com/leef-l/brain/license"
	"github.com/leef-l/brain/persistence"
	"github.com/leef-l/brain/protocol"
	"github.com/leef-l/brain/sidecar"
)

type mockKernelCaller struct {
	call func(method string, params interface{}, result interface{}) error
}

func (m mockKernelCaller) CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error {
	if m.call != nil {
		return m.call(method, params, result)
	}
	return nil
}

type fakeCentralService struct {
	runReview       func(context.Context, reviewrun.Request) (reviewrun.Result, error)
	emergencyAction func(context.Context, control.EmergencyActionRequest) (control.Result, error)
	updateConfig    func(context.Context, control.ConfigUpdateRequest) (control.Result, error)
	health          func() central.Health
}

func (f fakeCentralService) ReviewTrade(ctx context.Context, req quantcontracts.ReviewTradeRequest) (quantcontracts.ReviewDecision, error) {
	return quantcontracts.ReviewDecision{}, nil
}

func (f fakeCentralService) DataAlert(ctx context.Context, alert quantcontracts.DataAlert) (control.Result, error) {
	return control.Result{}, nil
}

func (f fakeCentralService) AccountError(ctx context.Context, err quantcontracts.AccountError) (control.Result, error) {
	return control.Result{}, nil
}

func (f fakeCentralService) MacroEvent(ctx context.Context, event quantcontracts.MacroEvent) (control.Result, error) {
	return control.Result{}, nil
}

func (f fakeCentralService) EmergencyAction(ctx context.Context, req control.EmergencyActionRequest) (control.Result, error) {
	if f.emergencyAction != nil {
		return f.emergencyAction(ctx, req)
	}
	return control.Result{}, nil
}

func (f fakeCentralService) UpdateConfig(ctx context.Context, req control.ConfigUpdateRequest) (control.Result, error) {
	if f.updateConfig != nil {
		return f.updateConfig(ctx, req)
	}
	return control.Result{}, nil
}

func (f fakeCentralService) RunReview(ctx context.Context, req reviewrun.Request) (reviewrun.Result, error) {
	if f.runReview != nil {
		return f.runReview(ctx, req)
	}
	return reviewrun.Result{}, nil
}

func (f fakeCentralService) State() central.State {
	if f.health != nil {
		return f.health().State
	}
	return central.State{}
}

func (f fakeCentralService) Health() central.Health {
	if f.health != nil {
		return f.health()
	}
	return central.Health{}
}

func TestCentralHandlerToolSchemasExposeOutputSchemas(t *testing.T) {
	h := &centralHandler{}
	schemas := h.ToolSchemas()
	if got, want := len(schemas), 9; got != want {
		t.Fatalf("unexpected schema count: got %d want %d", got, want)
	}
	tools := h.Tools()
	if len(tools) != len(schemas) {
		t.Fatalf("Tools and ToolSchemas diverged: %d vs %d", len(tools), len(schemas))
	}
	for i, schema := range schemas {
		if schema.Name != tools[i] {
			t.Fatalf("tool order mismatch at %d: %q vs %q", i, schema.Name, tools[i])
		}
		if len(schema.OutputSchema) == 0 {
			t.Fatalf("%s missing output schema", schema.Name)
		}
		if !json.Valid(schema.OutputSchema) {
			t.Fatalf("%s has invalid output schema: %s", schema.Name, string(schema.OutputSchema))
		}
	}
}

func TestRunChecksLicenseBeforeStartingSidecar(t *testing.T) {
	started := false

	err := run(
		context.Background(),
		func(sidecar.BrainHandler) error {
			started = true
			return nil
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-central" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, errors.New("license denied")
		},
	)
	if err == nil || err.Error() != "license: license denied" {
		t.Fatalf("unexpected error: %v", err)
	}
	if started {
		t.Fatal("sidecar should not start when license check fails")
	}
}

func TestRunStartsCentralBrainAfterLicenseCheck(t *testing.T) {
	wantErr := errors.New("boom")

	err := runWithFactory(
		context.Background(),
		func(handler sidecar.BrainHandler) error {
			if handler.Kind() != agent.KindCentral {
				t.Fatalf("handler kind=%s, want %s", handler.Kind(), agent.KindCentral)
			}
			return wantErr
		},
		func(name string, opts license.VerifyOptions) (*license.Result, error) {
			if name != "brain-central" {
				t.Fatalf("unexpected sidecar name: %s", name)
			}
			return nil, nil
		},
		func(context.Context) (*centralHandler, func() error, error) {
			return &centralHandler{svc: defaultCentralService()}, nil, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRuntimeHandlerFromEnvRestoresPersistedCentralState(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "brain-central.json")
	stores, err := persistence.Open("file", dsn)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer stores.Close()

	if err := stores.CentralStateStore.Save(ctx, &persistence.CentralState{
		Control: persistence.CentralControlState{
			TradingPaused:     true,
			PauseReason:       "ops halt",
			PausedInstruments: map[string]string{"BTC-USDT-SWAP": "vol spike"},
		},
		Review: persistence.CentralReviewState{
			LastRequest:  json.RawMessage(`{"trace_id":"trace-1","symbol":"BTC-USDT-SWAP","direction":"long","candidates":[{"account_id":"paper","allowed":true}]}`),
			LastDecision: json.RawMessage(`{"approved":true,"reason":"approved","size_factor":1}`),
			LastRunAtMS:  42,
		},
	}); err != nil {
		t.Fatalf("CentralStateStore.Save: %v", err)
	}

	t.Setenv("BRAIN_CENTRAL_PERSIST_DRIVER", "file")
	t.Setenv("BRAIN_CENTRAL_PERSIST_DSN", dsn)

	handler, closeFn, err := newRuntimeHandlerFromEnv(ctx)
	if err != nil {
		t.Fatalf("newRuntimeHandlerFromEnv: %v", err)
	}
	defer closeFn()

	resp, err := handler.HandleMethod(ctx, "brain/execute", json.RawMessage(`{
		"task_id":"health-1",
		"instruction":"health_check"
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(*sidecar.ExecuteResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Status != "completed" {
		t.Fatalf("unexpected execute result: %+v", result)
	}
	var payload central.Health
	if err := json.Unmarshal([]byte(result.Summary), &payload); err != nil {
		t.Fatalf("Unmarshal health summary: %v", err)
	}
	if !payload.State.Control.TradingPaused {
		t.Fatal("expected restored trading pause state")
	}
	if got := payload.State.Control.PausedInstruments["BTC-USDT-SWAP"]; got != "vol spike" {
		t.Fatalf("paused instrument=%q, want vol spike", got)
	}
	if payload.State.Review.LastRequest.TraceID != "trace-1" {
		t.Fatalf("trace id=%q, want trace-1", payload.State.Review.LastRequest.TraceID)
	}
}

func TestCentralHandleReviewTradeReturnsStructuredDecision(t *testing.T) {
	h := &centralHandler{}
	resp, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"central.review_trade",
		"arguments":{
			"trace_id":"trace-1",
			"symbol":"BTC-USDT-SWAP",
			"direction":"long",
			"candidates":[{"account_id":"paper","symbol":"BTC-USDT-SWAP","direction":"long","proposed_qty":1,"allowed":true}]
		}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(protocol.ToolCallResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Tool != quantcontracts.ToolCentralReviewTrade || result.IsError {
		t.Fatalf("unexpected review result: %+v", result)
	}
	var decision quantcontracts.ReviewDecision
	if err := result.DecodeOutput(&decision); err != nil {
		t.Fatalf("DecodeOutput failed: %v", err)
	}
	if !decision.Approved || decision.SizeFactor != 1.0 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestCentralHandleToolsCallEchoReturnsStructuredResult(t *testing.T) {
	h := &centralHandler{}
	resp, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"central.echo",
		"arguments":{"message":"hi"}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(protocol.ToolCallResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Tool != "central.echo" || result.IsError {
		t.Fatalf("unexpected result envelope: %+v", result)
	}
	var output map[string]string
	if err := result.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput failed: %v", err)
	}
	if output["message"] != "hi" {
		t.Fatalf("unexpected echo output: %+v", output)
	}
}

func TestCentralHandleDelegateReturnsStructuredOutput(t *testing.T) {
	h := &centralHandler{
		caller: mockKernelCaller{
			call: func(method string, params interface{}, result interface{}) error {
				if method != protocol.MethodSubtaskDelegate {
					t.Fatalf("unexpected method: %s", method)
				}
				raw := result.(*json.RawMessage)
				*raw = json.RawMessage(`{
					"status":"completed",
					"output":{"ok":true,"summary":"done"}
				}`)
				return nil
			},
		},
	}

	resp, err := h.HandleMethod(context.Background(), "tools/call", json.RawMessage(`{
		"name":"central.delegate",
		"arguments":{"target_kind":"code","instruction":"do it"}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(protocol.ToolCallResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Tool != "central.delegate" || result.IsError {
		t.Fatalf("unexpected delegate result: %+v", result)
	}
	var output map[string]interface{}
	if err := result.DecodeOutput(&output); err != nil {
		t.Fatalf("DecodeOutput failed: %v", err)
	}
	if output["summary"] != "done" {
		t.Fatalf("unexpected delegate output: %+v", output)
	}
}

func TestCentralHandleExecuteRejectsUnknownInstruction(t *testing.T) {
	h := &centralHandler{}
	resp, err := h.HandleMethod(context.Background(), "brain/execute", json.RawMessage(`{
		"task_id":"exec-1",
		"instruction":"unknown"
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(*sidecar.ExecuteResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Status != "failed" {
		t.Fatalf("unexpected execute result: %+v", result)
	}
}

func TestCentralHandleExecuteDailyReviewUsesCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := &centralHandler{
		svc: fakeCentralService{
			runReview: func(ctx context.Context, req reviewrun.Request) (reviewrun.Result, error) {
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("expected canceled context, got %v", ctx.Err())
				}
				return reviewrun.Result{}, ctx.Err()
			},
		},
	}

	resp, err := h.HandleMethod(ctx, "brain/execute", json.RawMessage(`{
		"task_id":"exec-review",
		"instruction":"daily_review_run",
		"context":{"trade":{"symbol":"BTC-USDT-SWAP","direction":"long","candidates":[{"account_id":"paper","allowed":true}]}}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod returned error: %v", err)
	}
	result, ok := resp.(*sidecar.ExecuteResult)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("unexpected execute result: %+v", result)
	}
}

func TestCentralHandleExecuteAppliesEmergencyActionAndConfigUpdate(t *testing.T) {
	h := &centralHandler{}

	emergencyResp, err := h.HandleMethod(context.Background(), "brain/execute", json.RawMessage(`{
		"task_id":"exec-emergency",
		"instruction":"emergency_action",
		"context":{"action":"pause_trading","reason":"ops halt"}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod emergency returned error: %v", err)
	}
	emergencyResult, ok := emergencyResp.(*sidecar.ExecuteResult)
	if !ok {
		t.Fatalf("unexpected emergency response type: %T", emergencyResp)
	}
	if emergencyResult.Status != "completed" {
		t.Fatalf("unexpected emergency result: %+v", emergencyResult)
	}
	var emergencyPayload struct {
		Status string         `json:"status"`
		Result control.Result `json:"result"`
		Health central.Health `json:"health"`
	}
	if err := json.Unmarshal([]byte(emergencyResult.Summary), &emergencyPayload); err != nil {
		t.Fatalf("Unmarshal emergency summary: %v", err)
	}
	if !emergencyPayload.Result.State.TradingPaused {
		t.Fatalf("expected trading pause after emergency action, got %+v", emergencyPayload.Result)
	}

	configResp, err := h.HandleMethod(context.Background(), "brain/execute", json.RawMessage(`{
		"task_id":"exec-config",
		"instruction":"update_config",
		"context":{"scope":"review","reason":"tighten policy","patch":{"mode":"strict"}}
	}`))
	if err != nil {
		t.Fatalf("HandleMethod update_config returned error: %v", err)
	}
	configResult, ok := configResp.(*sidecar.ExecuteResult)
	if !ok {
		t.Fatalf("unexpected config response type: %T", configResp)
	}
	if configResult.Status != "completed" {
		t.Fatalf("unexpected config result: %+v", configResult)
	}
	var configPayload struct {
		Status string         `json:"status"`
		Result control.Result `json:"result"`
	}
	if err := json.Unmarshal([]byte(configResult.Summary), &configPayload); err != nil {
		t.Fatalf("Unmarshal config summary: %v", err)
	}
	if configPayload.Result.State.LastConfigScope != "review" {
		t.Fatalf("last config scope=%q, want review", configPayload.Result.State.LastConfigScope)
	}
	if string(configPayload.Result.State.LastConfigPatch) != `{"mode":"strict"}` {
		t.Fatalf("last config patch=%s", string(configPayload.Result.State.LastConfigPatch))
	}
}
