package tool

import "testing"

// fakeTemplateSource 用于测试 ResolveAnomalyHandler 在 library hit / miss 两条分支。
type fakeTemplateSource struct {
	tpl *AnomalyTemplate
}

func (f *fakeTemplateSource) Match(_, _, _, _ string) *AnomalyTemplate {
	return f.tpl
}

// TestResolveAnomalyHandlerTemplateRetry 模板命中 → 翻译成 retry handler,
// 忽略 pattern 的 OnAnomaly(即使 pattern 有 abort 也走模板)。
func TestResolveAnomalyHandlerTemplateRetry(t *testing.T) {
	src := &fakeTemplateSource{tpl: &AnomalyTemplate{
		ID: 42,
		Recovery: []AnomalyTemplateRecoveryAction{
			{Kind: "retry", MaxRetries: 3, BackoffMS: 500, Reason: "transient"},
		},
	}}
	p := &UIPattern{OnAnomaly: map[string]AnomalyHandler{
		"session_expired": {Action: "abort", Reason: "pattern says give up"},
	}}
	h, ok, id := ResolveAnomalyHandler(src, p, "session_expired", "", "https://x.com", "")
	if !ok || h == nil {
		t.Fatal("expected handler")
	}
	if id != 42 {
		t.Errorf("template id = %d, want 42", id)
	}
	if h.Action != "retry" || h.MaxRetries != 3 || h.BackoffMS != 500 {
		t.Errorf("handler = %+v, want retry(3, 500ms)", h)
	}
}

// TestResolveAnomalyHandlerTemplateFallbackPattern 翻译 fallback_pattern。
func TestResolveAnomalyHandlerTemplateFallbackPattern(t *testing.T) {
	src := &fakeTemplateSource{tpl: &AnomalyTemplate{
		ID: 7,
		Recovery: []AnomalyTemplateRecoveryAction{
			{Kind: "fallback_pattern", FallbackID: "login_with_otp"},
		},
	}}
	h, ok, id := ResolveAnomalyHandler(src, nil, "captcha", "hcaptcha", "https://bot-guard.example", "high")
	if !ok || h == nil {
		t.Fatal("expected fallback handler")
	}
	if id != 7 || h.Action != "fallback_pattern" || h.FallbackID != "login_with_otp" {
		t.Errorf("handler = %+v", h)
	}
}

// TestResolveAnomalyHandlerFallbackPatternWithoutID 没 FallbackID 的 fallback_pattern
// 会被视为无效 → 回退 pattern OnAnomaly。
func TestResolveAnomalyHandlerFallbackPatternWithoutID(t *testing.T) {
	src := &fakeTemplateSource{tpl: &AnomalyTemplate{
		ID: 8,
		Recovery: []AnomalyTemplateRecoveryAction{
			{Kind: "fallback_pattern" /* FallbackID 空 */},
		},
	}}
	p := &UIPattern{OnAnomaly: map[string]AnomalyHandler{
		"session_expired": {Action: "abort"},
	}}
	h, ok, id := ResolveAnomalyHandler(src, p, "session_expired", "", "", "")
	if !ok || h == nil || h.Action != "abort" {
		t.Errorf("expected fallback to pattern abort, got ok=%v h=%+v", ok, h)
	}
	if id != 0 {
		t.Errorf("id = %d, want 0(未走模板)", id)
	}
}

// TestResolveAnomalyHandlerCustomStepsFalls 模板是 custom_steps → 本轮不翻译 → 回退 pattern。
func TestResolveAnomalyHandlerCustomStepsFalls(t *testing.T) {
	src := &fakeTemplateSource{tpl: &AnomalyTemplate{
		ID: 9,
		Recovery: []AnomalyTemplateRecoveryAction{
			{Kind: "custom_steps", Steps: []AnomalyTemplateStep{{Tool: "browser.click"}}},
		},
	}}
	p := &UIPattern{OnAnomaly: map[string]AnomalyHandler{
		"rate_limited": {Action: "retry", MaxRetries: 1},
	}}
	h, ok, id := ResolveAnomalyHandler(src, p, "rate_limited", "", "", "")
	if !ok || h == nil || h.Action != "retry" {
		t.Errorf("custom_steps should fall back to pattern retry, got ok=%v h=%+v", ok, h)
	}
	if id != 0 {
		t.Errorf("custom_steps must not report id, got %d", id)
	}
}

// TestResolveAnomalyHandlerMissThenPattern 模板库没命中 → pattern OnAnomaly 回退。
func TestResolveAnomalyHandlerMissThenPattern(t *testing.T) {
	src := &fakeTemplateSource{tpl: nil}
	p := &UIPattern{OnAnomaly: map[string]AnomalyHandler{
		"session_expired": {Action: "human_intervention", Reason: "need re-auth"},
	}}
	h, ok, _ := ResolveAnomalyHandler(src, p, "session_expired", "", "", "")
	if !ok || h == nil || h.Action != "human_intervention" {
		t.Errorf("pattern fallback failed: ok=%v h=%+v", ok, h)
	}
}

// TestResolveAnomalyHandlerNoLibraryNoPattern 两边都空 → 返回 (nil, false, 0)。
func TestResolveAnomalyHandlerNoLibraryNoPattern(t *testing.T) {
	h, ok, id := ResolveAnomalyHandler(nil, nil, "x", "", "", "")
	if ok || h != nil || id != 0 {
		t.Errorf("empty everything: ok=%v h=%+v id=%d", ok, h, id)
	}
}

// TestMarkTemplateHit 生成正确的事件标记。
func TestMarkTemplateHit(t *testing.T) {
	if m := MarkTemplateHit(0); m != nil {
		t.Errorf("id<=0 should return nil, got %+v", m)
	}
	m := MarkTemplateHit(77)
	if m == nil || m.TemplateID != 77 || m.HitAt.IsZero() {
		t.Errorf("mark = %+v", m)
	}
}
