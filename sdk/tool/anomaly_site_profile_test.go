package tool

import (
	"testing"
)

func ptrBool(b bool) *bool { return &b }

// TestNormalizeOrigin 覆盖 URL / 纯 host / 大小写的 origin 规整。
func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://Shop.Example.com/cart?x=1", "https://shop.example.com"},
		{"HTTPS://a.example:8443/foo", "https://a.example:8443"},
		{"shop.example.com", "shop.example.com"},
		{"shop.example.com/path", "shop.example.com"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeOrigin(c.in); got != c.want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSiteHistoryRecord 基本记录 + snapshotProfiles 聚合。
func TestSiteHistoryRecord(t *testing.T) {
	h := newSiteHistory()
	h.recordSiteAnomaly("https://shop.example.com/a", "rate_limited", "429_cooldown", 1000, ptrBool(true))
	h.recordSiteAnomaly("https://shop.example.com/b", "rate_limited", "429_cooldown", 2000, ptrBool(false))
	h.recordSiteAnomaly("https://other.example", "session_expired", "", 0, nil)

	snap := h.snapshotProfiles()
	if len(snap) != 2 {
		t.Fatalf("snapshot size = %d, want 2(两个 host 合并 rate_limited 到同 key)", len(snap))
	}

	// shop.example.com 的聚合条目应该是 Frequency=2,平均 1500,RecoverSuccess=1
	var shop *HostAnomalyEntry
	for i := range snap {
		if snap[i].SiteOrigin == "https://shop.example.com" {
			shop = &snap[i]
			break
		}
	}
	if shop == nil {
		t.Fatal("shop bucket missing")
	}
	if shop.Frequency != 2 {
		t.Errorf("freq = %d, want 2", shop.Frequency)
	}
	if shop.AvgDurationMs() != 1500 {
		t.Errorf("avg = %d, want 1500", shop.AvgDurationMs())
	}
	if shop.RecoverAttempts != 2 || shop.RecoverSuccesses != 1 {
		t.Errorf("recovery = %d/%d, want 2/1 attempts/success", shop.RecoverAttempts, shop.RecoverSuccesses)
	}
}

// TestSiteHistoryRecoverRate 成功率冷启动阈值。
func TestSiteHistoryRecoverRate(t *testing.T) {
	e := &HostAnomalyEntry{}
	// attempts=0 → -1
	if r := e.RecoverSuccessRate(); r != -1 {
		t.Errorf("empty rate = %v, want -1", r)
	}
	e.RecoverAttempts = 2
	if r := e.RecoverSuccessRate(); r != -1 {
		t.Errorf("attempts<3 rate = %v, want -1(冷启动)", r)
	}
	e.RecoverAttempts = 4
	e.RecoverSuccesses = 3
	if r := e.RecoverSuccessRate(); r != 0.75 {
		t.Errorf("rate = %v, want 0.75", r)
	}
}

// TestSiteHistoryListForSite 单 host 过滤 + 按 Frequency 排序。
func TestSiteHistoryListForSite(t *testing.T) {
	h := newSiteHistory()
	for i := 0; i < 3; i++ {
		h.recordSiteAnomaly("https://x.example", "captcha", "", 0, nil)
	}
	h.recordSiteAnomaly("https://x.example", "rate_limited", "429_cooldown", 0, nil)

	list := h.listForSite("x.example")
	if len(list) != 2 {
		t.Fatalf("list size = %d, want 2", len(list))
	}
	// captcha 出现 3 次,排最前
	if list[0].AnomalyType != "captcha" {
		t.Errorf("want captcha first, got %s", list[0].AnomalyType)
	}

	// URL 输入也能命中
	if list2 := h.listForSite("https://x.example/foo/bar"); len(list2) != 2 {
		t.Errorf("URL input should normalize: got %d entries", len(list2))
	}

	// 不存在的站返回空
	if list3 := h.listForSite("ghost.example"); list3 != nil {
		t.Errorf("missing site should return nil, got %+v", list3)
	}
}

// TestSiteHistoryTopFailingHosts topN 降序 + 稳定排序。
func TestSiteHistoryTopFailingHosts(t *testing.T) {
	h := newSiteHistory()
	// a: 3, b: 1, c: 2
	for i := 0; i < 3; i++ {
		h.recordSiteAnomaly("https://a.example", "captcha", "", 0, nil)
	}
	h.recordSiteAnomaly("https://b.example", "captcha", "", 0, nil)
	for i := 0; i < 2; i++ {
		h.recordSiteAnomaly("https://c.example", "captcha", "", 0, nil)
	}

	top := h.topFailingHosts(2)
	if len(top) != 2 {
		t.Fatalf("top size = %d, want 2", len(top))
	}
	if top[0] != "https://a.example" {
		t.Errorf("top[0] = %s, want a.example", top[0])
	}
	if top[1] != "https://c.example" {
		t.Errorf("top[1] = %s, want c.example", top[1])
	}

	// topN<=0 返回空
	if h.topFailingHosts(0) != nil || h.topFailingHosts(-1) != nil {
		t.Error("topN<=0 should return nil")
	}
	// topN > 实际 host 数,不 panic
	if got := h.topFailingHosts(100); len(got) != 3 {
		t.Errorf("topN=100 over 3 hosts, got %d", len(got))
	}
}

// TestSiteHistoryIgnoresEmptyInputs 空输入安全。
func TestSiteHistoryIgnoresEmptyInputs(t *testing.T) {
	h := newSiteHistory()
	h.recordSiteAnomaly("", "captcha", "", 0, nil)
	h.recordSiteAnomaly("https://x", "", "", 0, nil)
	if snap := h.snapshotProfiles(); len(snap) != 0 {
		t.Errorf("empty inputs leaked into snapshot: %+v", snap)
	}
}
