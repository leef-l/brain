package tool

import (
	"encoding/json"
	"testing"
)

// TestAnomalyTemplateMatchBasic 覆盖"只按 Type 匹配"的最松场景。
func TestAnomalyTemplateMatchBasic(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "session_expired"},
		Recovery: []AnomalyTemplateRecoveryAction{
			{Kind: "human_intervention", Reason: "re-auth"},
		},
	})
	got := lib.Match("session_expired", "", "https://any.example.com", "")
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Recovery[0].Kind != "human_intervention" {
		t.Errorf("recovery kind = %s, want human_intervention", got.Recovery[0].Kind)
	}
}

// TestAnomalyTemplateMatchSpecificity 确认 Match 优先返回最具体的模板。
func TestAnomalyTemplateMatchSpecificity(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	// 粗模板:仅按 type
	lib.Upsert(&AnomalyTemplate{
		ID:        10,
		Signature: AnomalyTemplateSignature{Type: "rate_limited"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry", BackoffMS: 10000}},
	})
	// 细模板:type + subtype + site
	lib.Upsert(&AnomalyTemplate{
		ID: 20,
		Signature: AnomalyTemplateSignature{
			Type: "rate_limited", Subtype: "429_cooldown",
			SitePattern: `^https://shop\.example\.com`,
		},
		Recovery: []AnomalyTemplateRecoveryAction{{Kind: "retry", BackoffMS: 60000}},
	})
	got := lib.Match("rate_limited", "429_cooldown", "https://shop.example.com/cart", "")
	if got == nil {
		t.Fatal("expected match")
	}
	if got.ID != 20 {
		t.Errorf("picked ID=%d, want 20 (more specific)", got.ID)
	}
}

// TestAnomalyTemplateMatchSiteRegex 跨站匹配的正则走通路径。
func TestAnomalyTemplateMatchSiteRegex(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	lib.Upsert(&AnomalyTemplate{
		ID:        1,
		Signature: AnomalyTemplateSignature{Type: "captcha", SitePattern: `cloudflare|turnstile`},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry", BackoffMS: 5000}},
	})
	if m := lib.Match("captcha", "", "https://a.turnstile.example", ""); m == nil {
		t.Error("expected match on turnstile host")
	}
	if m := lib.Match("captcha", "", "https://unrelated.com", ""); m != nil {
		t.Errorf("unexpected match on non-regex host: %+v", m)
	}
}

// TestAnomalyTemplateMatchDisabled 确认停用模板不再命中。
func TestAnomalyTemplateMatchDisabled(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	tpl := lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "session_expired"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})
	tpl.Stats.Disabled = true
	if m := lib.Match("session_expired", "", "https://x.com", ""); m != nil {
		t.Error("disabled template should not match")
	}
}

// TestAnomalyTemplateRecordOutcomeAutoDisable 复现 M3 阈值:5 次失败 + 成功率<0.3 → 自动停用。
func TestAnomalyTemplateRecordOutcomeAutoDisable(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	tpl := lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "blank_page"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})
	// 4 次失败 + 0 次成功,样本总数 4,还没到阈值(<5),不触发停用
	for i := 0; i < 4; i++ {
		lib.RecordOutcome(tpl.ID, false)
	}
	if tpl.Stats.Disabled {
		t.Fatal("should not disable before threshold")
	}
	// 再失败 1 次 → FailureCount=5,SuccessRate=0 < 0.3 → 停用
	lib.RecordOutcome(tpl.ID, false)
	if !tpl.Stats.Disabled {
		t.Errorf("expected auto-disable at FailureCount=5 rate=0, got %+v", tpl.Stats)
	}
}

// TestAnomalyTemplateRecordOutcomeHighSuccessNoDisable 高成功率模板不停用。
func TestAnomalyTemplateRecordOutcomeHighSuccessNoDisable(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	tpl := lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "rate_limited"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})
	// 5 次成功 + 5 次失败 → SuccessRate=0.5 > 0.3,不停用
	for i := 0; i < 5; i++ {
		lib.RecordOutcome(tpl.ID, true)
	}
	for i := 0; i < 5; i++ {
		lib.RecordOutcome(tpl.ID, false)
	}
	if tpl.Stats.Disabled {
		t.Errorf("0.5 rate should not trigger disable, got %+v", tpl.Stats)
	}
}

// TestAnomalyTemplateEnable 手动重启用覆盖自动停用。
func TestAnomalyTemplateEnable(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	tpl := lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "x"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})
	tpl.Stats.Disabled = true
	lib.Enable(tpl.ID)
	if tpl.Stats.Disabled {
		t.Error("Enable should clear Disabled flag")
	}
}

