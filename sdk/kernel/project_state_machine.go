package kernel

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// SMPhase — 状态机阶段常量（string 避免与 ProjectPhaseType 冲突）
// ---------------------------------------------------------------------------

const (
	SMPhaseRequirement = "requirement"   // 需求解析
	SMPhaseDesign      = "design"        // 方案设计
	SMPhaseReview      = "review"        // 方案审核
	SMPhaseExecution   = "execution"     // 任务执行
	SMPhaseAcceptance  = "acceptance"    // 验收测试
	SMPhaseDelivery    = "delivery"      // 交付生成
	SMPhaseRetrospect  = "retrospective" // 复盘学习
	SMPhaseCompleted   = "completed"     // 终态：完成
	SMPhaseFailed      = "failed"        // 终态：失败
)

// smPhaseSequence 定义正向阶段顺序，用于 skip 计算下一阶段。
var smPhaseSequence = []string{
	SMPhaseRequirement, SMPhaseDesign, SMPhaseReview,
	SMPhaseExecution, SMPhaseAcceptance, SMPhaseDelivery, SMPhaseRetrospect,
}

// smPhaseIdx 快速查找阶段在序列中的位置。
var smPhaseIdx = func() map[string]int {
	m := make(map[string]int, len(smPhaseSequence))
	for i, p := range smPhaseSequence {
		m[p] = i
	}
	return m
}()

// ---------------------------------------------------------------------------
// PhaseTransitionLog — 阶段流转日志
// ---------------------------------------------------------------------------

