// feedback_controller.go — 反馈伺服控制器（Feedback Controller）
//
// 钱学森《工程控制论》主线2（反馈伺服）的落地载体。
// FeedbackController 将 SystemState 与 PerformanceSpec 持续比较，
// 当检测到性能差距时生成 ControlAction，驱动系统回到期望轨道。
//
// 控制逻辑采用 PI（比例-积分）策略：
//   P 项：对当前偏差的即时响应（快速抑制越界）
//   I 项：对累积偏差的持续修正（消除稳态误差）
package kernel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// ControlAction — 控制动作
// ---------------------------------------------------------------------------

// ControlAction 描述 FeedbackController 生成的具体控制指令。
type ControlAction struct {
	TargetKind string    `json:"target_kind"` // 目标 brain kind，空字符串表示全局
	Action     string    `json:"action"`      // 动作类型
	Priority   int       `json:"priority"`    // 优先级（1-10，10 最高）
	Reason     string    `json:"reason"`      // 人类可读原因
	Timestamp  time.Time `json:"timestamp"`
}

// 预定义控制动作常量
const (
	ActionThrottle = "throttle" // 限流：降低该 brain 的任务分配速率
	ActionBoost    = "boost"    // 增压：提高该 brain 的任务分配权重
	ActionRestart  = "restart"  // 重启：重启该 brain 的 sidecar 进程
	ActionReroute  = "reroute"  // 重路由：将该 brain 的任务路由到其他 brain
	ActionAlert    = "alert"    // 告警：仅记录，不执行物理动作
	ActionNoOp     = "noop"     // 无动作：系统处于期望范围内
)

// ---------------------------------------------------------------------------
// FeedbackController — 反馈控制器接口
// ---------------------------------------------------------------------------

// FeedbackController 是反馈控制的核心接口。
type FeedbackController interface {
	// Evaluate 对当前系统状态做一次评估，返回建议的控制动作列表。
	Evaluate(state *SystemState) []ControlAction
	// LastActions 返回最近一次评估生成的动作（供外部查询或执行）。
	LastActions() []ControlAction
	// Start 启动后台控制循环。
	Start(ctx context.Context)
	// Stop 停止后台控制循环。
	Stop()
}

// ---------------------------------------------------------------------------
// MemFeedbackController — 内存反馈控制器
// ---------------------------------------------------------------------------

// MemFeedbackController 是基于 PI 控制律的反馈控制器实现。
type MemFeedbackController struct {
	spec      *PerformanceSpec
	observer  StateObserver
	synth     *ControlLawSynthesizer
	coupling  *CouplingMatrix // 可选：用于副作用预判和在线学习

	mu           sync.RWMutex
	lastActions  []ControlAction
	lastState    *SystemState

	// PI 控制状态
	integralErr map[string]float64 // 各指标的积分累积误差
	lastErr     map[string]float64 // 上次偏差（用于微分，当前版本仅 PI）
	lastActionAt map[string]time.Time // 各 brain 上次执行控制动作的时间（防抖）
	consecutiveCount map[string]int // 各指标连续越界次数（防抖）

	interval time.Duration
	stopCh   chan struct{}
}

// NewMemFeedbackController 创建内存反馈控制器。
// observer 为 nil 时 Evaluate 仍可手动调用，但 Start() 会退化为 no-op。
func NewMemFeedbackController(spec *PerformanceSpec, observer StateObserver) *MemFeedbackController {
	if spec == nil {
		spec = DefaultPerformanceSpec()
	}
	return &MemFeedbackController{
		spec:             spec,
		observer:         observer,
		synth:            NewControlLawSynthesizer(),
		integralErr:      make(map[string]float64),
		lastErr:          make(map[string]float64),
		lastActionAt:     make(map[string]time.Time),
		consecutiveCount: make(map[string]int),
		interval:         30 * time.Second,
		stopCh:           make(chan struct{}),
	}
}

// SetCouplingMatrix 设置耦合矩阵，用于控制动作副作用预判和在线学习。
func (fc *MemFeedbackController) SetCouplingMatrix(cm *CouplingMatrix) {
	fc.coupling = cm
}