// TestAnomalyTemplatePromoteCandidate 覆盖 LLM 修复成功后固化为模板的路径。
func TestAnomalyTemplatePromoteCandidate(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	sig := AnomalyTemplateSignature{Type: "rate_limited", Subtype: "429_cooldown"}
	recovery := []AnomalyTemplateRecoveryAction{{Kind: "retry", BackoffMS: 15000}}

	// 样本数不够 → 不固化
	if tpl := lib.PromoteCandidate(sig, recovery, AnomalyTemplateStats{SuccessCount: 1}, 3, 0.6); tpl != nil {
		t.Errorf("expected nil when samples<3, got %+v", tpl)
	}
	// 成功率不够 → 不固化
	if tpl := lib.PromoteCandidate(sig, recovery, AnomalyTemplateStats{SuccessCount: 2, FailureCount: 3}, 3, 0.6); tpl != nil {
		t.Errorf("expected nil when rate<0.6, got %+v", tpl)
	}
	// 条件满足 → 固化入库
	tpl := lib.PromoteCandidate(sig, recovery, AnomalyTemplateStats{SuccessCount: 4, FailureCount: 1}, 3, 0.6)
	if tpl == nil {
		t.Fatal("expected promotion to succeed")
	}
	if tpl.Source != "llm" {
		t.Errorf("Source = %s, want llm", tpl.Source)
	}
	if len(lib.List()) != 1 {
		t.Errorf("library size = %d, want 1", len(lib.List()))
	}
}

// TestAnomalyTemplateRecoveryJSONRoundtrip Recovery 的 JSON 编解码对称。
func TestAnomalyTemplateRecoveryJSONRoundtrip(t *testing.T) {
	orig := []AnomalyTemplateRecoveryAction{
		{Kind: "retry", MaxRetries: 3, BackoffMS: 500},
		{Kind: "fallback_pattern", FallbackID: "login_retry"},
		{Kind: "custom_steps", Steps: []AnomalyTemplateStep{
			{Tool: "browser.click", TargetRole: "close_dialog"},
			{Tool: "browser.wait", Params: map[string]interface{}{"ms": float64(500)}},
		}},
	}
	raw := EncodeRecoveryActions(orig)
	out, err := DecodeRecoveryActions(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(orig) {
		t.Fatalf("roundtrip lost entries: got %d, want %d", len(out), len(orig))
	}
	if out[0].MaxRetries != 3 || out[1].FallbackID != "login_retry" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
	if len(out[2].Steps) != 2 || out[2].Steps[0].Tool != "browser.click" {
		t.Errorf("custom_steps lost: %+v", out[2].Steps)
	}
	// 空输入安全
	if raw2 := EncodeRecoveryActions(nil); string(raw2) != "[]" {
		t.Errorf("nil encode should return [], got %s", raw2)
	}
	if out2, err := DecodeRecoveryActions(nil); err != nil || out2 != nil {
		t.Errorf("nil decode: got (%v, %v)", out2, err)
	}
	if out3, err := DecodeRecoveryActions(json.RawMessage("null")); err != nil || out3 != nil {
		t.Errorf("null decode: got (%v, %v)", out3, err)
	}
}

// TestAnomalyTemplateListOrder List 按 ID 升序返回,方便确定性断言。
func TestAnomalyTemplateListOrder(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	lib.Upsert(&AnomalyTemplate{ID: 3, Signature: AnomalyTemplateSignature{Type: "a"},
		Recovery: []AnomalyTemplateRecoveryAction{{Kind: "retry"}}})
	lib.Upsert(&AnomalyTemplate{ID: 1, Signature: AnomalyTemplateSignature{Type: "b"},
		Recovery: []AnomalyTemplateRecoveryAction{{Kind: "retry"}}})
	lib.Upsert(&AnomalyTemplate{ID: 2, Signature: AnomalyTemplateSignature{Type: "c"},
		Recovery: []AnomalyTemplateRecoveryAction{{Kind: "retry"}}})
	list := lib.List()
	if len(list) != 3 || list[0].ID != 1 || list[1].ID != 2 || list[2].ID != 3 {
		t.Errorf("List order wrong: %v", func() []int64 {
			ids := make([]int64, len(list))
			for i, t := range list {
				ids[i] = t.ID
			}
			return ids
		}())
	}
}

// TestAnomalyTemplateDelete 删除后再 Match 不应命中。
func TestAnomalyTemplateDelete(t *testing.T) {
	lib := NewAnomalyTemplateLibrary()
	tpl := lib.Upsert(&AnomalyTemplate{
		Signature: AnomalyTemplateSignature{Type: "blank_page"},
		Recovery:  []AnomalyTemplateRecoveryAction{{Kind: "retry"}},
	})
	lib.Delete(tpl.ID)
	if m := lib.Match("blank_page", "", "https://x.com", ""); m != nil {
		t.Errorf("expected no match after delete, got %+v", m)
	}
	if len(lib.List()) != 0 {
		t.Error("list should be empty after delete")
	}
}
