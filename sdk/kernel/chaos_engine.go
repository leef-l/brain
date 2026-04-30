// chaos_engine.go — 混沌注入引擎（Chaos Engineering）
// MACCS Wave 6 — 生产级硬化。模拟各种故障场景，验证系统的弹性和恢复能力。
package kernel

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// FaultType 标识可注入的故障类型。
type FaultType string

const (
	FaultBrainCrash     FaultType = "brain_crash"     // Brain 进程崩溃
	FaultLLMTimeout     FaultType = "llm_timeout"     // LLM API 超时
	FaultLLMError       FaultType = "llm_error"       // LLM API 错误
	FaultNetworkDelay   FaultType = "network_delay"   // 网络延迟
	FaultDiskFull       FaultType = "disk_full"       // 磁盘满
	FaultMemoryPressure FaultType = "memory_pressure" // 内存压力
	FaultResourceLock   FaultType = "resource_lock"   // 资源锁定
)

// ChaosExperiment 描述一次混沌注入实验。
type ChaosExperiment struct {
	ExperimentID string        `json:"experiment_id"`
	Name         string        `json:"name"`
	FaultType    FaultType     `json:"fault_type"`
	Target       string        `json:"target"`    // 目标组件或 brain
	Duration     time.Duration `json:"duration"`  // 持续时间
	Intensity    float64       `json:"intensity"` // 故障强度 0-1
	Schedule     string        `json:"schedule"`  // once/periodic
	Active       bool          `json:"active"`
	CreatedAt    time.Time     `json:"created_at"`
}

// ChaosImpact 量化混沌实验造成的影响。
type ChaosImpact struct {
	AffectedTasks   int     `json:"affected_tasks"`
	FailedRequests  int     `json:"failed_requests"`
	LatencyIncrease float64 `json:"latency_increase_pct"` // 延迟增加百分比
	DataLoss        bool    `json:"data_loss"`
}

// ChaosResult 记录一次实验的完整结果。
type ChaosResult struct {
	ExperimentID    string        `json:"experiment_id"`
	StartedAt       time.Time     `json:"started_at"`
	EndedAt         time.Time     `json:"ended_at"`
	FaultInjected   bool          `json:"fault_injected"`
	SystemRecovered bool          `json:"system_recovered"`
	RecoveryTime    time.Duration `json:"recovery_time"`
	Impact          ChaosImpact   `json:"impact"`
	Observations    []string      `json:"observations"`
}

// FaultInjector 定义故障注入与移除的能力。
type FaultInjector interface {
	Inject(ctx context.Context, experiment *ChaosExperiment) error
	Remove(ctx context.Context, experimentID string) error
	IsActive(experimentID string) bool
}

// ChaosSummary 汇总所有实验的统计指标。
type ChaosSummary struct {
	TotalExperiments int           `json:"total_experiments"`
	CompletedCount   int           `json:"completed_count"`
	RecoveryRate     float64       `json:"recovery_rate"` // 恢复成功率
	AvgRecoveryTime  time.Duration `json:"avg_recovery_time"`
}

// InjectionEvent 记录单次故障注入/移除事件，用于历史审计。
type InjectionEvent struct {
	Time         time.Time `json:"time"`
	ExperimentID string    `json:"experiment_id"`
	FaultType    FaultType `json:"fault_type"`
	Action       string    `json:"action"` // "inject" | "remove"
	Detail       string    `json:"detail"`
}

// ChaosEngine 管理混沌实验的生命周期：创建、执行、停止、汇总。
type ChaosEngine struct {
	mu          sync.RWMutex
	experiments map[string]*ChaosExperiment
	results     []ChaosResult
	injectors   map[FaultType]FaultInjector
	history     []InjectionEvent
	enabled     bool
}

// NewChaosEngine 创建混沌引擎（默认禁用，需显式 Enable）。
func NewChaosEngine() *ChaosEngine {
	return &ChaosEngine{
		experiments: make(map[string]*ChaosExperiment),
		injectors:   make(map[FaultType]FaultInjector),
	}
}

// Enable 启用混沌引擎。
func (ce *ChaosEngine) Enable() { ce.mu.Lock(); ce.enabled = true; ce.mu.Unlock() }

// Disable 禁用混沌引擎；不影响已注入的故障。
func (ce *ChaosEngine) Disable() { ce.mu.Lock(); ce.enabled = false; ce.mu.Unlock() }

// RegisterInjector 注册指定故障类型的注入器。
func (ce *ChaosEngine) RegisterInjector(faultType FaultType, injector FaultInjector) {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	ce.injectors[faultType] = injector
}