// Evaluate 对传入状态做一次控制评估。
func (fc *MemFeedbackController) Evaluate(state *SystemState) []ControlAction {
	if state == nil {
		return nil
	}
	gaps := fc.spec.Check(state)
	var actions []ControlAction
	now := time.Now().UTC()

	for _, gap := range gaps {
		if gap.Severity == "ok" {
			continue
		}
		key := gapKey(gap)

		// 更新 PI 状态
		fc.mu.Lock()
		fc.integralErr[key] += gap.Delta
		if fc.integralErr[key] < 0 {
			fc.integralErr[key] = 0 // 防止负向积分饱和（anti-windup 简化版）
		}
		const maxIntegralErr = 5.0 // 防止积分 windup 导致控制量无限增长
		if fc.integralErr[key] > maxIntegralErr {
			fc.integralErr[key] = maxIntegralErr
		}
		lastE := fc.lastErr[key]
		fc.lastErr[key] = gap.Delta
		lastAct := fc.lastActionAt[gap.Kind]
		fc.mu.Unlock()

		// 防抖：同一 brain 在 Cooldown 内不重复触发
		if !lastAct.IsZero() && now.Sub(lastAct) < fc.spec.Cooldown {
			continue
		}

		// 连续越界检查：必须连续 N 次越界才触发（N = spec.Consecutive）
		fc.mu.Lock()
		fc.consecutiveCount[key]++
		count := fc.consecutiveCount[key]
		fc.mu.Unlock()
		if count < fc.spec.Consecutive {
			continue
		}

		action := fc.synth.Synthesize(gap, state, fc.integralErr[key], lastE)
		if action.Action != ActionNoOp {
			actions = append(actions, action)
			fc.mu.Lock()
			fc.lastActionAt[gap.Kind] = now
			// 触发后重置连续计数，防止同一问题无限触发
			fc.consecutiveCount[key] = 0
			fc.mu.Unlock()
		}
	}

	// 重置本次未越界指标的连续计数
	fc.mu.Lock()
	presentKeys := make(map[string]struct{}, len(gaps))
	for _, g := range gaps {
		presentKeys[gapKey(g)] = struct{}{}
	}
	for k := range fc.consecutiveCount {
		if _, ok := presentKeys[k]; !ok {
			fc.consecutiveCount[k] = 0
		}
	}
	fc.lastActions = actions
	fc.lastState = state.Clone()
	fc.mu.Unlock()
	return actions
}

// LastActions 返回最近一次评估生成的动作。
func (fc *MemFeedbackController) LastActions() []ControlAction {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	out := make([]ControlAction, len(fc.lastActions))
	copy(out, fc.lastActions)
	return out
}

// Start 启动后台控制循环（每 interval 评估一次）。
func (fc *MemFeedbackController) Start(ctx context.Context) {
	if fc.observer == nil {
		return
	}
	ticker := time.NewTicker(fc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fc.stopCh:
			return
		case <-ticker.C:
			state := fc.observer.Snapshot()
			actions := fc.Evaluate(state)
			if len(actions) > 0 {
				for _, a := range actions {
					fmt.Fprintf(os.Stderr, "[feedback] %s kind=%s priority=%d reason=%s\n",
						a.Action, a.TargetKind, a.Priority, a.Reason)
				}
			}
			// 在线耦合学习：将控制动作与实际状态变化反馈给 CouplingMatrix
			if fc.coupling != nil {
				fc.coupling.Update(state, actions)
			}
		}
	}
}

// Stop 停止后台控制循环。
func (fc *MemFeedbackController) Stop() {
	close(fc.stopCh)
}

// gapKey 为 PerformanceGap 生成唯一键，用于 PI 状态索引。
func gapKey(gap PerformanceGap) string {
	if gap.Kind != "" {
		return gap.Kind + "/" + gap.Metric
	}
	return "global/" + gap.Metric
}

// ---------------------------------------------------------------------------
// NopFeedbackController — 空反馈控制器（可选注入时的 no-op 实现）
// ---------------------------------------------------------------------------

// NopFeedbackController 是不执行任何动作的 FeedbackController 实现，
// 用于当用户不需要反馈控制时保持接口兼容性。
type NopFeedbackController struct{}

func (n *NopFeedbackController) Evaluate(_ *SystemState) []ControlAction { return nil }
func (n *NopFeedbackController) LastActions() []ControlAction            { return nil }
func (n *NopFeedbackController) Start(_ context.Context)                 {}
func (n *NopFeedbackController) Stop()                                   {}

var _ FeedbackController = (*NopFeedbackController)(nil)

// ---------------------------------------------------------------------------
// SimpleFeedbackController — 基于阈值规则的轻量反馈控制器
// ---------------------------------------------------------------------------

// SimpleFeedbackController 用硬阈值生成控制动作，无需复杂的 PI 调参。
// 规则：
//   - 延迟 > 5s 或错误率 > 20% → throttle
//   - 延迟 > 10s 或错误率 > 50% → restart
//   - 连续 3 次评估越界 → alert
//   - 一切正常 → noop
type SimpleFeedbackController struct {
	mu        sync.Mutex
	last      []ControlAction
	violation map[string]int // gapKey → 连续越界次数
}

