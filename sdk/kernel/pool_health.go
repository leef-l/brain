// Package kernel — BrainPool 健康监控与自动扩缩容。
//
// HealthMonitor 后台定期检查 pool 中所有实例的健康状态，
// 自动移除不健康的实例，并根据负载自动扩缩容。
// 这是实现 kimi 2.6 超越所需的弹性伸缩能力。
package kernel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// HealthCheckResult 描述单个实例的健康检查结果。
type HealthCheckResult struct {
	ID        string
	Kind      agent.Kind
	Healthy   bool
	Load      int64
	Latency   float64 // 秒
	LastUsed  time.Time
	Reason    string  // 如果不健康，说明原因
}

// PoolHealthPolicy 定义健康监控与自动扩缩容策略。
type PoolHealthPolicy struct {
	// CheckInterval 健康检查间隔。
	CheckInterval time.Duration

	// MaxLoadPerInstance 单个实例的最大负载，超过则触发扩容。
	MaxLoadPerInstance int64

	// MinInstancesPerKind 每个 kind 的最小实例数。
	MinInstancesPerKind int

	// MaxInstancesPerKind 每个 kind 的最大实例数。
	MaxInstancesPerKind int

	// IdleTimeout 实例空闲超过此时间后考虑缩容。
	IdleTimeout time.Duration

	// ScaleUpCooldown 扩容冷却时间，防止抖动。
	ScaleUpCooldown time.Duration

	// ScaleDownCooldown 缩容冷却时间，防止抖动。
	ScaleDownCooldown time.Duration
}

// DefaultPoolHealthPolicy 返回默认策略。
func DefaultPoolHealthPolicy() PoolHealthPolicy {
	return PoolHealthPolicy{
		CheckInterval:        30 * time.Second,
		MaxLoadPerInstance:   5,
		MinInstancesPerKind:  1,
		MaxInstancesPerKind:  5,
		IdleTimeout:          5 * time.Minute,
		ScaleUpCooldown:      60 * time.Second,
		ScaleDownCooldown:    120 * time.Second,
	}
}

// HealthMonitor 监控 BrainPool 的健康状态并执行自动扩缩容。
type HealthMonitor struct {
	pool   *ProcessBrainPool
	policy PoolHealthPolicy

	mu            sync.Mutex
	lastScaleUp   map[agent.Kind]time.Time
	lastScaleDown map[agent.Kind]time.Time
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewHealthMonitor 创建健康监控器。
func NewHealthMonitor(pool *ProcessBrainPool, policy PoolHealthPolicy) *HealthMonitor {
	return &HealthMonitor{
		pool:          pool,
		policy:        policy,
		lastScaleUp:   make(map[agent.Kind]time.Time),
		lastScaleDown: make(map[agent.Kind]time.Time),
		stopCh:        make(chan struct{}),
	}
}

// Start 启动后台健康监控循环。
func (h *HealthMonitor) Start(ctx context.Context) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(h.policy.CheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.stopCh:
				return
			case <-ticker.C:
				h.runCheck(ctx)
			}
		}
	}()
}

// Stop 停止健康监控。
func (h *HealthMonitor) Stop() {
	close(h.stopCh)
	h.wg.Wait()
}

// RunOnce 执行一次健康检查（用于测试）。
func (h *HealthMonitor) RunOnce(ctx context.Context) {
	h.runCheck(ctx)
}

func (h *HealthMonitor) runCheck(ctx context.Context) {
	h.pool.mu.Lock()
	// 收集所有需要检查的 kind
	kinds := make([]agent.Kind, 0, len(h.pool.active))
	for kind := range h.pool.active {
		kinds = append(kinds, kind)
	}
	h.pool.mu.Unlock()

	for _, kind := range kinds {
		h.checkKind(ctx, kind)
	}
}