// CreateExperiment 创建一个新的混沌实验。
func (ce *ChaosEngine) CreateExperiment(name string, faultType FaultType, target string, duration time.Duration, intensity float64) *ChaosExperiment {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	if intensity < 0 {
		intensity = 0
	} else if intensity > 1 {
		intensity = 1
	}
	exp := &ChaosExperiment{
		ExperimentID: fmt.Sprintf("chaos-%d", time.Now().UnixNano()),
		Name:         name,
		FaultType:    faultType,
		Target:       target,
		Duration:     duration,
		Intensity:    intensity,
		Schedule:     "once",
		CreatedAt:    time.Now(),
	}
	ce.experiments[exp.ExperimentID] = exp
	return exp
}

// RunExperiment 执行指定实验：注入故障 → 等待 duration → 移除故障 → 检查恢复。
func (ce *ChaosEngine) RunExperiment(ctx context.Context, experimentID string) (*ChaosResult, error) {
	ce.mu.RLock()
	if !ce.enabled {
		ce.mu.RUnlock()
		return nil, fmt.Errorf("chaos engine is disabled")
	}
	exp, ok := ce.experiments[experimentID]
	if !ok {
		ce.mu.RUnlock()
		return nil, fmt.Errorf("experiment %s not found", experimentID)
	}
	injector, hasInjector := ce.injectors[exp.FaultType]
	ce.mu.RUnlock()
	if !hasInjector {
		return nil, fmt.Errorf("no injector registered for fault type %s", exp.FaultType)
	}
	ce.mu.Lock()
	exp.Active = true
	ce.mu.Unlock()

	result := ChaosResult{ExperimentID: experimentID, StartedAt: time.Now()}
	// 注入故障
	if err := injector.Inject(ctx, exp); err != nil {
		result.EndedAt = time.Now()
		result.Observations = append(result.Observations, "fault injection failed: "+err.Error())
		ce.appendResult(result)
		ce.mu.Lock()
		exp.Active = false
		ce.mu.Unlock()
		return &result, fmt.Errorf("inject failed: %w", err)
	}
	result.FaultInjected = true
	result.Observations = append(result.Observations, fmt.Sprintf("fault %s injected on %s", exp.FaultType, exp.Target))
	ce.recordEvent(InjectionEvent{
		Time:         time.Now(),
		ExperimentID: experimentID,
		FaultType:    exp.FaultType,
		Action:       "inject",
		Detail:       fmt.Sprintf("target=%s intensity=%.2f", exp.Target, exp.Intensity),
	})
	// 等待持续时间或 context 取消
	timer := time.NewTimer(exp.Duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		result.Observations = append(result.Observations, "experiment interrupted by context cancellation")
	}
	// 移除故障并检查恢复
	removeStart := time.Now()
	if err := injector.Remove(ctx, experimentID); err != nil {
		result.Observations = append(result.Observations, "fault removal failed: "+err.Error())
	}
	ce.recordEvent(InjectionEvent{
		Time:         time.Now(),
		ExperimentID: experimentID,
		FaultType:    exp.FaultType,
		Action:       "remove",
		Detail:       fmt.Sprintf("recovery_elapsed=%s", time.Since(removeStart)),
	})
	if recovered := !injector.IsActive(experimentID); recovered {
		result.SystemRecovered = true
		result.RecoveryTime = time.Since(removeStart)
		result.Observations = append(result.Observations, "system recovered successfully")
	} else {
		result.Observations = append(result.Observations, "system did NOT recover")
	}
	result.EndedAt = time.Now()
	ce.mu.Lock()
	exp.Active = false
	ce.mu.Unlock()
	ce.appendResult(result)
	return &result, nil
}

// StopExperiment 停止一个活跃的实验。
func (ce *ChaosEngine) StopExperiment(experimentID string) error {
	ce.mu.RLock()
	exp, ok := ce.experiments[experimentID]
	if !ok {
		ce.mu.RUnlock()
		return fmt.Errorf("experiment %s not found", experimentID)
	}
	injector, hasInjector := ce.injectors[exp.FaultType]
	ce.mu.RUnlock()
	if !hasInjector {
		return fmt.Errorf("no injector for fault type %s", exp.FaultType)
	}
	if err := injector.Remove(context.Background(), experimentID); err != nil {
		return fmt.Errorf("stop experiment: %w", err)
	}
	ce.recordEvent(InjectionEvent{
		Time:         time.Now(),
		ExperimentID: experimentID,
		FaultType:    exp.FaultType,
		Action:       "remove",
		Detail:       "stopped by StopExperiment",
	})
	ce.mu.Lock()
	exp.Active = false
	ce.mu.Unlock()
	return nil
}

// GetResults 返回指定实验的所有结果。
func (ce *ChaosEngine) GetResults(experimentID string) []ChaosResult {
	ce.mu.RLock()
	defer ce.mu.RUnlock()
	var out []ChaosResult
	for _, r := range ce.results {
		if r.ExperimentID == experimentID {
			out = append(out, r)
		}
	}
	return out
}

