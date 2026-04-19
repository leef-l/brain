package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leef-l/brain/sdk/persistence"
)

// memFailureStore is a minimal PatternFailureStore for tests — no SQLite.
// Goroutine-safe so tests that run ScanForSplit concurrent with RecordPatternFailure
// don't race.
type memFailureStore struct {
	mu      sync.Mutex
	samples []*persistence.PatternFailureSample
	nextID  int64
}

func (m *memFailureStore) SavePatternFailureSample(_ context.Context, s *persistence.PatternFailureSample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *s
	cp.ID = m.nextID
	m.samples = append(m.samples, &cp)
	*s = cp
	return nil
}

func (m *memFailureStore) ListPatternFailureSamples(_ context.Context, patternID string) ([]*persistence.PatternFailureSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*persistence.PatternFailureSample
	for _, s := range m.samples {
		if patternID != "" && s.PatternID != patternID {
			continue
		}
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

// newTestPatternLibrary opens a fresh SQLite-backed library in a temp dir,
// then wipes the seed patterns so tests control the candidate set explicitly.
func newTestPatternLibrary(t *testing.T) *PatternLibrary {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "split.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	t.Cleanup(func() { _ = lib.Close() })

	// Drop all seeded patterns so the test universe is deterministic.
	for _, p := range lib.ListAll("") {
		if err := lib.Delete(context.Background(), p.ID); err != nil {
			t.Fatalf("clear seed: %v", err)
		}
	}
	return lib
}

// learnedParent constructs a minimal source=learned pattern with a given ID.
func learnedParent(id string) *UIPattern {
	return &UIPattern{
		ID:       id,
		Category: "auth",
		Source:   "learned",
		AppliesWhen: MatchCondition{
			URLPattern: `(?i)/login$`,
			Has:        []string{`input[type="password"]`},
		},
		ElementRoles: map[string]ElementDescriptor{
			"email_field": {Tag: "input", CSS: `input[type="email"]`},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.type", TargetRole: "email_field", Params: map[string]interface{}{"text": "$credentials.email"}},
			{Tool: "browser.click", Params: map[string]interface{}{}},
		},
		Enabled: true,
	}
}

func TestSpawnVariantFieldsAccurate(t *testing.T) {
	parent := learnedParent("login_username_password")
	v := SpawnVariant(parent, "https://demo.gitea.com", "captcha")
	if v == nil {
		t.Fatal("SpawnVariant returned nil")
	}
	wantID := "login_username_password__demo_gitea_com__captcha"
	if v.ID != wantID {
		t.Errorf("ID = %q, want %q", v.ID, wantID)
	}
	if !v.Enabled {
		t.Error("variant should be Enabled")
	}
	if !v.Pending {
		t.Error("variant should be Pending on birth")
	}
	if v.Source != "learned" {
		t.Errorf("Source = %q, want learned", v.Source)
	}
	// URLPattern must now mention the host.
	if !strings.Contains(v.AppliesWhen.URLPattern, "demo.gitea.com") &&
		!strings.Contains(v.AppliesWhen.URLPattern, `demo\.gitea\.com`) {
		t.Errorf("AppliesWhen.URLPattern missing site host: %s", v.AppliesWhen.URLPattern)
	}
	// OnAnomaly has a captcha handler.
	h, ok := v.OnAnomaly["captcha"]
	if !ok {
		t.Fatalf("OnAnomaly[captcha] missing: %+v", v.OnAnomaly)
	}
	if h.Action != "retry" {
		t.Errorf("default handler Action = %q, want retry", h.Action)
	}
}

func TestSpawnVariantPreservesParentOnAnomaly(t *testing.T) {
	parent := learnedParent("p1")
	parent.OnAnomaly = map[string]AnomalyHandler{
		"captcha": {Action: "human_intervention", Reason: "manual review"},
	}
	v := SpawnVariant(parent, "https://a.example", "captcha")
	if v == nil {
		t.Fatal("SpawnVariant returned nil")
	}
	// Parent's handler must survive — we don't downgrade manual to retry.
	if got := v.OnAnomaly["captcha"].Action; got != "human_intervention" {
		t.Errorf("OnAnomaly[captcha].Action = %q, want human_intervention", got)
	}
}

func TestSpawnVariantDoesNotShareSlicesWithParent(t *testing.T) {
	parent := learnedParent("p1")
	v := SpawnVariant(parent, "https://a.example", "captcha")
	if v == nil {
		t.Fatal("SpawnVariant returned nil")
	}
	// Mutate the variant's ActionSequence — parent must be unaffected.
	v.ActionSequence[0].Tool = "browser.click"
	if parent.ActionSequence[0].Tool != "browser.type" {
		t.Errorf("parent ActionSequence mutated: parent[0].Tool = %q", parent.ActionSequence[0].Tool)
	}
	// Same check for ElementRoles.
	if v.ElementRoles["email_field"].Tag != "input" {
		t.Fatalf("variant clone missing email_field: %+v", v.ElementRoles)
	}
	delete(v.ElementRoles, "email_field")
	if _, ok := parent.ElementRoles["email_field"]; !ok {
		t.Error("parent ElementRoles mutated by variant delete")
	}
}

func TestSpawnVariantRejectsEmptyInputs(t *testing.T) {
	if v := SpawnVariant(nil, "s", "c"); v != nil {
		t.Error("nil parent should return nil")
	}
	if v := SpawnVariant(learnedParent("p"), "", "c"); v != nil {
		t.Error("empty site should return nil")
	}
	if v := SpawnVariant(learnedParent("p"), "https://a", ""); v != nil {
		t.Error("empty subtype should return nil")
	}
}

func TestRecordPatternFailureWritesSample(t *testing.T) {
	store := &memFailureStore{}
	SetPatternFailureStore(store)
	defer SetPatternFailureStore(nil)

	err := RecordPatternFailure(context.Background(),
		"login_username_password", "https://demo.gitea.com", "captcha", 2,
		"https://demo.gitea.com/user/login")
	if err != nil {
		t.Fatalf("RecordPatternFailure: %v", err)
	}
	if len(store.samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(store.samples))
	}
	got := store.samples[0]
	if got.PatternID != "login_username_password" || got.SiteOrigin != "https://demo.gitea.com" ||
		got.AnomalySubtype != "captcha" || got.FailureStep != 2 {
		t.Errorf("sample fields wrong: %+v", got)
	}
	var fp map[string]interface{}
	if err := json.Unmarshal(got.PageFingerprint, &fp); err != nil {
		t.Fatalf("fingerprint unmarshal: %v", err)
	}
	if fp["url"] != "https://demo.gitea.com/user/login" {
		t.Errorf("fingerprint url = %v", fp["url"])
	}
}

func TestRecordPatternFailureSilentlyNoopsWithoutStore(t *testing.T) {
	SetPatternFailureStore(nil)
	// Should be a complete no-op (no panic, no error).
	if err := RecordPatternFailure(context.Background(), "p", "s", "c", 0, ""); err != nil {
		t.Errorf("unexpected err without store: %v", err)
	}
}

func TestScanForSplitSpawnsVariantAtThreshold(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	parent := learnedParent("login_flow")
	if err := lib.Upsert(ctx, parent); err != nil {
		t.Fatalf("Upsert parent: %v", err)
	}

	store := &memFailureStore{}
	// Five identical failures → must spawn one variant.
	for i := 0; i < splitMinSampleCount; i++ {
		store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
			PatternID:      parent.ID,
			SiteOrigin:     "https://demo.gitea.com",
			AnomalySubtype: "captcha",
			FailureStep:    2,
		})
	}
	// Two unrelated failures → must NOT cause a spawn on their own.
	for i := 0; i < 2; i++ {
		store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
			PatternID: parent.ID, SiteOrigin: "https://x.example", AnomalySubtype: "rate_limited",
		})
	}

	spawned, err := ScanForSplit(ctx, lib, store)
	if err != nil {
		t.Fatalf("ScanForSplit: %v", err)
	}
	if len(spawned) != 1 {
		t.Fatalf("spawned = %v, want exactly 1 variant", spawned)
	}
	wantID := "login_flow__demo_gitea_com__captcha"
	if spawned[0] != wantID {
		t.Errorf("spawned[0] = %q, want %q", spawned[0], wantID)
	}
	// Library must now contain the variant as Pending + Enabled.
	v := lib.GetAny(wantID)
	if v == nil {
		t.Fatalf("variant %q not in library", wantID)
	}
	if !v.Pending || !v.Enabled {
		t.Errorf("variant flags wrong: pending=%v enabled=%v", v.Pending, v.Enabled)
	}
}