// PhaseTransitionLog 记录一次阶段流转，包括来源、目标、事件和原因。
type PhaseTransitionLog struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Event     string    `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// ProjectSMContext — 状态机上下文
// ---------------------------------------------------------------------------

// ProjectSMContext 保存状态机的全部运行时状态，包含当前阶段、流转历史、
// 共享数据和每阶段重试计数。
type ProjectSMContext struct {
	SessionID    string                 `json:"session_id"`
	CurrentPhase string                 `json:"current_phase"`
	PhaseHistory []PhaseTransitionLog   `json:"phase_history"`
	Data         map[string]interface{} `json:"data"`        // 跨阶段共享数据
	RetryCount   map[string]int         `json:"retry_count"` // 每阶段重试次数
	MaxRetries   int                    `json:"max_retries"` // 默认 3
}

// ---------------------------------------------------------------------------
// ProjectTransition — 状态转移定义
// ---------------------------------------------------------------------------

// ProjectTransition 描述一条合法的状态转移边：从 From 经 Event 到 To，
// 可选的 GuardFunc 在转移前做前置校验。
type ProjectTransition struct {
	From      string                         `json:"from"`
	To        string                         `json:"to"`
	Event     string                         `json:"event"` // advance/fail/retry/skip/rollback
	GuardFunc func(ctx *ProjectSMContext) bool `json:"-"`    // 守卫条件
}

// ---------------------------------------------------------------------------
// ProjectSMHooks — 状态机钩子
// ---------------------------------------------------------------------------

// ProjectSMHooks 提供阶段进入、离开、失败三个生命周期钩子。
type ProjectSMHooks struct {
	OnEnter func(phase string)            // 进入阶段时触发
	OnExit  func(phase string)            // 离开阶段时触发
	OnFail  func(phase string, err error) // 阶段失败时触发
}

// ---------------------------------------------------------------------------
// ProjectStateMachine — 状态机核心
// ---------------------------------------------------------------------------

// ProjectStateMachine 管理 7 阶段项目的状态流转、守卫条件和回退支持。
// 线程安全，所有公开方法均通过 RWMutex 保护。
type ProjectStateMachine struct {
	mu          sync.RWMutex
	transitions []ProjectTransition
	ctx         *ProjectSMContext
	hooks       ProjectSMHooks
}

// ---------------------------------------------------------------------------
// 守卫条件工厂
// ---------------------------------------------------------------------------

// advanceGuard 检查当前阶段是否已完成（Data["phase_status"] == "done"）。
func advanceGuard(ctx *ProjectSMContext) bool {
	v, ok := ctx.Data["phase_status"]
	if !ok {
		return false
	}
	s, _ := v.(string)
	return s == "done"
}

// rollbackGuard 检查重试次数未耗尽。
func rollbackGuard(ctx *ProjectSMContext) bool {
	used := ctx.RetryCount[ctx.CurrentPhase]
	return used < ctx.MaxRetries
}

// retryGuard 检查当前处于 failed 且重试次数未耗尽。
func retryGuard(ctx *ProjectSMContext) bool {
	if ctx.CurrentPhase != SMPhaseFailed {
		return false
	}
	// 查最近一条历史，确定 failed 前的阶段
	if len(ctx.PhaseHistory) == 0 {
		return false
	}
	prev := ctx.PhaseHistory[len(ctx.PhaseHistory)-1].From
	used := ctx.RetryCount[prev]
	return used < ctx.MaxRetries
}

// ---------------------------------------------------------------------------
// 默认转移规则
// ---------------------------------------------------------------------------

func defaultTransitions() []ProjectTransition {
	tt := []ProjectTransition{
		// 正向 advance
		{From: SMPhaseRequirement, To: SMPhaseDesign, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseDesign, To: SMPhaseReview, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseReview, To: SMPhaseExecution, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseExecution, To: SMPhaseAcceptance, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseAcceptance, To: SMPhaseDelivery, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseDelivery, To: SMPhaseRetrospect, Event: "advance", GuardFunc: advanceGuard},
		{From: SMPhaseRetrospect, To: SMPhaseCompleted, Event: "advance", GuardFunc: advanceGuard},

		// 回退 rollback
		{From: SMPhaseReview, To: SMPhaseDesign, Event: "rollback", GuardFunc: rollbackGuard},
		{From: SMPhaseExecution, To: SMPhaseDesign, Event: "rollback", GuardFunc: rollbackGuard},
		{From: SMPhaseAcceptance, To: SMPhaseExecution, Event: "rollback", GuardFunc: rollbackGuard},

		// 失败 → failed（从所有非终态阶段）
		{From: SMPhaseRequirement, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseDesign, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseReview, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseExecution, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseAcceptance, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseDelivery, To: SMPhaseFailed, Event: "fail"},
		{From: SMPhaseRetrospect, To: SMPhaseFailed, Event: "fail"},

		// 从 failed 重试回前一阶段
		{From: SMPhaseFailed, To: "", Event: "retry", GuardFunc: retryGuard},
	}

	// skip：从每个非终态阶段跳到下一个阶段
	for i := 0; i < len(smPhaseSequence)-1; i++ {
		tt = append(tt, ProjectTransition{
			From:  smPhaseSequence[i],
			To:    smPhaseSequence[i+1],
			Event: "skip",
		})
	}
	// 最后一个阶段 skip 直接到 completed
	tt = append(tt, ProjectTransition{
		From:  smPhaseSequence[len(smPhaseSequence)-1],
		To:    SMPhaseCompleted,
		Event: "skip",
	})

	return tt
}

// ---------------------------------------------------------------------------
// 构造函数
// ---------------------------------------------------------------------------

// NewProjectStateMachine 创建项目状态机，使用默认 maxRetries=3 和空钩子。
func NewProjectStateMachine(sessionID string) *ProjectStateMachine {
	return NewProjectStateMachineWithConfig(sessionID, 3, ProjectSMHooks{})
}

// NewProjectStateMachineWithConfig 创建项目状态机，支持自定义重试上限和钩子。
func NewProjectStateMachineWithConfig(sessionID string, maxRetries int, hooks ProjectSMHooks) *ProjectStateMachine {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	return &ProjectStateMachine{
		transitions: defaultTransitions(),
		ctx: &ProjectSMContext{
			SessionID:    sessionID,
			CurrentPhase: SMPhaseRequirement,
			PhaseHistory: make([]PhaseTransitionLog, 0),
			Data:         make(map[string]interface{}),
			RetryCount:   make(map[string]int),
			MaxRetries:   maxRetries,
		},
		hooks: hooks,
	}
}

// ---------------------------------------------------------------------------
// 公开方法
// ---------------------------------------------------------------------------

// Fire 触发事件，执行状态转移。
func (sm *ProjectStateMachine) Fire(event string) error {
	return sm.FireWithReason(event, "")
}

// FireWithReason 带原因的状态转移，是状态机的核心驱动方法。
func (sm *ProjectStateMachine) FireWithReason(event, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cur := sm.ctx.CurrentPhase

	// retry 特殊处理：回到 failed 前的阶段
	if event == "retry" && cur == SMPhaseFailed {
		return sm.handleRetry(reason)
	}

	// 查找匹配的转移
	for _, t := range sm.transitions {
		if t.From != cur || t.Event != event {
			continue
		}
		// 检查守卫条件
		if t.GuardFunc != nil && !t.GuardFunc(sm.ctx) {
			return fmt.Errorf("守卫条件不满足: 事件 %s 从 %s 到 %s", event, cur, t.To)
		}
		return sm.applyTransition(cur, t.To, event, reason)
	}
	return fmt.Errorf("无匹配转移: 当前阶段 %s, 事件 %s", cur, event)
}

// CanFire 检查是否可以触发指定事件。
func (sm *ProjectStateMachine) CanFire(event string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	cur := sm.ctx.CurrentPhase

	// retry 特殊判断
	if event == "retry" && cur == SMPhaseFailed {
		return retryGuard(sm.ctx)
	}

	for _, t := range sm.transitions {
		if t.From != cur || t.Event != event {
			continue
		}
		if t.GuardFunc != nil && !t.GuardFunc(sm.ctx) {
			continue
		}
		return true
	}
	return false
}

// CurrentPhase 获取当前阶段。
func (sm *ProjectStateMachine) CurrentPhase() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.ctx.CurrentPhase
}

// History 获取完整的转移历史。
func (sm *ProjectStateMachine) History() []PhaseTransitionLog {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]PhaseTransitionLog, len(sm.ctx.PhaseHistory))
	copy(out, sm.ctx.PhaseHistory)
	return out
}

// IsTerminal 判断是否处于终态（completed 或 failed）。
func (sm *ProjectStateMachine) IsTerminal() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.ctx.CurrentPhase == SMPhaseCompleted || sm.ctx.CurrentPhase == SMPhaseFailed
}

// SetData 设置共享数据。
func (sm *ProjectStateMachine) SetData(key string, value interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ctx.Data[key] = value
}

// GetData 获取共享数据。
func (sm *ProjectStateMachine) GetData(key string) (interface{}, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	v, ok := sm.ctx.Data[key]
	return v, ok
}

// RetriesRemaining 返回指定阶段的剩余重试次数。
func (sm *ProjectStateMachine) RetriesRemaining(phase string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	used := sm.ctx.RetryCount[phase]
	rem := sm.ctx.MaxRetries - used
	if rem < 0 {
		return 0
	}
	return rem
}

// Snapshot 返回上下文的深拷贝，可安全用于 JSON 序列化。
func (sm *ProjectStateMachine) Snapshot() *ProjectSMContext {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	snap := &ProjectSMContext{
		SessionID:    sm.ctx.SessionID,
		CurrentPhase: sm.ctx.CurrentPhase,
		MaxRetries:   sm.ctx.MaxRetries,
	}

	snap.PhaseHistory = make([]PhaseTransitionLog, len(sm.ctx.PhaseHistory))
	copy(snap.PhaseHistory, sm.ctx.PhaseHistory)

	snap.Data = make(map[string]interface{}, len(sm.ctx.Data))
	for k, v := range sm.ctx.Data {
		snap.Data[k] = v
	}

	snap.RetryCount = make(map[string]int, len(sm.ctx.RetryCount))
	for k, v := range sm.ctx.RetryCount {
		snap.RetryCount[k] = v
	}

	return snap
}

// ---------------------------------------------------------------------------
// 内部方法（调用方必须持有写锁）
// ---------------------------------------------------------------------------

// applyTransition 执行一次状态转移：记录日志、触发钩子、更新当前阶段。
func (sm *ProjectStateMachine) applyTransition(from, to, event, reason string) error {
	// 触发 OnExit 钩子
	if sm.hooks.OnExit != nil {
		sm.hooks.OnExit(from)
	}

	// 如果是 fail 事件，触发 OnFail 钩子
	if event == "fail" && sm.hooks.OnFail != nil {
		sm.hooks.OnFail(from, fmt.Errorf("阶段 %s 失败: %s", from, reason))
	}

	// 记录转移日志
	sm.ctx.PhaseHistory = append(sm.ctx.PhaseHistory, PhaseTransitionLog{
		From:      from,
		To:        to,
		Event:     event,
		Timestamp: time.Now(),
		Reason:    reason,
	})

	// 更新当前阶段
	sm.ctx.CurrentPhase = to

	// 进入新阶段时清除 phase_status，为下一次 advance 做准备
	if event == "advance" || event == "rollback" || event == "retry" || event == "skip" {
		delete(sm.ctx.Data, "phase_status")
	}

	// 触发 OnEnter 钩子
	if sm.hooks.OnEnter != nil {
		sm.hooks.OnEnter(to)
	}

	return nil
}

// handleRetry 处理 failed 状态的重试：回到失败前的阶段，增加重试计数。
func (sm *ProjectStateMachine) handleRetry(reason string) error {
	if len(sm.ctx.PhaseHistory) == 0 {
		return fmt.Errorf("无转移历史，无法重试")
	}

	// 查找最近一次进入 failed 的记录，取其来源阶段
	prevPhase := sm.ctx.PhaseHistory[len(sm.ctx.PhaseHistory)-1].From

	// 检查重试次数
	if sm.ctx.RetryCount[prevPhase] >= sm.ctx.MaxRetries {
		return fmt.Errorf("阶段 %s 重试次数已耗尽（%d/%d）", prevPhase, sm.ctx.RetryCount[prevPhase], sm.ctx.MaxRetries)
	}

	// 增加重试计数
	sm.ctx.RetryCount[prevPhase]++

	return sm.applyTransition(SMPhaseFailed, prevPhase, "retry", reason)
}