// AllExperiments 返回所有实验的快照。
func (ce *ChaosEngine) AllExperiments() []*ChaosExperiment {
	ce.mu.RLock()
	defer ce.mu.RUnlock()
	out := make([]*ChaosExperiment, 0, len(ce.experiments))
	for _, exp := range ce.experiments {
		out = append(out, exp)
	}
	return out
}

// Summary 返回所有实验的汇总统计。
func (ce *ChaosEngine) Summary() ChaosSummary {
	ce.mu.RLock()
	defer ce.mu.RUnlock()
	s := ChaosSummary{TotalExperiments: len(ce.experiments)}
	if len(ce.results) == 0 {
		return s
	}
	var totalRecovery time.Duration
	var recovered int
	for _, r := range ce.results {
		s.CompletedCount++
		if r.SystemRecovered {
			recovered++
			totalRecovery += r.RecoveryTime
		}
	}
	if s.CompletedCount > 0 {
		s.RecoveryRate = float64(recovered) / float64(s.CompletedCount)
	}
	if recovered > 0 {
		s.AvgRecoveryTime = totalRecovery / time.Duration(recovered)
	}
	return s
}

// InjectionHistory 返回所有注入/移除事件的历史记录（只读快照）。
func (ce *ChaosEngine) InjectionHistory() []InjectionEvent {
	ce.mu.RLock()
	defer ce.mu.RUnlock()
	out := make([]InjectionEvent, len(ce.history))
	copy(out, ce.history)
	return out
}

func (ce *ChaosEngine) appendResult(r ChaosResult) {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	ce.results = append(ce.results, r)
}

func (ce *ChaosEngine) recordEvent(ev InjectionEvent) {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	ce.history = append(ce.history, ev)
}

// ---------------------------------------------------------------------------
// MemFaultInjector — 内存 bookkeeping（仅跟踪活跃状态，不产生运行时副作用）
// ---------------------------------------------------------------------------

// MemFaultInjector 在内存中模拟故障注入，不实际 kill 进程，适用于测试和演练场景。
type MemFaultInjector struct {
	mu     sync.RWMutex
	active map[string]*ChaosExperiment
}

// NewMemFaultInjector 创建内存故障注入器。
func NewMemFaultInjector() *MemFaultInjector {
	return &MemFaultInjector{active: make(map[string]*ChaosExperiment)}
}

func (m *MemFaultInjector) Inject(_ context.Context, experiment *ChaosExperiment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[experiment.ExperimentID] = experiment
	return nil
}

func (m *MemFaultInjector) Remove(_ context.Context, experimentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, experimentID)
	return nil
}

func (m *MemFaultInjector) IsActive(experimentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.active[experimentID]
	return ok
}

// ---------------------------------------------------------------------------
// DelayInjector — 延迟注入器
// ---------------------------------------------------------------------------

// DelayInjector 注入网络/操作延迟。
// Inject 会阻塞调用者 intensity×MaxDelay 毫秒，模拟慢速响应。
// 注入期间 IsActive 返回 true；Remove 后停止阻塞新请求。
type DelayInjector struct {
	mu       sync.RWMutex
	active   map[string]*ChaosExperiment
	MaxDelay time.Duration // 最大延迟（intensity=1.0 时使用）；默认 5s
}

// NewDelayInjector 创建延迟注入器，maxDelay=0 时使用默认 5s。
func NewDelayInjector(maxDelay time.Duration) *DelayInjector {
	if maxDelay <= 0 {
		maxDelay = 5 * time.Second
	}
	return &DelayInjector{
		active:   make(map[string]*ChaosExperiment),
		MaxDelay: maxDelay,
	}
}

// Inject 记录实验为活跃，并立即阻塞 intensity×MaxDelay，模拟注入时刻的延迟峰值。
func (d *DelayInjector) Inject(ctx context.Context, experiment *ChaosExperiment) error {
	d.mu.Lock()
	d.active[experiment.ExperimentID] = experiment
	delay := time.Duration(float64(d.MaxDelay) * experiment.Intensity)
	d.mu.Unlock()

	if delay <= 0 {
		return nil
	}
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Remove 移除活跃标记，后续请求不再受延迟影响。
func (d *DelayInjector) Remove(_ context.Context, experimentID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.active, experimentID)
	return nil
}

// IsActive 返回该实验是否仍处于注入状态。
func (d *DelayInjector) IsActive(experimentID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.active[experimentID]
	return ok
}

