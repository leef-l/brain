package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubLLM 按固定脚本返回;用于驱动 Execute 不真去调 Anthropic。
type stubLLM struct {
	reply string
	err   error
}

func (s *stubLLM) Complete(_ context.Context, _ string, _ string) (string, error) {
	return s.reply, s.err
}

func TestParseLLMRecoveryBasic(t *testing.T) {
	raw := `{
  "recovery": [
    {"kind":"retry","max_retries":2,"backoff_ms":1000,"reason":"transient"},
    {"kind":"custom_steps","steps":[{"tool":"browser.click","target_role":"close"}]}
  ],
  "rationale": "short retry then dismiss overlay"
}`
	rec, rationale, err := parseLLMRecovery(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rec) != 2 || rec[0].Kind != "retry" || rec[1].Kind != "custom_steps" {
		t.Errorf("unexpected recovery: %+v", rec)
	}
	if !strings.Contains(rationale, "retry then dismiss") {
		t.Errorf("rationale = %q", rationale)
	}
}

func TestParseLLMRecoveryCodeFenceStripped(t *testing.T) {
	raw := "```json\n" + `{"recovery":[{"kind":"retry"}],"rationale":"x"}` + "\n```"
	rec, _, err := parseLLMRecovery(raw)
	if err != nil {
		t.Fatalf("parse with fence: %v", err)
	}
	if len(rec) != 1 {
		t.Errorf("want 1 action, got %d", len(rec))
	}
}

func TestParseLLMRecoveryFiltersInvalidKind(t *testing.T) {
	raw := `{"recovery":[{"kind":"nonsense"},{"kind":"retry"}],"rationale":"ok"}`
	rec, _, err := parseLLMRecovery(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rec) != 1 || rec[0].Kind != "retry" {
		t.Errorf("nonsense kind should be filtered, got %+v", rec)
	}
}

func TestParseLLMRecoveryEmpty(t *testing.T) {
	if _, _, err := parseLLMRecovery(""); err == nil {
		t.Error("empty should error")
	}
	if _, _, err := parseLLMRecovery(`not-json`); err == nil {
		t.Error("invalid json should error")
	}
	if _, _, err := parseLLMRecovery(`{"recovery":[]}`); err == nil {
		t.Error("empty recovery should error")
	}
}

func TestFallbackRecoveryAuthClass(t *testing.T) {
	sig := AnomalyTemplateSignature{Type: "captcha"}
	rec := fallbackRecovery(sig)
	if len(rec) != 1 || rec[0].Kind != "human_intervention" {
		t.Errorf("captcha should escalate human, got %+v", rec)
	}
	sig2 := AnomalyTemplateSignature{Type: "rate_limited"}
	rec2 := fallbackRecovery(sig2)
	if len(rec2) < 2 || rec2[0].Kind != "retry" {
		t.Errorf("transient should retry first, got %+v", rec2)
	}
}

func TestAnomalyFixStorePromote(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	store := newAnomalyFixStore()
	sig := AnomalyTemplateSignature{Type: "blank_page"}
	rec := []AnomalyTemplateRecoveryAction{{Kind: "retry", BackoffMS: 500}}
	store.upsertCandidate(sig, rec)

	// 3 次成功 + 1 次失败 → rate=0.75 ≥ 0.6,samples=4 ≥ 3 → 固化
	for i := 0; i < 3; i++ {
		store.recordOutcome(sig, true)
	}
	store.recordOutcome(sig, false)
	tpl := store.promoteIfReady(lib, sig)
	if tpl == nil {
		t.Fatal("expected promotion")
	}
	if tpl.Source != "llm" {
		t.Errorf("source = %s, want llm", tpl.Source)
	}
	// 晋升后 candidate 应被移除
	if len(store.listCandidates()) != 0 {
		t.Error("candidate should be removed after promotion")
	}
	// 库里也应该有了
	if len(lib.List()) != 1 {
		t.Errorf("library size = %d, want 1", len(lib.List()))
	}
}

func TestAnomalyFixStoreNoPromoteLowSuccess(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	store := newAnomalyFixStore()
	sig := AnomalyTemplateSignature{Type: "rate_limited"}
	store.upsertCandidate(sig, []AnomalyTemplateRecoveryAction{{Kind: "retry"}})
	// 1 成功 5 失败 → rate=1/6 < 0.6 → 不固化
	store.recordOutcome(sig, true)
	for i := 0; i < 5; i++ {
		store.recordOutcome(sig, false)
	}
	if tpl := store.promoteIfReady(lib, sig); tpl != nil {
		t.Errorf("low success should not promote, got %+v", tpl)
	}
}

