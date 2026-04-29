package kernel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

func TestEnvKey(t *testing.T) {
	cases := []struct {
		input string
		key   string
		ok    bool
	}{
		{"FOO=bar", "FOO", true},
		{"PATH=/usr/bin", "PATH", true},
		{"EMPTY=", "EMPTY", true},
		{"NO_EQUALS", "NO_EQUALS", false},
		{"=VALUE", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		k, ok := envKey(c.input)
		if ok != c.ok {
			t.Fatalf("envKey(%q) ok=%v, want %v", c.input, ok, c.ok)
		}
		if k != c.key {
			t.Fatalf("envKey(%q) key=%q, want %q", c.input, k, c.key)
		}
	}
}

func TestMergeProcessEnv(t *testing.T) {
	// Empty extra returns copy of base.
	merged := mergeProcessEnv([]string{"A=1", "B=2"}, nil)
	if len(merged) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(merged))
	}

	// Extra overrides base.
	merged = mergeProcessEnv([]string{"A=1", "B=2"}, []string{"A=3", "C=4"})
	if len(merged) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(merged))
	}
	m := make(map[string]string, len(merged))
	for _, e := range merged {
		k, _ := envKey(e)
		m[k] = e
	}
	if m["A"] != "A=3" {
		t.Fatalf("expected A=3, got %s", m["A"])
	}
	if m["B"] != "B=2" {
		t.Fatalf("expected B=2, got %s", m["B"])
	}
	if m["C"] != "C=4" {
		t.Fatalf("expected C=4, got %s", m["C"])
	}

	// Empty base with extra falls back to os.Environ() then applies extra.
	merged = mergeProcessEnv(nil, []string{"TEST_VAR_UNIQUE=xyz"})
	if len(merged) == 0 {
		t.Fatal("expected non-empty merged env when extra is provided")
	}
	found := false
	for _, e := range merged {
		if e == "TEST_VAR_UNIQUE=xyz" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected TEST_VAR_UNIQUE=xyz in merged env")
	}
}

func TestProcessBrainPoolAvailable(t *testing.T) {
	p := &ProcessBrainPool{
		available: map[agent.Kind]bool{
			agent.KindCode: true,
		},
	}
	if !p.Available(agent.KindCode) {
		t.Fatal("expected Code brain to be available")
	}
	if p.Available(agent.KindBrowser) {
		t.Fatal("expected Browser brain to NOT be available")
	}
}

func TestProcessBrainPoolAvailableKinds(t *testing.T) {
	p := &ProcessBrainPool{
		available: map[agent.Kind]bool{
			agent.KindCode:    true,
			agent.KindBrowser: true,
		},
	}
	kinds := p.AvailableKinds()
	if len(kinds) != 2 {
		t.Fatalf("expected 2 kinds, got %d", len(kinds))
	}
}

func TestProcessBrainPoolRegistrations(t *testing.T) {
	p := &ProcessBrainPool{
		registrations: map[agent.Kind]*BrainRegistration{
			agent.KindCode: {Kind: agent.KindCode, Binary: "/bin/code"},
		},
	}
	regs := p.Registrations()
	if len(regs) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(regs))
	}
	if regs[0].Kind != agent.KindCode {
		t.Fatalf("expected Kind=code, got %s", regs[0].Kind)
	}
}

func TestProcessBrainPoolHealthCheckEmpty(t *testing.T) {
	p := &ProcessBrainPool{
		active: map[agent.Kind][]*poolEntry{},
	}
	result := p.HealthCheck()
	if len(result) != 0 {
		t.Fatalf("expected empty health check, got %d", len(result))
	}
}

func TestProcessBrainPoolIsAliveNil(t *testing.T) {
	p := &ProcessBrainPool{}
	if p.isAlive(nil) {
		t.Fatal("expected isAlive(nil)=false")
	}
}

func TestNewProcessBrainPoolEmpty(t *testing.T) {
	p := NewProcessBrainPool(nil, nil, OrchestratorConfig{})
	if p == nil {
		t.Fatal("expected non-nil pool")
	}
	if p.active == nil {
		t.Fatal("expected active map initialized")
	}
	if p.available == nil {
		t.Fatal("expected available map initialized")
	}
}

// ---------------------------------------------------------------------------
// 负载均衡策略测试
// ---------------------------------------------------------------------------

type fakeAgent struct {
	kind agent.Kind
}

func (a *fakeAgent) Kind() agent.Kind             { return a.kind }
func (a *fakeAgent) Descriptor() agent.Descriptor { return agent.Descriptor{} }
func (a *fakeAgent) Ready(ctx context.Context) error { return nil }
func (a *fakeAgent) Shutdown(ctx context.Context) error { return nil }

func makeTestEntries(kind agent.Kind, loads []int64) []*poolEntry {
	entries := make([]*poolEntry, len(loads))
	for i, load := range loads {
		e := newPoolEntry(&fakeAgent{kind: kind}, fmt.Sprintf("%s-%d", kind, i))
		// 手动设置负载（绕过原子操作，测试专用）。
		for j := int64(0); j < load; j++ {
			e.Acquire()
		}
		entries[i] = e
	}
	return entries
}