func NewSimpleFeedbackController() *SimpleFeedbackController {
	return &SimpleFeedbackController{violation: make(map[string]int)}
}

func (sfc *SimpleFeedbackController) Evaluate(state *SystemState) []ControlAction {
	if state == nil {
		return nil
	}
	var actions []ControlAction
	now := time.Now().UTC()
	for kind, bus := range state.Brains {
		action := sfc.evalBrain(kind, bus)
		if action.Action != ActionNoOp {
			action.Timestamp = now
			actions = append(actions, action)
		}
	}
	sfc.mu.Lock()
	sfc.last = actions
	sfc.mu.Unlock()
	return actions
}

func (sfc *SimpleFeedbackController) evalBrain(kind agent.Kind, bus BrainUnitState) ControlAction {
	action := ControlAction{TargetKind: string(kind), Action: ActionNoOp, Reason: "within spec"}
	key := string(kind)

	latencySec := bus.AvgLatencyMs / 1000.0
	if latencySec > 10.0 {
		action = ControlAction{TargetKind: string(kind), Action: ActionRestart, Priority: 9, Reason: fmt.Sprintf("latency %.1fs > 10s", latencySec)}
	} else if latencySec > 5.0 {
		action = ControlAction{TargetKind: string(kind), Action: ActionThrottle, Priority: 7, Reason: fmt.Sprintf("latency %.1fs > 5s", latencySec)}
	} else if bus.ErrorRate > 0.5 {
		action = ControlAction{TargetKind: string(kind), Action: ActionRestart, Priority: 9, Reason: fmt.Sprintf("error rate %.0f%% > 50%%", bus.ErrorRate*100)}
	} else if bus.ErrorRate > 0.2 {
		action = ControlAction{TargetKind: string(kind), Action: ActionThrottle, Priority: 7, Reason: fmt.Sprintf("error rate %.0f%% > 20%%", bus.ErrorRate*100)}
	}

	if action.Action != ActionNoOp {
		sfc.violation[key]++
		if sfc.violation[key] >= 3 {
			action.Action = ActionAlert
			action.Priority = 10
			action.Reason += " (consecutive violation >= 3)"
		}
	} else {
		sfc.violation[key] = 0
	}
	return action
}

func (sfc *SimpleFeedbackController) LastActions() []ControlAction {
	sfc.mu.Lock()
	defer sfc.mu.Unlock()
	return append([]ControlAction(nil), sfc.last...)
}
func (sfc *SimpleFeedbackController) Start(_ context.Context) {}
func (sfc *SimpleFeedbackController) Stop()                    {}

// ---------------------------------------------------------------------------
// FeedbackAwareBrainPool — 带反馈控制的 BrainPool 包装器（Phase B 扩展点）
// ---------------------------------------------------------------------------

// FeedbackAwareBrainPool 在 BrainPool 之上叠加反馈控制逻辑。
// GetBrain 时检查 FeedbackController 的动作，必要时拦截或重路由。
type FeedbackAwareBrainPool struct {
	pool       BrainPool
	controller FeedbackController
}

// NewFeedbackAwareBrainPool 创建带反馈控制的 brain pool。
func NewFeedbackAwareBrainPool(pool BrainPool, controller FeedbackController) *FeedbackAwareBrainPool {
	if controller == nil {
		controller = &NopFeedbackController{}
	}
	return &FeedbackAwareBrainPool{pool: pool, controller: controller}
}

// GetBrain 委托给底层 pool，但会在 controller 建议 ActionReroute 时尝试换路。
func (fap *FeedbackAwareBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	if fap.controller != nil {
		for _, a := range fap.controller.LastActions() {
			if a.TargetKind == string(kind) && a.Action == ActionThrottle {
				// 被限流时返回一个临时拒绝，让上层重试或路由
				return nil, fmt.Errorf("brain %s throttled by feedback controller: %s", kind, a.Reason)
			}
		}
	}
	return fap.pool.GetBrain(ctx, kind)
}

// Status 透传底层 pool 的状态。
func (fap *FeedbackAwareBrainPool) Status() map[agent.Kind]BrainStatus {
	return fap.pool.Status()
}

// AutoStart 透传。
func (fap *FeedbackAwareBrainPool) AutoStart(ctx context.Context) {
	fap.pool.AutoStart(ctx)
}

// Shutdown 透传。
func (fap *FeedbackAwareBrainPool) Shutdown(ctx context.Context) error {
	return fap.pool.Shutdown(ctx)
}

var _ BrainPool = (*FeedbackAwareBrainPool)(nil)
