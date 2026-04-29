package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// fakeExitableAgent 模拟可以退出的 agent。
type fakeExitableAgent struct {
	fakeAgent
	exited bool
}

func (a *fakeExitableAgent) ProcessExited() bool { return a.exited }

func TestHealthCheckResult(t *testing.T) {
	ag := &fakeAgent{kind: agent.KindCode}
	e := newPoolEntry(ag, "code-0")
	e.Acquire()

	pool := &ProcessBrainPool{}
	hm := NewHealthMonitor(pool, DefaultPoolHealthPolicy())
	result := hm.checkEntry(e)

	if !result.Healthy {
		t.Fatal("expected healthy agent")
	}
	if result.ID != "code-0" {
		t.Fatalf("expected id code-0, got %s", result.ID)
	}
	if result.Load != 1 {
		t.Fatalf("expected load 1, got %d", result.Load)
	}
}

func TestHealthCheckUnhealthy(t *testing.T) {
	ag := &fakeExitableAgent{fakeAgent: fakeAgent{kind: agent.KindCode}, exited: true}
	e := newPoolEntry(ag, "code-0")

	pool := &ProcessBrainPool{}
	hm := NewHealthMonitor(pool, DefaultPoolHealthPolicy())
	result := hm.checkEntry(e)

	if result.Healthy {
		t.Fatal("expected unhealthy agent")
	}
	if result.Reason != "process exited" {
		t.Fatalf("expected 'process exited', got %s", result.Reason)
	}
}

func TestHealthMonitorRemoveUnhealthy(t *testing.T) {
	// 创建一个 pool，包含 1 个健康实例和 1 个不健康实例
	healthyAg := &fakeAgent{kind: agent.KindCode}
	unhealthyAg := &fakeExitableAgent{fakeAgent: fakeAgent{kind: agent.KindCode}, exited: true}

	pool := &ProcessBrainPool{
		active: map[agent.Kind][]*poolEntry{
			agent.KindCode: {
				newPoolEntry(healthyAg, "code-0"),
				newPoolEntry(unhealthyAg, "code-1"),
			},
		},
	}

	policy := DefaultPoolHealthPolicy()
	policy.CheckInterval = time.Hour // 不启动 ticker，手动触发
	hm := NewHealthMonitor(pool, policy)

	ctx := context.Background()
	hm.RunOnce(ctx)

	// 不健康实例应被移除
	pool.mu.Lock()
	entries := pool.active[agent.KindCode]
	pool.mu.Unlock()

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after removal, got %d", len(entries))
	}
	if entries[0].id != "code-0" {
		t.Fatalf("expected code-0 remaining, got %s", entries[0].id)
	}
}

func TestDefaultPoolHealthPolicy(t *testing.T) {
	p := DefaultPoolHealthPolicy()
	if p.CheckInterval <= 0 {
		t.Fatal("expected positive check interval")
	}
	if p.MaxLoadPerInstance <= 0 {
		t.Fatal("expected positive max load")
	}
	if p.MinInstancesPerKind <= 0 {
		t.Fatal("expected positive min instances")
	}
	if p.MaxInstancesPerKind < p.MinInstancesPerKind {
		t.Fatal("expected max >= min")
	}
}
