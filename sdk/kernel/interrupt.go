package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/loop"
)

// ---------------------------------------------------------------------------
// InterruptType — 中断类型枚举
// ---------------------------------------------------------------------------

type InterruptType string

const (
	InterruptPlanChanged      InterruptType = "plan_changed"
	InterruptEmergencyStop    InterruptType = "emergency_stop"
	InterruptPriorityOverride InterruptType = "priority_override"
	InterruptDependencyChange InterruptType = "dependency_change"
	InterruptBudgetExhausted  InterruptType = "budget_exhausted"
)

// ---------------------------------------------------------------------------
// InterruptAction — 中断后的动作
// ---------------------------------------------------------------------------

type InterruptAction string

const (
	InterruptActionPause   InterruptAction = "pause"
	InterruptActionStop    InterruptAction = "stop"
	InterruptActionRestart InterruptAction = "restart"
)

// ---------------------------------------------------------------------------
// InterruptSignal — 中断信号
// ---------------------------------------------------------------------------

// InterruptSignal 表示中央大脑发出的中断信号。
// 类比交响乐团：指挥举手示意乐手在当前小节结束后停下。
type InterruptSignal struct {
	SignalID      string          `json:"signal_id"`
	Type          InterruptType   `json:"type"`
	AffectedTasks []string        `json:"affected_tasks"` // 空表示全部
	Action        InterruptAction `json:"action"`
	Reason        string          `json:"reason"`
	IssuedAt      time.Time       `json:"issued_at"`
	IssuedBy      string          `json:"issued_by"` // "central"/"user"/"system"
}

// NewInterruptSignal 创建带时间戳的中断信号。
func NewInterruptSignal(typ InterruptType, action InterruptAction, reason, issuedBy string) InterruptSignal {
	return InterruptSignal{
		SignalID: fmt.Sprintf("int-%d", time.Now().UnixNano()),
		Type:     typ,
		Action:   action,
		Reason:   reason,
		IssuedAt: time.Now(),
		IssuedBy: issuedBy,
	}
}

// ---------------------------------------------------------------------------
// InterruptChecker — 中断检查接口
// ---------------------------------------------------------------------------

// InterruptChecker 提供中断信号的检查、发送和清除能力。
// Runner 在每 turn 开始前调用 Check 检查是否有中断信号。
type InterruptChecker interface {
	Check(ctx context.Context, runID string) *InterruptSignal
	Send(ctx context.Context, signal InterruptSignal) error
	Clear(ctx context.Context, runID string) error
}

// ---------------------------------------------------------------------------
// MemInterruptChecker — 基于内存的中断检查器
// ---------------------------------------------------------------------------

// MemInterruptChecker 使用 sync.RWMutex + map 实现 InterruptChecker。
// 适用于单进程场景；分布式场景可替换为 Redis/SQLite 实现。
type MemInterruptChecker struct {
	mu      sync.RWMutex
	signals map[string]*InterruptSignal // runID -> signal
}

// NewMemInterruptChecker 创建基于内存的中断检查器。
func NewMemInterruptChecker() *MemInterruptChecker {
	return &MemInterruptChecker{
		signals: make(map[string]*InterruptSignal),
	}
}

// Check 查找 runID 对应的中断信号。找到则返回并清除，未找到返回 nil。
func (m *MemInterruptChecker) Check(_ context.Context, runID string) *InterruptSignal {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 优先检查精确 runID
	if sig, ok := m.signals[runID]; ok {
		delete(m.signals, runID)
		return sig
	}
	// 检查全局广播信号
	if sig, ok := m.signals["__all__"]; ok {
		// 全局信号不在此处删除，由 Clear 统一清理
		cp := *sig
		return &cp
	}
	return nil
}

// Send 存储中断信号。如果 AffectedTasks 不为空，为每个 taskID 都存一份；
// 如果为空则用特殊 key "__all__" 表示广播。
func (m *MemInterruptChecker) Send(_ context.Context, signal InterruptSignal) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(signal.AffectedTasks) == 0 {
		cp := signal
		m.signals["__all__"] = &cp
		return nil
	}
	for _, taskID := range signal.AffectedTasks {
		cp := signal
		m.signals[taskID] = &cp
	}
	return nil
}

// Clear 删除 runID 对应的中断信号。
func (m *MemInterruptChecker) Clear(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.signals, runID)
	return nil
}

// ---------------------------------------------------------------------------
// KernelInterruptCheckerAdapter — 把 kernel.InterruptChecker 适配为 loop.RunInterruptChecker
// ---------------------------------------------------------------------------

// KernelInterruptCheckerAdapter 让 Runner（loop 包）可以使用 kernel 层的 InterruptChecker，
// 同时避免 loop → kernel 的循环依赖（kernel → loop 是允许的单向依赖）。
//
// 字段映射契约：
//   - SignalID → SignalID（直接复制）
//   - InterruptType（kernel）→ string Type（loop）
//   - InterruptAction（kernel）→ string Action（loop）
//   - Reason → Reason
//   - IssuedAt 不暴露给 loop 端，避免泄漏内部字段
type KernelInterruptCheckerAdapter struct {
	checker InterruptChecker
}

// NewKernelInterruptCheckerAdapter 构造一个适配器。
// 当 checker 为 nil 时，CheckInterrupt 始终返回 nil（视作无中断），
// 这与 Runner.InterruptChecker 字段为 nil 时的语义一致。
func NewKernelInterruptCheckerAdapter(checker InterruptChecker) loop.RunInterruptChecker {
	return &KernelInterruptCheckerAdapter{checker: checker}
}

// CheckInterrupt 实现 loop.RunInterruptChecker 接口。
// 调用底层 InterruptChecker.Check，把 *kernel.InterruptSignal 转成 *loop.RunInterruptSignal。
func (a *KernelInterruptCheckerAdapter) CheckInterrupt(ctx context.Context, runID string) *loop.RunInterruptSignal {
	if a == nil || a.checker == nil {
		return nil
	}
	sig := a.checker.Check(ctx, runID)
	if sig == nil {
		return nil
	}
	return &loop.RunInterruptSignal{
		SignalID: sig.SignalID,
		Type:     string(sig.Type),
		Action:   string(sig.Action),
		Reason:   sig.Reason,
	}
}
