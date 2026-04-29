// system_state.go — Brain 系统控制论状态空间
//
// 将钱学森《工程控制论》主线1（系统建模）映射到 Brain 系统：
//   状态 = 各 brain 健康度、负载、队列深度、租约占用率
//   输入 = 请求速率、任务复杂度、环境扰动
//   输出 = 完成率、延迟、错误率、资源利用率
//
// SystemState 是控制论框架的观测对象，StateObserver 负责从现有
// 基础设施（BrainPool、runManager、LeaseManager、LearningEngine）
// 聚合实时状态。
package kernel

import (
	"context"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// SystemState — 系统状态向量
// ---------------------------------------------------------------------------

// BrainUnitState 是单个 brain 的局部状态。
type BrainUnitState struct {
	Kind         agent.Kind `json:"kind"`
	Healthy      bool       `json:"healthy"`       // 进程是否存活
	Load         float64    `json:"load"`          // 当前负载 [0, 1]
	QueueDepth   int        `json:"queue_depth"`   // 待处理任务数
	SuccessRate  float64    `json:"success_rate"`  // EWMA 平滑成功率
	AvgLatencyMs float64    `json:"avg_latency_ms"` // EWMA 平滑延迟(ms)
	ErrorRate    float64    `json:"error_rate"`    // EWMA 平滑错误率
}

// SystemState 是 Brain 系统全局状态向量，供 FeedbackController、
// SelfStabilizer、CouplingEstimator 使用。
type SystemState struct {
	Timestamp time.Time `json:"timestamp"`

	// --- 状态向量（State） ---
	Brains map[agent.Kind]BrainUnitState `json:"brains"`

	// 全局资源状态
	LeaseOccupancy float64 `json:"lease_occupancy"` // [0, 1] 租约占用率
	Concurrency    int     `json:"concurrency"`     // 当前并发运行数
	QueueDepth     int     `json:"queue_depth"`     // 全局等待队列深度

	// --- 输出向量（Output） ---
	GlobalSuccessRate  float64 `json:"global_success_rate"`  // 全局成功率
	GlobalAvgLatencyMs float64 `json:"global_avg_latency_ms"` // 全局平均延迟
	GlobalErrorRate    float64 `json:"global_error_rate"`    // 全局错误率
	Throughput         float64 `json:"throughput"`           // 每秒完成任务数

	// --- 输入向量（Input / 扰动） ---
	RequestRate   float64 `json:"request_rate"`   // 每秒请求数
	TaskComplexity float64 `json:"task_complexity"` // 平均任务复杂度（工具调用数）
	NoiseLevel    float64 `json:"noise_level"`    // 环境噪声水平（错误率基线）
}

// Clone 返回状态的深度拷贝，防止观测器与控制器之间的数据竞争。
func (s *SystemState) Clone() *SystemState {
	if s == nil {
		return nil
	}
	out := &SystemState{
		Timestamp:          s.Timestamp,
		Brains:             make(map[agent.Kind]BrainUnitState, len(s.Brains)),
		LeaseOccupancy:     s.LeaseOccupancy,
		Concurrency:        s.Concurrency,
		QueueDepth:         s.QueueDepth,
		GlobalSuccessRate:  s.GlobalSuccessRate,
		GlobalAvgLatencyMs: s.GlobalAvgLatencyMs,
		GlobalErrorRate:    s.GlobalErrorRate,
		Throughput:         s.Throughput,
		RequestRate:        s.RequestRate,
		TaskComplexity:     s.TaskComplexity,
		NoiseLevel:         s.NoiseLevel,
	}
	for k, v := range s.Brains {
		out.Brains[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// StateObserver — 状态观测器接口
// ---------------------------------------------------------------------------

// StateObserver 从现有基础设施聚合 SystemState。
// 实现必须是线程安全的。
type StateObserver interface {
	// Snapshot 返回当前系统状态的拷贝。
	Snapshot() *SystemState
	// Start 启动后台观测循环（可选，某些实现可能是被动的）。
	Start(ctx context.Context)
	// Stop 停止后台观测循环。
	Stop()
}

// ---------------------------------------------------------------------------
// MemStateObserver — 内存状态观测器
// ---------------------------------------------------------------------------

// MetricsCallback 允许外部（如 runManager）注入运行时指标。
// 返回：并发数、队列深度、请求速率(每秒)、吞吐量(每秒)。
type MetricsCallback func() (concurrency, queueDepth int, requestRate, throughput float64)

// MemStateObserver 从 BrainPool、LeaseManager、LearningEngine 等
// 现有组件聚合状态，每 30 秒更新一次。
type MemStateObserver struct {
	mu              sync.RWMutex
	state           *SystemState
	pool            BrainPool
	leaseMgr        *MemLeaseManager
	learner         *LearningEngine
	metricsCallback MetricsCallback
	interval        time.Duration
	stopCh          chan struct{}
}

// NewMemStateObserver 创建内存状态观测器。
// pool/leaseMgr/learner 可为 nil，nil 时对应指标留空。
func NewMemStateObserver(pool BrainPool, leaseMgr *MemLeaseManager, learner *LearningEngine) *MemStateObserver {
	return &MemStateObserver{
		state: &SystemState{
			Timestamp: time.Now().UTC(),
			Brains:    make(map[agent.Kind]BrainUnitState),
		},
		pool:     pool,
		leaseMgr: leaseMgr,
		learner:  learner,
		interval: 30 * time.Second,
		stopCh:   make(chan struct{}),
	}
}

// SetMetricsCallback 设置外部指标回调，用于获取并发数、队列深度等运行时数据。
func (o *MemStateObserver) SetMetricsCallback(cb MetricsCallback) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.metricsCallback = cb
}

// Snapshot 返回当前状态的防御性拷贝。
func (o *MemStateObserver) Snapshot() *SystemState {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.state.Clone()
}

// Start 启动后台观测循环。
func (o *MemStateObserver) Start(ctx context.Context) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-o.stopCh:
			return
		case <-ticker.C:
			o.observe()
		}
	}
}

