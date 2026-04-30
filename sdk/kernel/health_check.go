// Package kernel — 组件级健康检查框架。
//
// HealthManager 提供组件级健康监控、聚合报告和自愈触发能力，
// 是 MACCS Wave 6 生产级硬化的核心基础设施。
package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ────────────────────── HealthStatus 枚举 ──────────────────────

// HealthStatus 表示组件健康状态。
type HealthStatus string

const (
	HealthOK       HealthStatus = "ok"
	HealthDegraded HealthStatus = "degraded"
	HealthDown     HealthStatus = "down"
	HealthUnknown  HealthStatus = "unknown"
)

// ────────────────────── ComponentHealth ──────────────────────

// ComponentHealth 描述单个组件的健康状态快照。
type ComponentHealth struct {
	Component   string            `json:"component"`
	Status      HealthStatus      `json:"status"`
	Message     string            `json:"message,omitempty"`
	Latency     time.Duration     `json:"latency"`
	LastCheckAt time.Time         `json:"last_check_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ────────────────────── HealthReport ──────────────────────

// HealthReport 聚合所有组件的健康状态。
type HealthReport struct {
	ReportID     string            `json:"report_id"`
	Overall      HealthStatus      `json:"overall"`
	Components   []ComponentHealth `json:"components"`
	HealthyCount int               `json:"healthy_count"`
	TotalCount   int               `json:"total_count"`
	Uptime       time.Duration     `json:"uptime"`
	CheckedAt    time.Time         `json:"checked_at"`
}

// ────────────────────── HealthChecker 接口 ──────────────────────

// HealthChecker 是单组件的健康检查器接口。
type HealthChecker interface {
	Name() string
	Check(ctx context.Context) ComponentHealth
}

// ────────────────────── SelfHealAction ──────────────────────

// SelfHealAction 记录一次自愈动作的执行信息。
type SelfHealAction struct {
	Component string    `json:"component"`
	Action    string    `json:"action"` // restart/reconnect/reset/escalate
	Reason    string    `json:"reason"`
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}

// ────────────────────── HealthManager ──────────────────────

// HealthManager 管理所有组件的健康检查、聚合报告和自愈触发。
type HealthManager struct {
	mu          sync.RWMutex
	checkers    []HealthChecker
	lastReport  *HealthReport
	healActions []SelfHealAction
	startTime   time.Time
	onHeal      func(action SelfHealAction)
	reportSeq   uint64
}

// NewHealthManager 创建一个新的 HealthManager。
func NewHealthManager() *HealthManager {
	return &HealthManager{
		startTime: time.Now(),
	}
}

// Register 注册一个组件健康检查器。
func (hm *HealthManager) Register(checker HealthChecker) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.checkers = append(hm.checkers, checker)
}

// CheckAll 执行所有已注册检查器并聚合为统一报告。
//
// 聚合规则：
//   - 所有 ok → overall = ok
//   - 有 degraded 无 down → overall = degraded
//   - 有 down → overall = down
func (hm *HealthManager) CheckAll(ctx context.Context) *HealthReport {
	hm.mu.RLock()
	checkers := make([]HealthChecker, len(hm.checkers))
	copy(checkers, hm.checkers)
	hm.mu.RUnlock()

	now := time.Now()
	components := make([]ComponentHealth, 0, len(checkers))
	healthyCount := 0
	hasDown := false
	hasDegraded := false

	for _, c := range checkers {
		start := time.Now()
		ch := c.Check(ctx)
		if ch.Latency == 0 {
			ch.Latency = time.Since(start)
		}
		if ch.LastCheckAt.IsZero() {
			ch.LastCheckAt = now
		}
		if ch.Component == "" {
			ch.Component = c.Name()
		}
		components = append(components, ch)

		switch ch.Status {
		case HealthOK:
			healthyCount++
		case HealthDegraded:
			hasDegraded = true
		case HealthDown:
			hasDown = true
		}
	}

	overall := HealthOK
	if hasDegraded {
		overall = HealthDegraded
	}
	if hasDown {
		overall = HealthDown
	}

	hm.mu.Lock()
	hm.reportSeq++
	report := &HealthReport{
		ReportID:     fmt.Sprintf("hr-%d", hm.reportSeq),
		Overall:      overall,
		Components:   components,
		HealthyCount: healthyCount,
		TotalCount:   len(components),
		Uptime:       time.Since(hm.startTime),
		CheckedAt:    now,
	}
	hm.lastReport = report
	hm.mu.Unlock()

	return report
}

// GetLastReport 返回最近一次健康报告，如果尚未执行过检查则返回 nil。
func (hm *HealthManager) GetLastReport() *HealthReport {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return hm.lastReport
}

// TriggerHeal 触发一次自愈动作，并通过回调通知外部。
func (hm *HealthManager) TriggerHeal(component, action, reason string) *SelfHealAction {
	heal := SelfHealAction{
		Component: component,
		Action:    action,
		Reason:    reason,
		Success:   true,
		Timestamp: time.Now(),
	}

	hm.mu.Lock()
	hm.healActions = append(hm.healActions, heal)
	cb := hm.onHeal
	hm.mu.Unlock()

	if cb != nil {
		cb(heal)
	}
	return &heal
}

// SetHealCallback 设置自愈动作的回调函数。
func (hm *HealthManager) SetHealCallback(fn func(SelfHealAction)) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.onHeal = fn
}

// GetHealHistory 返回所有历史自愈动作记录。
func (hm *HealthManager) GetHealHistory() []SelfHealAction {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	out := make([]SelfHealAction, len(hm.healActions))
	copy(out, hm.healActions)
	return out
}

// IsHealthy 快速判断系统是否健康（基于最近一次报告）。
// 如果尚未执行过检查，默认返回 true。
func (hm *HealthManager) IsHealthy() bool {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	if hm.lastReport == nil {
		return true
	}
	return hm.lastReport.Overall == HealthOK
}

// Uptime 返回 HealthManager 自创建以来的运行时间。
func (hm *HealthManager) Uptime() time.Duration {
	return time.Since(hm.startTime)
}

// ────────────────────── FuncHealthChecker ──────────────────────

// FuncHealthChecker 使用闭包实现 HealthChecker，方便注册简单检查器。
type FuncHealthChecker struct {
	name    string
	checkFn func(ctx context.Context) ComponentHealth
}

// NewFuncHealthChecker 创建一个基于闭包的健康检查器。
func NewFuncHealthChecker(name string, fn func(ctx context.Context) ComponentHealth) *FuncHealthChecker {
	return &FuncHealthChecker{name: name, checkFn: fn}
}

// Name 返回检查器名称。
func (f *FuncHealthChecker) Name() string { return f.name }

// Check 执行健康检查。
func (f *FuncHealthChecker) Check(ctx context.Context) ComponentHealth {
	return f.checkFn(ctx)
}
