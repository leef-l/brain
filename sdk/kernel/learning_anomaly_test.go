package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// fakeAnomalyStore 实现 persistence.LearningStore 的最小子集,只追踪本 P3.1
// 加的几组方法。其余方法留空实现以满足接口即可。
type fakeAnomalyStore struct {
	templates        []*persistence.AnomalyTemplate
	siteProfiles     []*persistence.SiteAnomalyProfile
	deleteCalled     []int64
	templatesListHit int
}

func (f *fakeAnomalyStore) SaveAnomalyTemplate(_ context.Context, tpl *persistence.AnomalyTemplate) error {
	tpl.ID = int64(len(f.templates) + 1)
	f.templates = append(f.templates, tpl)
	return nil
}
func (f *fakeAnomalyStore) GetAnomalyTemplate(_ context.Context, id int64) (*persistence.AnomalyTemplate, error) {
	for _, t := range f.templates {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, nil
}
func (f *fakeAnomalyStore) ListAnomalyTemplates(_ context.Context) ([]*persistence.AnomalyTemplate, error) {
	f.templatesListHit++
	return f.templates, nil
}
func (f *fakeAnomalyStore) DeleteAnomalyTemplate(_ context.Context, id int64) error {
	f.deleteCalled = append(f.deleteCalled, id)
	return nil
}
func (f *fakeAnomalyStore) UpsertSiteAnomalyProfile(_ context.Context, p *persistence.SiteAnomalyProfile) error {
	f.siteProfiles = append(f.siteProfiles, p)
	return nil
}
func (f *fakeAnomalyStore) ListSiteAnomalyProfiles(_ context.Context, site string) ([]*persistence.SiteAnomalyProfile, error) {
	var out []*persistence.SiteAnomalyProfile
	for _, p := range f.siteProfiles {
		if p.SiteOrigin == site {
			out = append(out, p)
		}
	}
	return out, nil
}

// 下面是 LearningStore 接口的无关方法,空实现。
func (f *fakeAnomalyStore) SaveProfile(context.Context, *persistence.LearningProfile) error { return nil }
func (f *fakeAnomalyStore) SaveTaskScore(context.Context, *persistence.LearningTaskScore) error {
	return nil
}
func (f *fakeAnomalyStore) ListProfiles(context.Context) ([]*persistence.LearningProfile, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) ListTaskScores(context.Context, string) ([]*persistence.LearningTaskScore, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SaveSequence(context.Context, *persistence.LearningSequence) error {
	return nil
}
func (f *fakeAnomalyStore) ListSequences(context.Context, int) ([]*persistence.LearningSequence, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SaveInteractionSequence(context.Context, *persistence.InteractionSequence) error {
	return nil
}
func (f *fakeAnomalyStore) ListInteractionSequences(context.Context, string, int) ([]*persistence.InteractionSequence, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SavePreference(context.Context, *persistence.LearningPreference) error {
	return nil
}
func (f *fakeAnomalyStore) GetPreference(context.Context, string) (*persistence.LearningPreference, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) ListPreferences(context.Context) ([]*persistence.LearningPreference, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SaveDailySummary(context.Context, *persistence.DailySummary) error {
	return nil
}
func (f *fakeAnomalyStore) GetDailySummary(context.Context, string) (*persistence.DailySummary, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) ListDailySummaries(context.Context, int) ([]*persistence.DailySummary, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SavePatternFailureSample(context.Context, *persistence.PatternFailureSample) error {
	return nil
}
func (f *fakeAnomalyStore) ListPatternFailureSamples(context.Context, string) ([]*persistence.PatternFailureSample, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SaveHumanDemoSequence(context.Context, *persistence.HumanDemoSequence) error {
	return nil
}
func (f *fakeAnomalyStore) ListHumanDemoSequences(context.Context, bool) ([]*persistence.HumanDemoSequence, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) SaveSitemapSnapshot(context.Context, *persistence.SitemapSnapshot) error {
	return nil
}
func (f *fakeAnomalyStore) GetSitemapSnapshot(context.Context, string, int) (*persistence.SitemapSnapshot, error) {
	return nil, nil
}
func (f *fakeAnomalyStore) PurgeSitemapSnapshots(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestLearningEngineSaveAnomalyTemplate(t *testing.T) {
	store := &fakeAnomalyStore{}
	le := NewLearningEngineWithStore(store)
	ctx := context.Background()

	tpl := &persistence.AnomalyTemplate{
		SignatureType:   "rate_limited",
		SignatureSubtype: "429_cooldown",
		RecoveryActions: json.RawMessage(`[{"kind":"retry","max_retries":2}]`),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := le.SaveAnomalyTemplate(ctx, tpl); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tpl.ID == 0 {
		t.Fatal("store should have filled ID")
	}
	got, err := le.GetAnomalyTemplate(ctx, tpl.ID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v, got=%+v", err, got)
	}
	if got.SignatureSubtype != "429_cooldown" {
		t.Errorf("wrong template: %+v", got)
	}
	list, _ := le.ListAnomalyTemplates(ctx)
	if len(list) != 1 {
		t.Errorf("list = %d, want 1", len(list))
	}
	if err := le.DeleteAnomalyTemplate(ctx, tpl.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.deleteCalled) != 1 || store.deleteCalled[0] != tpl.ID {
		t.Errorf("delete not called properly: %+v", store.deleteCalled)
	}
}

func TestLearningEngineSaveAnomalyTemplateNoStore(t *testing.T) {
	le := NewLearningEngine() // 不带 store
	ctx := context.Background()
	if err := le.SaveAnomalyTemplate(ctx, &persistence.AnomalyTemplate{}); err != nil {
		t.Errorf("no-store should be silent no-op, got %v", err)
	}
	tpl, err := le.GetAnomalyTemplate(ctx, 1)
	if err != nil || tpl != nil {
		t.Errorf("no-store get should return (nil,nil), got (%v, %v)", tpl, err)
	}
	list, err := le.ListAnomalyTemplates(ctx)
	if err != nil || list != nil {
		t.Errorf("no-store list: %v %v", list, err)
	}
	// Delete nil / id=0 也安全
	if err := le.DeleteAnomalyTemplate(ctx, 0); err != nil {
		t.Errorf("delete id=0 should no-op: %v", err)
	}
}

func TestLearningEngineSiteAnomalyProfile(t *testing.T) {
	store := &fakeAnomalyStore{}
	le := NewLearningEngineWithStore(store)
	ctx := context.Background()
	profile := &persistence.SiteAnomalyProfile{
		SiteOrigin:          "https://shop.example",
		AnomalyType:         "rate_limited",
		AnomalySubtype:     "429_cooldown",
		Frequency:          3,
		AvgDurationMs:      1500,
		RecoverySuccessRate: 0.66,
		LastSeenAt:         time.Now(),
	}
	if err := le.UpsertSiteAnomalyProfile(ctx, profile); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	out, err := le.ListSiteAnomalyProfiles(ctx, "https://shop.example")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].Frequency != 3 {
		t.Errorf("got %+v", out)
	}
	// 空 site 直接返回空,不访问 store
	out2, err := le.ListSiteAnomalyProfiles(ctx, "")
	if err != nil || out2 != nil {
		t.Errorf("empty site should no-op, got %v %v", out2, err)
	}
}