// Stop 停止后台观测循环。
func (o *MemStateObserver) Stop() {
	close(o.stopCh)
}

// observe 执行一次状态聚合。
func (o *MemStateObserver) observe() {
	state := &SystemState{
		Timestamp: time.Now().UTC(),
		Brains:    make(map[agent.Kind]BrainUnitState),
	}

	// 1. 从 BrainPool 聚合各 brain 状态
	if o.pool != nil {
		for kind, status := range o.pool.Status() {
			bs := BrainUnitState{
				Kind:        kind,
				Healthy:     status.Running,
				Load:        0, // BrainStatus 当前仅暴露 Running/Binary，不暴露负载；Load 由 LearningEngine 画像间接推断或通过外部 MetricsCallback 注入
				SuccessRate: 1.0, // 乐观默认：无学习数据时视为健康，避免冷启动被误判为故障
			}
			state.Brains[kind] = bs
		}
	}

	// 2. 从 LearningEngine 聚合成功率/延迟（L1 能力画像）
	if o.learner != nil {
		for kind, profile := range o.learner.Profiles() {
			bs, ok := state.Brains[kind]
			if !ok {
				bs = BrainUnitState{Kind: kind}
			}
			if profile != nil && len(profile.TaskScores) > 0 {
				var totalSuccess, totalLatency float64
				var count int
				for _, ts := range profile.TaskScores {
					totalSuccess += ts.Accuracy.Value
					totalLatency += ts.Speed.Value * 1000 // Speed 存的是秒，转 ms
					count++
				}
				if count > 0 {
					bs.SuccessRate = totalSuccess / float64(count)
					bs.AvgLatencyMs = totalLatency / float64(count)
				}
			}
			state.Brains[kind] = bs
		}
	}

	// 3. 从 LeaseManager 聚合资源占用
	if o.leaseMgr != nil {
		active := o.leaseMgr.ActiveCount()
		// 以 20 个并发租约为满负荷基准进行估算
		state.LeaseOccupancy = min(float64(active)/20.0, 1.0)
	}

	// 3b. 从外部回调注入运行时指标（并发、队列、速率、吞吐）
	if o.metricsCallback != nil {
		state.Concurrency, state.QueueDepth, state.RequestRate, state.Throughput = o.metricsCallback()
	}

	// 4. 全局指标聚合（基于各 brain 的加权平均）
	if len(state.Brains) > 0 {
		var sumSuccess, sumLatency, sumError float64
		for _, bs := range state.Brains {
			sumSuccess += bs.SuccessRate
			sumLatency += bs.AvgLatencyMs
			sumError += bs.ErrorRate
		}
		n := float64(len(state.Brains))
		state.GlobalSuccessRate = sumSuccess / n
		state.GlobalAvgLatencyMs = sumLatency / n
		state.GlobalErrorRate = sumError / n
	}

	// 5. 输入/扰动（简化估计）
	state.NoiseLevel = state.GlobalErrorRate // 以错误率基线作为噪声水平

	o.mu.Lock()
	o.state = state
	o.mu.Unlock()
}