func TestScanForSplitIdempotent(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	parent := learnedParent("login_flow")
	_ = lib.Upsert(ctx, parent)

	store := &memFailureStore{}
	for i := 0; i < splitMinSampleCount+3; i++ {
		store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
			PatternID: parent.ID, SiteOrigin: "https://a.example", AnomalySubtype: "captcha",
		})
	}

	first, _ := ScanForSplit(ctx, lib, store)
	second, _ := ScanForSplit(ctx, lib, store)
	if len(first) != 1 {
		t.Fatalf("first scan = %v, want 1", first)
	}
	if len(second) != 0 {
		t.Errorf("second scan should be no-op (variant already exists), got %v", second)
	}
}

func TestScanForSplitSkipsSeedSource(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	// Seed-sourced pattern must not be split (protection rule).
	seed := learnedParent("seed_login")
	seed.Source = "seed"
	if err := lib.Upsert(ctx, seed); err != nil {
		t.Fatalf("Upsert seed: %v", err)
	}
	store := &memFailureStore{}
	for i := 0; i < splitMinSampleCount*2; i++ {
		store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
			PatternID: seed.ID, SiteOrigin: "https://a.example", AnomalySubtype: "captcha",
		})
	}
	spawned, err := ScanForSplit(ctx, lib, store)
	if err != nil {
		t.Fatalf("ScanForSplit: %v", err)
	}
	if len(spawned) != 0 {
		t.Errorf("seed source must not spawn variants; got %v", spawned)
	}
}