func (h *HealthMonitor) checkKind(ctx context.Context, kind agent.Kind) {
	h.pool.mu.Lock()
	entries := make([]*poolEntry, len(h.pool.active[kind]))
	copy(entries, h.pool.active[kind])
	h.pool.mu.Unlock()

	var healthy []*poolEntry
	var unhealthy []*poolEntry

	for _, e := range entries {
		result := h.checkEntry(e)
		if result.Healthy {
			healthy = append(healthy, e)
		} else {
			unhealthy = append(unhealthy, e)
		}
	}

	// 1. 移除不健康实例
	for _, e := range unhealthy {
		fmt.Fprintf(os.Stderr, "health-monitor: removing unhealthy %s instance %s: %s\n", kind, e.id, "process exited or unresponsive")
		h.removeEntry(kind, e)
	}

	// 2. 计算平均负载
	avgLoad := float64(0)
	if len(healthy) > 0 {
		totalLoad := int64(0)
		for _, e := range healthy {
			totalLoad += e.CurrentLoad()
		}
		avgLoad = float64(totalLoad) / float64(len(healthy))
	}

	// 3. 扩容判断：平均负载超过阈值且未达到最大实例数
	if len(healthy) > 0 && avgLoad >= float64(h.policy.MaxLoadPerInstance) && len(healthy) < h.policy.MaxInstancesPerKind {
		h.mu.Lock()
		lastUp := h.lastScaleUp[kind]
		h.mu.Unlock()
		if time.Since(lastUp) > h.policy.ScaleUpCooldown {
			fmt.Fprintf(os.Stderr, "health-monitor: scaling up %s (%d instances, avg load %.1f)\n", kind, len(healthy), avgLoad)
			if err := h.pool.ScaleBrain(ctx, kind, len(healthy)+1); err != nil {
				fmt.Fprintf(os.Stderr, "health-monitor: scale up %s failed: %v\n", kind, err)
			} else {
				h.mu.Lock()
				h.lastScaleUp[kind] = time.Now()
				h.mu.Unlock()
			}
		}
	}

	// 4. 缩容判断：实例数超过最小值且有实例长时间空闲
	if len(healthy) > h.policy.MinInstancesPerKind {
		var idleEntries []*poolEntry
		for _, e := range healthy {
			if e.CurrentLoad() == 0 && time.Since(e.LastUsed()) > h.policy.IdleTimeout {
				idleEntries = append(idleEntries, e)
			}
		}
		if len(idleEntries) > 0 {
			h.mu.Lock()
			lastDown := h.lastScaleDown[kind]
			h.mu.Unlock()
			if time.Since(lastDown) > h.policy.ScaleDownCooldown {
				// 只缩容一个实例
				toRemove := idleEntries[0]
				fmt.Fprintf(os.Stderr, "health-monitor: scaling down %s instance %s (idle %s)\n", kind, toRemove.id, time.Since(toRemove.LastUsed()))
				h.removeEntry(kind, toRemove)
				h.mu.Lock()
				h.lastScaleDown[kind] = time.Now()
				h.mu.Unlock()
			}
		}
	}
}

func (h *HealthMonitor) checkEntry(e *poolEntry) HealthCheckResult {
	result := HealthCheckResult{
		ID:       e.id,
		Kind:     e.agent.Kind(),
		Load:     e.CurrentLoad(),
		Latency:  e.LatencyEWMA(),
		LastUsed: e.LastUsed(),
		Healthy:  true,
	}

	// 检查进程是否存活
	if !h.pool.isAlive(e.agent) {
		result.Healthy = false
		result.Reason = "process exited"
		return result
	}

	return result
}

func (h *HealthMonitor) removeEntry(kind agent.Kind, e *poolEntry) {
	h.pool.mu.Lock()
	entries := h.pool.active[kind]
	var newEntries []*poolEntry
	for _, entry := range entries {
		if entry != e {
			newEntries = append(newEntries, entry)
		}
	}
	h.pool.active[kind] = newEntries
	h.pool.mu.Unlock()

	// 关闭实例
	if e != nil && e.agent != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdowner, ok := e.agent.(interface{ Shutdown(context.Context) error }); ok {
			shutdowner.Shutdown(shutCtx)
		}
		if h.pool.runner != nil {
			h.pool.runner.Stop(shutCtx, kind)
		}
	}
}