func TestRequestAnomalyFixToolFallbackWhenNoLLM(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	// holder=nil 也 OK,Execute 里对 nil 有兜底 —— 真实环境 holder 非 nil
	tool := NewBrowserRequestAnomalyFixTool(nil, nil /*llm*/, lib)

	args, _ := json.Marshal(map[string]interface{}{
		"anomaly": map[string]interface{}{
			"type":    "rate_limited",
			"subtype": "429_cooldown",
		},
		"site_origin": "https://shop.example.com",
	})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", string(res.Output))
	}
	var out struct {
		Source   string                          `json:"source"`
		Recovery []AnomalyTemplateRecoveryAction `json:"recovery"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Source != "fallback" {
		t.Errorf("source = %s, want fallback(no llm)", out.Source)
	}
	if len(out.Recovery) == 0 {
		t.Error("fallback should produce non-empty recovery")
	}
	// candidate 应当被暂存
	if len(tool.ListFixCandidates()) != 1 {
		t.Errorf("candidate not stored: %+v", tool.ListFixCandidates())
	}
}

func TestRequestAnomalyFixToolLLMPath(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	backend := &stubLLM{reply: `{"recovery":[{"kind":"retry","max_retries":2}],"rationale":"try again"}`}
	tool := NewBrowserRequestAnomalyFixTool(nil, backend, lib)

	args, _ := json.Marshal(map[string]interface{}{
		"anomaly":     map[string]interface{}{"type": "blank_page"},
		"site_origin": "https://flaky.example",
	})
	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Source    string `json:"source"`
		Rationale string `json:"rationale"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Source != "llm" {
		t.Errorf("source = %s, want llm", out.Source)
	}
	if !strings.Contains(out.Rationale, "try again") {
		t.Errorf("rationale dropped: %q", out.Rationale)
	}
}

func TestRequestAnomalyFixToolLLMErrorFallback(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	backend := &stubLLM{err: errFake{}}
	tool := NewBrowserRequestAnomalyFixTool(nil, backend, lib)

	args, _ := json.Marshal(map[string]interface{}{
		"anomaly": map[string]interface{}{"type": "rate_limited"},
	})
	res, _ := tool.Execute(context.Background(), args)
	var out struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Source != "fallback" {
		t.Errorf("llm error should fallback, got %s", out.Source)
	}
}

func TestRequestAnomalyFixToolRecordOutcome(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	backend := &stubLLM{reply: `{"recovery":[{"kind":"retry"}],"rationale":"r"}`}
	tool := NewBrowserRequestAnomalyFixTool(nil, backend, lib)

	// 触发一次以便 store 里有 candidate
	args, _ := json.Marshal(map[string]interface{}{
		"anomaly":     map[string]interface{}{"type": "blank_page", "subtype": "iframe_load_fail"},
		"site_origin": "https://flaky.example",
	})
	_, _ = tool.Execute(context.Background(), args)

	sig := AnomalyTemplateSignature{
		Type: "blank_page", Subtype: "iframe_load_fail", SitePattern: "https://flaky.example",
	}
	// 3 成功 → 晋升
	for i := 0; i < 3; i++ {
		tool.RecordFixOutcome(sig, true)
	}
	if len(lib.List()) != 1 {
		t.Errorf("expected 1 promoted template, got %d", len(lib.List()))
	}
}

// TestRequestAnomalyFixToolMissingAnomaly 空 anomaly 参数应报错。
func TestRequestAnomalyFixToolMissingAnomaly(t *testing.T) {
	tool := NewBrowserRequestAnomalyFixTool(nil, nil, NewAnomalyTemplateLibrary())
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError {
		t.Errorf("missing anomaly should error, got %s", string(res.Output))
	}
}

// TestTailRecorderActionsWithBind 绑定 recorder 后能取到尾部动作。
func TestTailRecorderActionsWithBind(t *testing.T) {
	// 注意:BindRecorder 的 key 是 ctx 值(map[context.Context]),所以必须给
	// 一个独立 ctx —— 不能用 context.Background(),否则会被其它测试复用到同一 key。
	type mykey struct{}
	ctx := context.WithValue(context.Background(), mykey{}, "run-fix")
	BindRecorder(ctx, "run-fix", "browser", "flaky nav")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	// 手动往 recorder 灌几条动作
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		t.Fatal("recorder not bound")
	}
	for i := 0; i < 5; i++ {
		rec.append(RecordedAction{Tool: "browser.click"})
	}
	out := tailRecorderActions(ctx, 3)
	if len(out) != 3 {
		t.Fatalf("want 3 actions, got %d", len(out))
	}
	// 不同 ctx → 没有绑定,返回 nil
	type unboundkey struct{}
	unboundCtx := context.WithValue(context.Background(), unboundkey{}, "none")
	if x := tailRecorderActions(unboundCtx, 3); x != nil {
		t.Errorf("unbound ctx should return nil, got %+v", x)
	}
	// n<=0 安全
	if x := tailRecorderActions(ctx, 0); x != nil {
		t.Errorf("n=0 should return nil, got %+v", x)
	}
}

// errFake 是一个用完即扔的 error,避免触发 golangci-lint 静态字符串规则。
type errFake struct{}

func (errFake) Error() string { return "simulated llm failure" }
