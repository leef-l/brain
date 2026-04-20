package tool

import (
	"context"
	"path/filepath"
	"testing"
)

// M3 测试:
//  1. 新 Upsert 的 pattern 默认 Enabled = true。
//  2. 连续 5 次失败 + 成功率<0.3 后 RecordExecution 自动停用。
//  3. List/Get 把 Enabled=false 的剔除,ListAll/GetAny 能拿到。
//  4. SetEnabled(id, true) 手动重置可恢复。
//  5. 旧 DB(无 enabled 列)迁移后原有 row enabled = true。

func newTestLib(t *testing.T) *PatternLibrary {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "p.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	t.Cleanup(func() { lib.Close() })
	return lib
}

func testPattern(id, category, source string) *UIPattern {
	return &UIPattern{
		ID:             id,
		Category:       category,
		Source:         source,
		AppliesWhen:    MatchCondition{Has: []string{"body"}},
		ActionSequence: []ActionStep{{Tool: "browser.snapshot"}},
	}
}

func TestUpsertDefaultsEnabledTrue(t *testing.T) {
	lib := newTestLib(t)
	p := testPattern("test-default", "nav", "user")
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got := lib.Get("test-default")
	if got == nil {
		t.Fatalf("Get returned nil for freshly upserted pattern")
	}
	if !got.Enabled {
		t.Errorf("new pattern Enabled = false, want true")
	}
}

func TestRecordExecutionAutoDisable(t *testing.T) {
	lib := newTestLib(t)
	p := testPattern("auto-off", "form", "user")
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// 4 次失败还不到阈值,保持 enabled。
	for i := 0; i < 4; i++ {
		if err := lib.RecordExecution(context.Background(), "auto-off", false, 100); err != nil {
			t.Fatalf("RecordExecution: %v", err)
		}
	}
	if got := lib.GetAny("auto-off"); got == nil || !got.Enabled {
		t.Fatalf("after 4 failures, pattern must still be enabled")
	}
	// 第 5 次失败触发 fail>=5 && rate=0 自动禁用。
	if err := lib.RecordExecution(context.Background(), "auto-off", false, 100); err != nil {
		t.Fatalf("RecordExecution 5: %v", err)
	}
	got := lib.GetAny("auto-off")
	if got == nil {
		t.Fatalf("GetAny missing")
	}
	if got.Enabled {
		t.Errorf("after 5 failures with rate=0, pattern should be auto-disabled, got Enabled=true")
	}
	// List 应剔除
	lst := lib.List("form")
	for _, x := range lst {
		if x.ID == "auto-off" {
			t.Errorf("disabled pattern leaked into List: %+v", x)
		}
	}
	// Get 应返回 nil
	if lib.Get("auto-off") != nil {
		t.Errorf("Get on disabled pattern should return nil")
	}
	// ListAll 应包含
	foundAll := false
	for _, x := range lib.ListAll("form") {
		if x.ID == "auto-off" {
			foundAll = true
			break
		}
	}
	if !foundAll {
		t.Errorf("ListAll should include disabled pattern")
	}
}

func TestAutoDisableNotTriggeredWithHighSuccessRate(t *testing.T) {
	// 7 成功 + 5 失败 → rate ≈ 0.58,高于 0.3,不触发。
	lib := newTestLib(t)
	p := testPattern("mixed", "", "user")
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for i := 0; i < 7; i++ {
		_ = lib.RecordExecution(context.Background(), "mixed", true, 100)
	}
	for i := 0; i < 5; i++ {
		_ = lib.RecordExecution(context.Background(), "mixed", false, 100)
	}
	got := lib.GetAny("mixed")
	if got == nil || !got.Enabled {
		t.Errorf("high success-rate pattern should stay enabled, got %+v", got)
	}
}

func TestSetEnabledManualReset(t *testing.T) {
	lib := newTestLib(t)
	p := testPattern("manual", "", "user")
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := lib.SetEnabled(context.Background(), "manual", false); err != nil {
		t.Fatalf("SetEnabled false: %v", err)
	}
	if lib.Get("manual") != nil {
		t.Errorf("manual-disabled pattern should not show in Get")
	}
	if err := lib.SetEnabled(context.Background(), "manual", true); err != nil {
		t.Fatalf("SetEnabled true: %v", err)
	}
	if got := lib.Get("manual"); got == nil || !got.Enabled {
		t.Errorf("after manual reset, pattern should be visible and enabled")
	}
}

func TestSetEnabledNotFound(t *testing.T) {
	lib := newTestLib(t)
	if err := lib.SetEnabled(context.Background(), "no-such-id", true); err == nil {
		t.Errorf("SetEnabled on missing id should return error")
	}
}

func TestReloadPreservesEnabled(t *testing.T) {
	// 持久化后重新打开,disabled 状态必须跨重启保留。
	dsn := filepath.Join(t.TempDir(), "persist.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	p := testPattern("persisted", "", "user")
	if err := lib.Upsert(context.Background(), p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := lib.SetEnabled(context.Background(), "persisted", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	lib.Close()

	lib2, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer lib2.Close()
	got := lib2.GetAny("persisted")
	if got == nil {
		t.Fatalf("pattern lost across reopen")
	}
	if got.Enabled {
		t.Errorf("disabled state not persisted across reopen")
	}
}

func TestPatternLibraryReloadIfChangedObservesExternalWrites(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "shared.db")

	lib1, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("open lib1: %v", err)
	}
	defer lib1.Close()
	lib1.reloadInterval = 0

	lib2, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("open lib2: %v", err)
	}
	defer lib2.Close()

	p := &UIPattern{ID: "external-write", Category: "auth", Source: "learned", AppliesWhen: MatchCondition{Has: []string{"input"}}, ActionSequence: []ActionStep{{Tool: "browser.click"}}}
	if err := lib2.Upsert(context.Background(), p); err != nil {
		t.Fatalf("lib2.Upsert: %v", err)
	}

	if got := lib1.GetAny("external-write"); got == nil {
		t.Fatal("ReloadIfChanged-backed GetAny should observe external write")
	}
}

func TestEnsureEnabledColumnIdempotent(t *testing.T) {
	// 第二次调用不应报错(已有列)。addColumnIfMissing 在 NewPatternLibrary
	// 里已经执行过一次,直接再跑一次验证幂等(P3.2 把 ensureEnabledColumn
	// 归一成通用 addColumnIfMissing,同一语义)。
	lib := newTestLib(t)
	if err := addColumnIfMissing(lib.db, `ALTER TABLE ui_patterns ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`); err != nil {
		t.Errorf("addColumnIfMissing second call: %v", err)
	}
}
