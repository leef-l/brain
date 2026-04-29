// Package kernel — PoolEntry 与负载均衡策略。
//
// 每个 brain kind 可以维护多个实例（poolEntry），负载均衡策略
// 负责在多个存活实例中选择最优的一个。这是实现 kimi 2.6 超越
// 所需的实例级水平扩展能力。
package kernel

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// poolEntry 表示 brain pool 中的单个实例。
type poolEntry struct {
	agent    agent.Agent
	id       string
	load     atomic.Int64   // 当前活跃请求数
	lastUsed atomic.Value   // time.Time

	// latency 记录该实例的历史响应延迟 EWMA（纳秒级）。
	latency    *EWMAScore
	latencyMu  sync.RWMutex

	// createdAt 记录实例启动时间。
	createdAt time.Time
}

// newPoolEntry 创建一个 pool entry。
func newPoolEntry(ag agent.Agent, id string) *poolEntry {
	e := &poolEntry{
		agent:     ag,
		id:        id,
		latency:   &EWMAScore{},
		createdAt: time.Now(),
	}
	e.lastUsed.Store(time.Now())
	return e
}

// Acquire 增加负载计数并更新最后使用时间。
func (e *poolEntry) Acquire() {
	e.load.Add(1)
	e.lastUsed.Store(time.Now())
}

// Release 减少负载计数。
func (e *poolEntry) Release() {
	v := e.load.Add(-1)
	if v < 0 {
		e.load.Store(0)
	}
}

// CurrentLoad 返回当前活跃请求数。
func (e *poolEntry) CurrentLoad() int64 {
	return e.load.Load()
}

// RecordLatency 记录一次调用的延迟。
func (e *poolEntry) RecordLatency(d time.Duration) {
	e.latencyMu.Lock()
	defer e.latencyMu.Unlock()
	e.latency.Update(d.Seconds())
}

// LatencyEWMA 返回该实例的延迟 EWMA（秒）。
func (e *poolEntry) LatencyEWMA() float64 {
	e.latencyMu.RLock()
	defer e.latencyMu.RUnlock()
	return e.latency.Value
}

// LastUsed 返回最后使用时间。
func (e *poolEntry) LastUsed() time.Time {
	return e.lastUsed.Load().(time.Time)
}

// ---------------------------------------------------------------------------
// 负载均衡策略
// ---------------------------------------------------------------------------

// LoadBalanceStrategy 定义 brain 实例选择策略。
type LoadBalanceStrategy interface {
	// Select 从存活实例中选择最优的一个。返回 nil 表示无可用实例。
	Select(entries []*poolEntry) *poolEntry
}

// RoundRobinStrategy 轮询策略。
type RoundRobinStrategy struct {
	mu   sync.Mutex
	next map[string]int // key=kind string
}

// NewRoundRobinStrategy 创建轮询策略。
func NewRoundRobinStrategy() *RoundRobinStrategy {
	return &RoundRobinStrategy{next: make(map[string]int)}
}

// Select 按轮询顺序选择下一个实例。
func (s *RoundRobinStrategy) Select(entries []*poolEntry) *poolEntry {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 所有实例共享同一个 kind，用第一个实例的 kind 作为 key。
	key := string(entries[0].agent.Kind())
	idx := s.next[key]
	selected := entries[idx%len(entries)]
	s.next[key] = idx + 1
	return selected
}

// LeastLoadedStrategy 最小负载策略。
type LeastLoadedStrategy struct{}

// NewLeastLoadedStrategy 创建最小负载策略。
func NewLeastLoadedStrategy() *LeastLoadedStrategy {
	return &LeastLoadedStrategy{}
}

// Select 选择当前负载最小的实例。
func (s *LeastLoadedStrategy) Select(entries []*poolEntry) *poolEntry {
	if len(entries) == 0 {
		return nil
	}
	var best *poolEntry
	var bestLoad int64 = -1
	for _, e := range entries {
		load := e.CurrentLoad()
		if best == nil || load < bestLoad {
			best = e
			bestLoad = load
		}
	}
	return best
}

// LatencyAwareStrategy 延迟感知策略。
// 优先选择延迟最低的实例，如果延迟相近则 fallback 到最小负载。
type LatencyAwareStrategy struct{}

// NewLatencyAwareStrategy 创建延迟感知策略。
func NewLatencyAwareStrategy() *LatencyAwareStrategy {
	return &LatencyAwareStrategy{}
}

// Select 选择延迟最低（或负载最小）的实例。
func (s *LatencyAwareStrategy) Select(entries []*poolEntry) *poolEntry {
	if len(entries) == 0 {
		return nil
	}
	var best *poolEntry
	var bestScore float64 = -1
	for _, e := range entries {
		latency := e.LatencyEWMA()
		load := float64(e.CurrentLoad())
		// 评分 = 延迟 + 负载惩罚。延迟为主要因素，每 1 个并发请求加 0.5s 惩罚。
		score := latency + load*0.5
		if best == nil || score < bestScore {
			best = e
			bestScore = score
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// 默认策略
// ---------------------------------------------------------------------------

// DefaultLoadBalanceStrategy 返回默认的负载均衡策略。
// 当前默认使用延迟感知策略，兼顾响应速度和负载均衡。
func DefaultLoadBalanceStrategy() LoadBalanceStrategy {
	return NewLatencyAwareStrategy()
}