// ApplyDelay 供业务代码在关键路径上调用：若该实验活跃则阻塞对应延迟。
// 用法：在 HTTP handler / RPC 入口插入 delayInjector.ApplyDelay(ctx, experimentID)。
func (d *DelayInjector) ApplyDelay(ctx context.Context, experimentID string) error {
	d.mu.RLock()
	exp, ok := d.active[experimentID]
	d.mu.RUnlock()
	if !ok {
		return nil
	}
	delay := time.Duration(float64(d.MaxDelay) * exp.Intensity)
	if delay <= 0 {
		return nil
	}
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// ErrorInjector — 错误注入器
// ---------------------------------------------------------------------------

// ErrorInjector 以可配置概率返回预设错误，模拟 LLM API 错误、磁盘满等。
// 概率由 experiment.Intensity 控制（1.0 = 100% 注入）。
type ErrorInjector struct {
	mu     sync.RWMutex
	active map[string]*ChaosExperiment
	rng    *rand.Rand
	// InjectedCount 统计真正触发注入（返回 error）的次数，用于验收。
	InjectedCount int64
}

// NewErrorInjector 创建错误注入器。
func NewErrorInjector() *ErrorInjector {
	return &ErrorInjector{
		active: make(map[string]*ChaosExperiment),
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec
	}
}

// Inject 记录实验活跃，并根据 intensity 概率决定本次调用是否注入错误。
// 若决定注入，则立即返回包含 fault 类型和目标的 error（真正改变调用链行为）。
func (e *ErrorInjector) Inject(_ context.Context, experiment *ChaosExperiment) error {
	e.mu.Lock()
	e.active[experiment.ExperimentID] = experiment
	shouldFire := e.rng.Float64() < experiment.Intensity
	if shouldFire {
		e.InjectedCount++
	}
	e.mu.Unlock()

	if shouldFire {
		return fmt.Errorf("chaos fault injected: type=%s target=%s experimentID=%s",
			experiment.FaultType, experiment.Target, experiment.ExperimentID)
	}
	return nil
}

// Remove 移除活跃标记，后续操作不再受错误注入影响。
func (e *ErrorInjector) Remove(_ context.Context, experimentID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, experimentID)
	return nil
}

// IsActive 返回该实验是否仍处于注入状态。
func (e *ErrorInjector) IsActive(experimentID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.active[experimentID]
	return ok
}

// MaybeFail 供业务代码在关键路径上调用：若实验活跃且命中概率则返回 error。
// 用法：if err := errInjector.MaybeFail(experimentID); err != nil { return err }
func (e *ErrorInjector) MaybeFail(experimentID string) error {
	e.mu.Lock()
	exp, ok := e.active[experimentID]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	fire := e.rng.Float64() < exp.Intensity
	if fire {
		e.InjectedCount++
	}
	e.mu.Unlock()

	if fire {
		return fmt.Errorf("chaos fault injected: type=%s target=%s experimentID=%s",
			exp.FaultType, exp.Target, experimentID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// PanicInjector — panic 注入器（仅供 dev/test 模式使用）
// ---------------------------------------------------------------------------

// PanicInjector 在注入时触发 panic，模拟进程崩溃场景。
// 警告：只在 dev/test 模式下启用；生产环境使用时调用方必须 recover。
type PanicInjector struct {
	mu      sync.RWMutex
	active  map[string]*ChaosExperiment
	devMode bool
	rng     *rand.Rand
	// PanicCount 统计触发 panic 的次数（在 recover 后可读取）。
	PanicCount int64
}

// NewPanicInjector 创建 panic 注入器。devMode=false 时 Inject 退化为 NoOp（安全降级）。
func NewPanicInjector(devMode bool) *PanicInjector {
	return &PanicInjector{
		active:  make(map[string]*ChaosExperiment),
		devMode: devMode,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Inject 在 devMode 下以 intensity 概率触发 panic。
// 调用方需使用 recover() 捕获，否则进程终止（这正是模拟崩溃的目的）。
func (p *PanicInjector) Inject(_ context.Context, experiment *ChaosExperiment) error {
	p.mu.Lock()
	p.active[experiment.ExperimentID] = experiment
	shouldPanic := p.devMode && p.rng.Float64() < experiment.Intensity
	if shouldPanic {
		p.PanicCount++
	}
	p.mu.Unlock()

	if shouldPanic {
		panic(fmt.Sprintf("chaos panic injected: type=%s target=%s experimentID=%s",
			experiment.FaultType, experiment.Target, experiment.ExperimentID))
	}
	return nil
}

// Remove 移除活跃标记。
func (p *PanicInjector) Remove(_ context.Context, experimentID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, experimentID)
	return nil
}

// IsActive 返回该实验是否仍处于注入状态。
func (p *PanicInjector) IsActive(experimentID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.active[experimentID]
	return ok
}
