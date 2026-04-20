package tool

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

type fakeCoord struct {
	mu    sync.Mutex
	calls []HumanTakeoverRequest
	reply HumanTakeoverResponse
	delay time.Duration
}

func (f *fakeCoord) RequestTakeover(_ context.Context, req HumanTakeoverRequest) HumanTakeoverResponse {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.reply
}

func TestHumanRequestTakeoverResumed(t *testing.T) {
	coord := &fakeCoord{reply: HumanTakeoverResponse{Outcome: HumanOutcomeResumed, Note: "done"}}
	SetHumanTakeoverCoordinator(coord)
	t.Cleanup(func() { SetHumanTakeoverCoordinator(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-123", "browser", "log in")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	tool := NewHumanRequestTakeoverTool()
	res, err := tool.Execute(ctx, json.RawMessage(`{"reason":"captcha"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", string(res.Output))
	}
	var out struct {
		Outcome string `json:"outcome"`
		Note    string `json:"note"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Outcome != "resumed" || out.Note != "done" {
		t.Errorf("bad outcome: %+v", out)
	}

	if len(coord.calls) != 1 {
		t.Fatalf("expected 1 coord call")
	}
	if coord.calls[0].RunID != "run-123" || coord.calls[0].BrainKind != "browser" {
		t.Errorf("bad request context: %+v", coord.calls[0])
	}
}

func TestHumanRequestTakeoverNoCoordinator(t *testing.T) {
	SetHumanTakeoverCoordinator(nil)
	tool := NewHumanRequestTakeoverTool()
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"reason":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Outcome != "no_coordinator" {
		t.Errorf("outcome = %q, want no_coordinator", out.Outcome)
	}
}

func TestHumanRequestTakeoverRecordsMarker(t *testing.T) {
	SetHumanTakeoverCoordinator(&fakeCoord{reply: HumanTakeoverResponse{Outcome: HumanOutcomeAborted}})
	t.Cleanup(func() { SetHumanTakeoverCoordinator(nil) })

	ctx := context.Background()
	BindRecorder(ctx, "run-mark", "browser", "test")
	defer func() { _ = FinishRecorder(ctx, "failure") }()

	tool := NewHumanRequestTakeoverTool()
	_, _ = tool.Execute(ctx, json.RawMessage(`{"reason":"captcha"}`))

	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		t.Fatal("recorder gone")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.actions) < 2 {
		t.Fatalf("expected at least 2 marker actions, got %d", len(rec.actions))
	}
	first := rec.actions[0]
	if first.Tool != "human.takeover" || first.Result != "start" {
		t.Errorf("first action mismatch: %+v", first)
	}
	last := rec.actions[len(rec.actions)-1]
	if last.Tool != "human.takeover" || last.Result != "aborted" {
		t.Errorf("last action mismatch: %+v", last)
	}
}

func TestHumanRequestTakeoverRejectsMissingReason(t *testing.T) {
	SetHumanTakeoverCoordinator(&fakeCoord{reply: HumanTakeoverResponse{Outcome: HumanOutcomeResumed}})
	t.Cleanup(func() { SetHumanTakeoverCoordinator(nil) })
	tool := NewHumanRequestTakeoverTool()
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError {
		t.Fatal("missing reason must error")
	}
}

type fakeDemoSink struct {
	mu   sync.Mutex
	seqs []*persistence.HumanDemoSequence
}

func (f *fakeDemoSink) SaveHumanDemoSequence(_ context.Context, seq *persistence.HumanDemoSequence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seqs = append(f.seqs, seq)
	return nil
}

type delayedCoord struct {
	onEntered func()
	reply     HumanTakeoverResponse
}

func (d *delayedCoord) RequestTakeover(ctx context.Context, _ HumanTakeoverRequest) HumanTakeoverResponse {
	if d.onEntered != nil {
		d.onEntered()
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Millisecond):
	}
	return d.reply
}

func TestHumanTakeoverRecordsDOMEvents(t *testing.T) {
	src := cdp.NewMemoryEventSource(32)
	sink := &fakeDemoSink{}

	SetHumanEventSourceFactory(func(_ context.Context) (cdp.EventSource, error) { return src, nil })
	SetHumanDemoSink(sink)
	t.Cleanup(func() {
		SetHumanEventSourceFactory(nil)
		SetHumanDemoSink(nil)
		SetHumanTakeoverCoordinator(nil)
	})

	coord := &delayedCoord{
		onEntered: func() {
			base := time.Now().UTC()
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventClick, BrainID: 7, Tag: "button", Name: "Login", URL: "https://x.com/login", Timestamp: base})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventInput, BrainID: 9, Tag: "input", Type: "email", Value: "a", Timestamp: base.Add(10 * time.Millisecond)})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventInput, BrainID: 9, Tag: "input", Type: "email", Value: "ab", Timestamp: base.Add(80 * time.Millisecond)})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventInput, BrainID: 9, Tag: "input", Type: "email", Value: "abc@x.com", Timestamp: base.Add(200 * time.Millisecond)})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventSubmit, BrainID: 11, Tag: "form", Timestamp: base.Add(1 * time.Second)})
		},
		reply: HumanTakeoverResponse{Outcome: HumanOutcomeResumed, Note: "human filled it"},
	}
	SetHumanTakeoverCoordinator(coord)

	ctx := context.Background()
	BindRecorder(ctx, "run-dom", "browser", "log in with real human")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	tool := NewHumanRequestTakeoverTool()
	res, err := tool.Execute(ctx, json.RawMessage(`{"reason":"captcha","url":"https://x.com/login"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", string(res.Output))
	}

	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		t.Fatal("recorder gone")
	}
	rec.mu.Lock()
	actions := append([]RecordedAction(nil), rec.actions...)
	rec.mu.Unlock()

	if len(actions) < 5 {
		t.Fatalf("expected ≥5 actions in recorder, got %d: %+v", len(actions), actions)
	}
	if actions[0].Tool != "human.takeover" || actions[0].Result != "start" {
		t.Errorf("first action should be takeover start, got %+v", actions[0])
	}
	last := actions[len(actions)-1]
	if last.Tool != "human.takeover" || last.Result != "resumed" {
		t.Errorf("last action should be takeover resumed marker, got %+v", last)
	}

	dom := actions[1 : len(actions)-1]
	var humanCount, clickCount, typeCount, submitCount int
	for _, a := range dom {
		if v, _ := a.Params["_human"].(bool); v {
			humanCount++
		}
		switch a.Tool {
		case "browser.click":
			if a.Params["submit"] == true {
				submitCount++
			} else {
				clickCount++
			}
		case "browser.type":
			typeCount++
		}
	}
	if humanCount != len(dom) {
		t.Errorf("all DOM actions must carry _human=true, got %d/%d", humanCount, len(dom))
	}
	if clickCount != 1 {
		t.Errorf("want 1 click, got %d", clickCount)
	}
	if submitCount != 1 {
		t.Errorf("want 1 submit, got %d", submitCount)
	}
	if typeCount != 1 {
		t.Errorf("input merge failed: want 1 Type action, got %d", typeCount)
	}
	for _, a := range dom {
		if a.Tool == "browser.type" {
			if got, _ := a.Params["text"].(string); got != "abc@x.com" {
				t.Errorf("merged Type.text = %q, want %q", got, "abc@x.com")
			}
		}
	}

	sink.mu.Lock()
	seqs := sink.seqs
	sink.mu.Unlock()
	if len(seqs) != 1 {
		t.Fatalf("expected 1 demo sequence, got %d", len(seqs))
	}
	seq := seqs[0]
	if seq.RunID != "run-dom" || seq.BrainKind != "browser" || seq.Approved {
		t.Errorf("bad seq metadata: %+v", seq)
	}
	if seq.Site != "https://x.com" {
		t.Errorf("site = %q, want https://x.com", seq.Site)
	}
	var demoActs []RecordedAction
	if err := json.Unmarshal(seq.Actions, &demoActs); err != nil {
		t.Fatalf("unmarshal demo actions: %v", err)
	}
	if len(demoActs) != 3 {
		t.Errorf("demo actions count = %d, want 3 (click+type+submit)", len(demoActs))
	}
}

func TestHumanTakeoverNoFactoryIsBackwardsCompat(t *testing.T) {
	sink := &fakeDemoSink{}
	SetHumanEventSourceFactory(nil)
	SetHumanDemoSink(sink)
	SetHumanTakeoverCoordinator(&fakeCoord{reply: HumanTakeoverResponse{Outcome: HumanOutcomeAborted}})
	t.Cleanup(func() {
		SetHumanDemoSink(nil)
		SetHumanTakeoverCoordinator(nil)
	})

	ctx := context.Background()
	BindRecorder(ctx, "run-compat", "browser", "t")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "failure") })

	tool := NewHumanRequestTakeoverTool()
	_, _ = tool.Execute(ctx, json.RawMessage(`{"reason":"captcha"}`))

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.seqs) != 0 {
		t.Errorf("no factory → no demo sequence persisted, got %d", len(sink.seqs))
	}
}

func TestHumanTakeoverEmptyDemoNotPersisted(t *testing.T) {
	src := cdp.NewMemoryEventSource(8)
	sink := &fakeDemoSink{}
	SetHumanEventSourceFactory(func(_ context.Context) (cdp.EventSource, error) { return src, nil })
	SetHumanDemoSink(sink)
	SetHumanTakeoverCoordinator(&fakeCoord{reply: HumanTakeoverResponse{Outcome: HumanOutcomeResumed}})
	t.Cleanup(func() {
		SetHumanEventSourceFactory(nil)
		SetHumanDemoSink(nil)
		SetHumanTakeoverCoordinator(nil)
	})

	ctx := context.Background()
	BindRecorder(ctx, "run-empty", "browser", "t")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	_, _ = NewHumanRequestTakeoverTool().Execute(ctx, json.RawMessage(`{"reason":"x"}`))

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.seqs) != 0 {
		t.Errorf("empty demo should not be persisted, got %d", len(sink.seqs))
	}
}

func TestHumanTakeoverNonInputResetsMergeWindow(t *testing.T) {
	src := cdp.NewMemoryEventSource(16)
	sink := &fakeDemoSink{}
	SetHumanEventSourceFactory(func(_ context.Context) (cdp.EventSource, error) { return src, nil })
	SetHumanDemoSink(sink)
	t.Cleanup(func() {
		SetHumanEventSourceFactory(nil)
		SetHumanDemoSink(nil)
		SetHumanTakeoverCoordinator(nil)
	})
	coord := &delayedCoord{
		onEntered: func() {
			base := time.Now().UTC()
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventInput, BrainID: 1, Value: "a", Timestamp: base})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventClick, BrainID: 2, Tag: "a", Name: "Help", Timestamp: base.Add(50 * time.Millisecond)})
			src.Push(cdp.HumanEvent{Kind: cdp.HumanEventInput, BrainID: 1, Value: "ab", Timestamp: base.Add(100 * time.Millisecond)})
		},
		reply: HumanTakeoverResponse{Outcome: HumanOutcomeResumed},
	}
	SetHumanTakeoverCoordinator(coord)

	ctx := context.Background()
	BindRecorder(ctx, "run-reset", "browser", "t")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	_, _ = NewHumanRequestTakeoverTool().Execute(ctx, json.RawMessage(`{"reason":"x"}`))

	sink.mu.Lock()
	seqs := sink.seqs
	sink.mu.Unlock()
	if len(seqs) != 1 {
		t.Fatalf("expected 1 demo seq, got %d", len(seqs))
	}
	var acts []RecordedAction
	_ = json.Unmarshal(seqs[0].Actions, &acts)
	typeN := 0
	for _, a := range acts {
		if a.Tool == "browser.type" {
			typeN++
		}
	}
	if typeN != 2 {
		t.Errorf("want 2 Type actions (window reset by click), got %d: %+v", typeN, acts)
	}
}

func TestConvertDemoToPattern_ParameterizesAuthFlow(t *testing.T) {
	seq := &persistence.HumanDemoSequence{
		RunID:      "run-auth",
		Goal:       "登录管理后台",
		URL:        "https://pwv2.easytestdev.online/admin/#/auth/login",
		RecordedAt: time.Now().UTC(),
	}
	actions := []RecordedAction{
		{
			Tool:        "browser.type",
			ElementRole: "textbox",
			ElementName: "账号",
			Params: map[string]interface{}{
				"text": "admin",
				"id":   1,
			},
		},
		{
			Tool:        "browser.type",
			ElementRole: "textbox",
			ElementType: "password",
			ElementName: "密码",
			Params: map[string]interface{}{
				"text": "123456789ASD",
				"id":   2,
			},
		},
		{
			Tool:        "browser.drag",
			ElementRole: "button",
			ElementName: "请按住滑块拖动",
			Params: map[string]interface{}{
				"from_x": 10.0,
				"from_y": 20.0,
				"to_x":   200.0,
				"to_y":   20.0,
			},
		},
		{
			Tool:        "browser.click",
			ElementRole: "button",
			ElementName: "登录",
			Params: map[string]interface{}{
				"id": 3,
			},
		},
	}

	got := ConvertDemoToPattern(seq, actions)
	if got == nil {
		t.Fatal("ConvertDemoToPattern() = nil")
	}
	if got.Category != "auth" {
		t.Fatalf("category = %q, want auth", got.Category)
	}
	if got.ElementRoles["username_field"].Name != "账号" {
		t.Fatalf("username descriptor = %+v", got.ElementRoles["username_field"])
	}
	if got.ElementRoles["password_field"].Type != "password" {
		t.Fatalf("password descriptor = %+v", got.ElementRoles["password_field"])
	}
	if got.ActionSequence[0].Params["text"] != "$credentials.username" {
		t.Fatalf("first text = %v, want placeholder", got.ActionSequence[0].Params["text"])
	}
	if got.ActionSequence[1].Params["text"] != "$credentials.password" {
		t.Fatalf("second text = %v, want placeholder", got.ActionSequence[1].Params["text"])
	}
	if got.ActionSequence[2].Tool != "browser.drag" || got.ActionSequence[2].TargetRole != "slider_handle" {
		t.Fatalf("drag step = %+v, want slider_handle drag", got.ActionSequence[2])
	}
	if got.ActionSequence[3].TargetRole != "submit_button" {
		t.Fatalf("submit step = %+v, want submit_button", got.ActionSequence[3])
	}
}
