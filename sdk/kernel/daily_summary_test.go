package kernel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/persistence"
)

type memLearningStore struct {
	mu       sync.Mutex
	summary  map[string]*persistence.DailySummary
}

func newMemLearningStore() *memLearningStore {
	return &memLearningStore{summary: map[string]*persistence.DailySummary{}}
}

func (s *memLearningStore) SaveDailySummary(_ context.Context, d *persistence.DailySummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *d
	s.summary[d.Date] = &cp
	return nil
}
func (s *memLearningStore) GetDailySummary(_ context.Context, date string) (*persistence.DailySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summary[date], nil
}
func (s *memLearningStore) ListDailySummaries(_ context.Context, _ int) ([]*persistence.DailySummary, error) {
	return nil, nil
}

// unused L1/L2/L3 methods
func (s *memLearningStore) SaveProfile(context.Context, *persistence.LearningProfile) error {
	return nil
}
func (s *memLearningStore) SaveTaskScore(context.Context, *persistence.LearningTaskScore) error {
	return nil
}
func (s *memLearningStore) ListProfiles(context.Context) ([]*persistence.LearningProfile, error) {
	return nil, nil
}
func (s *memLearningStore) ListTaskScores(context.Context, string) ([]*persistence.LearningTaskScore, error) {
	return nil, nil
}
func (s *memLearningStore) SaveSequence(context.Context, *persistence.LearningSequence) error {
	return nil
}
func (s *memLearningStore) ListSequences(context.Context, int) ([]*persistence.LearningSequence, error) {
	return nil, nil
}
func (s *memLearningStore) SaveInteractionSequence(context.Context, *persistence.InteractionSequence) error {
	return nil
}
func (s *memLearningStore) ListInteractionSequences(context.Context, string, int) ([]*persistence.InteractionSequence, error) {
	return nil, nil
}
func (s *memLearningStore) SavePreference(context.Context, *persistence.LearningPreference) error {
	return nil
}
func (s *memLearningStore) GetPreference(context.Context, string) (*persistence.LearningPreference, error) {
	return nil, nil
}
func (s *memLearningStore) ListPreferences(context.Context) ([]*persistence.LearningPreference, error) {
	return nil, nil
}

// ── P3.0 bootstrap (#16) 扩出的新方法 ── 测试里不关心,空桩到位即可。
func (s *memLearningStore) SaveAnomalyTemplate(context.Context, *persistence.AnomalyTemplate) error {
	return nil
}
func (s *memLearningStore) GetAnomalyTemplate(context.Context, int64) (*persistence.AnomalyTemplate, error) {
	return nil, nil
}
func (s *memLearningStore) ListAnomalyTemplates(context.Context) ([]*persistence.AnomalyTemplate, error) {
	return nil, nil
}
func (s *memLearningStore) DeleteAnomalyTemplate(context.Context, int64) error { return nil }
func (s *memLearningStore) UpsertSiteAnomalyProfile(context.Context, *persistence.SiteAnomalyProfile) error {
	return nil
}
func (s *memLearningStore) ListSiteAnomalyProfiles(context.Context, string) ([]*persistence.SiteAnomalyProfile, error) {
	return nil, nil
}
func (s *memLearningStore) SavePatternFailureSample(context.Context, *persistence.PatternFailureSample) error {
	return nil
}
func (s *memLearningStore) ListPatternFailureSamples(context.Context, string) ([]*persistence.PatternFailureSample, error) {
	return nil, nil
}
func (s *memLearningStore) SaveHumanDemoSequence(context.Context, *persistence.HumanDemoSequence) error {
	return nil
}
func (s *memLearningStore) ListHumanDemoSequences(context.Context, bool) ([]*persistence.HumanDemoSequence, error) {
	return nil, nil
}
func (s *memLearningStore) GetHumanDemoSequence(context.Context, int64) (*persistence.HumanDemoSequence, error) {
	return nil, nil
}
func (s *memLearningStore) ApproveHumanDemoSequence(context.Context, int64) error {
	return nil
}
func (s *memLearningStore) DeleteHumanDemoSequence(context.Context, int64) error {
	return nil
}
func (s *memLearningStore) PurgeHumanDemoSequences(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (s *memLearningStore) SaveSitemapSnapshot(context.Context, *persistence.SitemapSnapshot) error {
	return nil
}
func (s *memLearningStore) GetSitemapSnapshot(context.Context, string, int) (*persistence.SitemapSnapshot, error) {
	return nil, nil
}
func (s *memLearningStore) PurgeSitemapSnapshots(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestSummaryDaemonIngestsTerminalStates(t *testing.T) {
	bus := events.NewMemEventBus()
	store := newMemLearningStore()

	d := NewSummaryDaemon(SummaryDaemonConfig{
		Bus:   bus,
		Store: store,
		Logger: func(string, ...interface{}) {},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	time.Sleep(20 * time.Millisecond)

	// 模拟多条 TaskExecution Transition 事件
	for _, s := range []string{"running", "completed", "failed", "completed", "waiting_tool"} {
		payload, _ := json.Marshal(map[string]string{"brain_id": "browser"})
		bus.Publish(ctx, events.Event{
			Type:      "task.state." + s,
			Timestamp: time.Now(),
			Data:      payload,
		})
	}
	time.Sleep(100 * time.Millisecond)

	d.ForceFlush(ctx)

	date := time.Now().UTC().Format("2006-01-02")
	got, _ := store.GetDailySummary(ctx, date)
	if got == nil {
		t.Fatalf("summary missing for %s", date)
	}
	if got.RunsTotal != 3 {
		t.Errorf("expected 3 terminal states, got %d", got.RunsTotal)
	}
	if got.RunsFailed != 1 {
		t.Errorf("expected 1 failed, got %d", got.RunsFailed)
	}
	var counts map[string]int
	_ = json.Unmarshal([]byte(got.BrainCounts), &counts)
	if counts["browser"] != 3 {
		t.Errorf("browser count = %d, want 3", counts["browser"])
	}
}

func TestSummaryDaemonWithoutStoreIsNoop(t *testing.T) {
	bus := events.NewMemEventBus()
	d := NewSummaryDaemon(SummaryDaemonConfig{
		Bus:   bus,
		Store: nil,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()
}

func TestSummaryDaemonRollsOverAtMidnight(t *testing.T) {
	bus := events.NewMemEventBus()
	store := newMemLearningStore()

	// Fake clock — 先在 23:59 接事件,再切到下一天
	clock := time.Date(2026, 4, 19, 23, 59, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	d := NewSummaryDaemon(SummaryDaemonConfig{
		Bus:    bus,
		Store:  store,
		Now:    now,
		Logger: func(string, ...interface{}) {},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()
	time.Sleep(20 * time.Millisecond)

	payload, _ := json.Marshal(map[string]string{"brain_id": "code"})
	bus.Publish(ctx, events.Event{Type: "task.state.completed", Data: payload})
	time.Sleep(30 * time.Millisecond)

	// 切到下一天
	mu.Lock()
	clock = clock.Add(2 * time.Minute) // 跨零点
	mu.Unlock()

	// 再来一条事件应触发 rollover
	bus.Publish(ctx, events.Event{Type: "task.state.completed", Data: payload})
	time.Sleep(100 * time.Millisecond)

	got1, _ := store.GetDailySummary(ctx, "2026-04-19")
	if got1 == nil {
		t.Fatalf("previous day summary missing")
	}
	if got1.RunsTotal != 1 {
		t.Errorf("prev RunsTotal = %d, want 1", got1.RunsTotal)
	}
}