func TestScanForSplitSkipsSamplesWithoutAnomalySubtype(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	parent := learnedParent("generic_form")
	_ = lib.Upsert(ctx, parent)
	store := &memFailureStore{}
	// Plenty of samples but subtype is empty → ScanForSplit must ignore them.
	for i := 0; i < splitMinSampleCount*2; i++ {
		store.SavePatternFailureSample(ctx, &persistence.PatternFailureSample{
			PatternID: parent.ID, SiteOrigin: "https://a.example", AnomalySubtype: "",
		})
	}
	spawned, _ := ScanForSplit(ctx, lib, store)
	if len(spawned) != 0 {
		t.Errorf("samples without anomaly subtype must not spawn; got %v", spawned)
	}
}

func TestPendingClearedAfterThreeSuccesses(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	p := learnedParent("variant")
	p.Pending = true
	if err := lib.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := lib.RecordExecution(ctx, p.ID, true, 100); err != nil {
			t.Fatalf("RecordExecution: %v", err)
		}
	}
	got := lib.GetAny(p.ID)
	if got == nil {
		t.Fatal("pattern vanished")
	}
	if got.Pending {
		t.Errorf("Pending still set after 3 successes: %+v", got.Stats)
	}
}

func TestPendingSurvivesUpsertReload(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "p.db")

	// Create lib, wipe seeds, write a Pending variant, close.
	{
		lib, err := NewPatternLibrary(dsn)
		if err != nil {
			t.Fatalf("open1: %v", err)
		}
		for _, seeded := range lib.ListAll("") {
			_ = lib.Delete(context.Background(), seeded.ID)
		}
		p := learnedParent("v1")
		p.Pending = true
		if err := lib.Upsert(context.Background(), p); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		_ = lib.Close()
	}

	// Re-open and verify the pending flag survived.
	lib2, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer lib2.Close()
	got := lib2.GetAny("v1")
	if got == nil {
		t.Fatal("pattern missing after reopen")
	}
	if !got.Pending {
		t.Errorf("Pending lost across reopen: %+v", got)
	}
}

func TestVariantStillSubjectToAutoDisable(t *testing.T) {
	lib := newTestPatternLibrary(t)
	ctx := context.Background()
	// A variant (Pending=true) that keeps failing: M3 auto-disable must
	// still kick in after 5 failures with <30% success rate.
	v := learnedParent("dying_variant")
	v.Pending = true
	if err := lib.Upsert(ctx, v); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := lib.RecordExecution(ctx, v.ID, false, 100); err != nil {
			t.Fatalf("RecordExecution %d: %v", i, err)
		}
	}
	got := lib.GetAny(v.ID)
	if got == nil {
		t.Fatal("pattern vanished")
	}
	if got.Enabled {
		t.Errorf("M3 auto-disable did not trigger for pending variant: %+v", got.Stats)
	}
}