func TestRoundRobinStrategy(t *testing.T) {
	entries := makeTestEntries(agent.KindCode, []int64{0, 0, 0})
	s := NewRoundRobinStrategy()

	// 轮询应依次返回 0, 1, 2, 0, 1...
	for round := 0; round < 3; round++ {
		for i := 0; i < len(entries); i++ {
			selected := s.Select(entries)
			if selected == nil {
				t.Fatalf("round %d idx %d: expected non-nil", round, i)
			}
			wantID := fmt.Sprintf("code-%d", i)
			if selected.id != wantID {
				t.Fatalf("round %d idx %d: got %s, want %s", round, i, selected.id, wantID)
			}
		}
	}

	// 空列表返回 nil
	if s.Select(nil) != nil {
		t.Fatal("expected nil for empty entries")
	}
}

func TestLeastLoadedStrategy(t *testing.T) {
	entries := makeTestEntries(agent.KindCode, []int64{5, 1, 3})
	s := NewLeastLoadedStrategy()

	// 应选择负载最小的实例（索引 1，load=1）
	selected := s.Select(entries)
	if selected == nil {
		t.Fatal("expected non-nil")
	}
	if selected.id != "code-1" {
		t.Fatalf("got %s, want code-1", selected.id)
	}

	// 空列表返回 nil
	if s.Select(nil) != nil {
		t.Fatal("expected nil for empty entries")
	}
}

func TestLatencyAwareStrategy(t *testing.T) {
	entries := makeTestEntries(agent.KindCode, []int64{0, 0, 0})
	// 设置不同延迟
	entries[0].RecordLatency(500 * time.Millisecond)
	entries[1].RecordLatency(100 * time.Millisecond)
	entries[2].RecordLatency(300 * time.Millisecond)

	s := NewLatencyAwareStrategy()

	// 延迟最低的是索引 1（100ms）
	selected := s.Select(entries)
	if selected == nil {
		t.Fatal("expected non-nil")
	}
	if selected.id != "code-1" {
		t.Fatalf("got %s, want code-1", selected.id)
	}

	// 增加负载后，负载惩罚应改变选择
	entries[1].Acquire()
	entries[1].Acquire()
	entries[1].Acquire()
	// entries[1] 现在 load=3, latency=100ms, score=100ms+3*0.5s=1600ms
	// entries[0] load=0, latency=500ms, score=500ms
	// entries[2] load=0, latency=300ms, score=300ms  ← 最低
	selected = s.Select(entries)
	if selected.id != "code-2" {
		t.Fatalf("after load change got %s, want code-2", selected.id)
	}

	// 空列表返回 nil
	if s.Select(nil) != nil {
		t.Fatal("expected nil for empty entries")
	}
}

func TestPoolEntryAcquireRelease(t *testing.T) {
	ag := &fakeAgent{kind: agent.KindCode}
	e := newPoolEntry(ag, "code-0")

	if e.CurrentLoad() != 0 {
		t.Fatalf("expected load 0, got %d", e.CurrentLoad())
	}

	e.Acquire()
	e.Acquire()
	if e.CurrentLoad() != 2 {
		t.Fatalf("expected load 2, got %d", e.CurrentLoad())
	}

	e.Release()
	if e.CurrentLoad() != 1 {
		t.Fatalf("expected load 1, got %d", e.CurrentLoad())
	}

	// 过度释放不应变为负数
	e.Release()
	e.Release()
	e.Release()
	if e.CurrentLoad() != 0 {
		t.Fatalf("expected load 0 after over-release, got %d", e.CurrentLoad())
	}
}

func TestPoolEntryLatency(t *testing.T) {
	ag := &fakeAgent{kind: agent.KindCode}
	e := newPoolEntry(ag, "code-0")

	if e.LatencyEWMA() != 0 {
		t.Fatalf("expected initial latency 0, got %f", e.LatencyEWMA())
	}

	e.RecordLatency(100 * time.Millisecond)
	e.RecordLatency(200 * time.Millisecond)
	// EWMA 从 0 开始，alpha=0.2：
	// 第一次: 0.2*0.1 = 0.02
	// 第二次: 0.2*0.2 + 0.8*0.02 = 0.056
	lat := e.LatencyEWMA()
	if lat < 0.01 || lat > 0.2 {
		t.Fatalf("expected latency between 0.01 and 0.2, got %f", lat)
	}
}

func TestProcessBrainPoolFilterAliveLocked(t *testing.T) {
	p := &ProcessBrainPool{}

	// nil 返回 nil
	if p.filterAliveLocked(nil) != nil {
		t.Fatal("expected nil for nil entries")
	}

	// 空 slice 返回 nil
	if p.filterAliveLocked([]*poolEntry{}) != nil {
		t.Fatal("expected nil for empty entries")
	}
}
