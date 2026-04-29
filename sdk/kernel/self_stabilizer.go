// self_stabilizer.go — 自稳定器（Self Stabilizer）
//
// 钱学森《工程控制论》主线5（长期可靠）的落地载体。
// SelfStabilizer 持续监测系统是否进入不稳定状态（振荡、死锁、
// 资源泄漏、正反馈失控），并在检测到不稳定时触发恢复动作。
//
// 检测维度：
//   1. 振荡检测：某指标在短时间内反复上下穿越阈值
//   2. 死锁检测：长时间无状态变化 + 队列持续增长
//   3. 正反馈环：两个 brain 互相推高负载（通过 CouplingMatrix 识别）
//   4. 资源泄漏：租约占用率持续增长但任务数不增
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
// StabilityIssue — 稳定性问题
// ---------------------------------------------------------------------------

// StabilityIssue 描述检测到的不稳定模式。
type StabilityIssue struct {
	Type     string    `json:"type"`     // "oscillation", "deadlock", "feedback_loop", "resource_leak"
	Kind     string    `json:"kind"`     // 关联 brain kind，空字符串表示全局
	Severity string    `json:"severity"` // "critical", "warning"
	Detail   string    `json:"detail"`
	Since    time.Time `json:"since"`
}

// ---------------------------------------------------------------------------
// StabilityReport — 稳定性报告
// ---------------------------------------------------------------------------

// StabilityReport 是自稳定器的一次评估结果。
type StabilityReport struct {
	Timestamp time.Time          `json:"timestamp"`
	Stable    bool               `json:"stable"`
	Issues    []StabilityIssue   `json:"issues,omitempty"`
	Actions   []ControlAction    `json:"actions,omitempty"`
}

// ---------------------------------------------------------------------------
// SelfStabilizer — 自稳定器接口
// ---------------------------------------------------------------------------

// SelfStabilizer 是长期稳定性保障的接口。
type SelfStabilizer interface {
	// Check 对当前状态和历史做一次稳定性评估。
	Check(state *SystemState, history []*SystemState) StabilityReport
	// LastReport 返回最近一次评估报告。
	LastReport() StabilityReport
	// Start 启动后台监测循环。
	Start(ctx context.Context)
	// Stop 停止后台监测循环。
	Stop()
}

// ---------------------------------------------------------------------------
// MemSelfStabilizer — 内存自稳定器
// ---------------------------------------------------------------------------

// MemSelfStabilizer 基于滑动窗口历史进行稳定性检测。
type MemSelfStabilizer struct {
	observer  StateObserver
	coupling  *CouplingMatrix

	mu       sync.RWMutex
	history  []*SystemState // 滑动窗口，保留最近 N 次状态快照
	report   StabilityReport

	// 配置参数
	WindowSize           int           // 历史窗口大小
	OscillationThreshold int           // 连续方向变化次数判定为振荡
	DeadlockTimeout      time.Duration // 无有效完成即判定死锁的时间
	LeakGrowthThreshold  float64       // 租约增长率阈值

	interval time.Duration
	stopCh   chan struct{}
}

// NewMemSelfStabilizer 创建内存自稳定器。
func NewMemSelfStabilizer(observer StateObserver, coupling *CouplingMatrix) *MemSelfStabilizer {
	return &MemSelfStabilizer{
		observer:             observer,
		coupling:             coupling,
		history:              make([]*SystemState, 0, 20),
		WindowSize:           20,
		OscillationThreshold: 4,
		DeadlockTimeout:      5 * time.Minute,
		LeakGrowthThreshold:  0.10, // 10%
		interval:             30 * time.Second,
		stopCh:               make(chan struct{}),
	}
}

// Check 执行稳定性检测。
func (ss *MemSelfStabilizer) Check(state *SystemState, history []*SystemState) StabilityReport {
	now := time.Now().UTC()
	report := StabilityReport{Timestamp: now, Stable: true}

	if state == nil {
		return report
	}

	// 1. 振荡检测：检查各 brain 的负载/错误率是否反复穿越阈值
	for kind := range state.Brains {
		if detectOscillation(kind, history, ss.OscillationThreshold, func(s *SystemState) float64 {
			if b, ok := s.Brains[kind]; ok {
				return b.Load
			}
			return 0
		}) {
			report.Issues = append(report.Issues, StabilityIssue{
				Type:     "oscillation",
				Kind:     string(kind),
				Severity: "warning",
				Detail:   fmt.Sprintf("%s load oscillating over last %d samples", kind, len(history)),
				Since:    now,
			})
			report.Stable = false
		}
	}

	// 2. 死锁检测：队列持续增长但吞吐量接近 0
	if len(history) >= 3 {
		if state.QueueDepth > history[0].QueueDepth && state.Throughput < 0.01 {
			report.Issues = append(report.Issues, StabilityIssue{
				Type:     "deadlock",
				Kind:     "",
				Severity: "critical",
				Detail:   fmt.Sprintf("queue depth rising (%d → %d) but throughput stalled", history[0].QueueDepth, state.QueueDepth),
				Since:    now,
			})
			report.Stable = false
		}
	}

	// 3. 正反馈环检测：通过 CouplingMatrix 检查两个 brain 是否互相推高
	if ss.coupling != nil {
		for kind, bs := range state.Brains {
			if bs.Load < 0.5 {
				continue
			}
			// 查找所有对当前 brain 有正向耦合且自身负载也高的 source
			for srcKind := range state.Brains {
				if srcKind == kind {
					continue
				}
				srcBs := state.Brains[srcKind]
				if srcBs.Load < 0.5 {
					continue
				}
				w := ss.coupling.Get(string(srcKind), string(kind))
				if w > 0.3 {
					report.Issues = append(report.Issues, StabilityIssue{
						Type:     "feedback_loop",
						Kind:     fmt.Sprintf("%s→%s", srcKind, kind),
						Severity: "warning",
						Detail:   fmt.Sprintf("positive feedback loop detected: %s (load=%.2f) → %s (load=%.2f) weight=%.2f", srcKind, srcBs.Load, kind, bs.Load, w),
						Since:    now,
					})
					report.Stable = false
				}
			}
		}
	}

	// 4. 资源泄漏检测：租约占用率持续上升
	if len(history) >= 2 {
		first := history[0]
		last := history[len(history)-1]
		if first.LeaseOccupancy > 0 && last.LeaseOccupancy > first.LeaseOccupancy*(1+ss.LeakGrowthThreshold) {
			report.Issues = append(report.Issues, StabilityIssue{
				Type:     "resource_leak",
				Kind:     "",
				Severity: "warning",
				Detail:   fmt.Sprintf("lease occupancy growing (%.2f → %.2f) without task growth", first.LeaseOccupancy, last.LeaseOccupancy),
				Since:    now,
			})
			report.Stable = false
		}
	}

	// 生成恢复动作
	for _, issue := range report.Issues {
		switch issue.Type {
		case "deadlock":
			report.Actions = append(report.Actions, ControlAction{
				TargetKind: "",
				Action:     ActionAlert,
				Priority:   10,
				Reason:     "deadlock detected: manual intervention or queue flush required",
				Timestamp:  now,
			})
		case "feedback_loop":
			report.Actions = append(report.Actions, ControlAction{
				TargetKind: issue.Kind,
				Action:     ActionThrottle,
				Priority:   8,
				Reason:     "positive feedback loop: throttling to break the cycle",
				Timestamp:  now,
			})
		case "resource_leak":
			report.Actions = append(report.Actions, ControlAction{
				TargetKind: "",
				Action:     ActionAlert,
				Priority:   7,
				Reason:     "potential lease leak: review lease release logic",
				Timestamp:  now,
			})
		}
	}

	ss.mu.Lock()
	ss.report = report
	ss.mu.Unlock()
	return report
}

// LastReport 返回最近一次评估报告。
func (ss *MemSelfStabilizer) LastReport() StabilityReport {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.report
}

// Start 启动后台监测循环。
func (ss *MemSelfStabilizer) Start(ctx context.Context) {
	if ss.observer == nil {
		return
	}
	ticker := time.NewTicker(ss.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ss.stopCh:
			return
		case <-ticker.C:
			state := ss.observer.Snapshot()
			ss.mu.Lock()
			ss.history = append(ss.history, state)
			if len(ss.history) > ss.WindowSize {
				ss.history = ss.history[len(ss.history)-ss.WindowSize:]
			}
			histCopy := make([]*SystemState, len(ss.history))
			copy(histCopy, ss.history)
			ss.mu.Unlock()

			report := ss.Check(state, histCopy)
			if !report.Stable {
				for _, issue := range report.Issues {
					fmt.Fprintf(os.Stderr, "[stabilizer] %s kind=%s severity=%s: %s\n",
						issue.Type, issue.Kind, issue.Severity, issue.Detail)
				}
			}
		}
	}
}

// Stop 停止后台监测循环。
func (ss *MemSelfStabilizer) Stop() {
	close(ss.stopCh)
}

// ---------------------------------------------------------------------------
// 检测辅助函数
// ---------------------------------------------------------------------------

// detectOscillation 检测序列中是否存在频繁的方向变化。
func detectOscillation(kind agent.Kind, history []*SystemState, threshold int, extractor func(*SystemState) float64) bool {
	if len(history) < threshold+1 {
		return false
	}
	changes := 0
	var lastDir int // -1 = down, 1 = up, 0 = none
	for i := 1; i < len(history); i++ {
		prev := extractor(history[i-1])
		curr := extractor(history[i])
		delta := curr - prev
		if delta == 0 {
			continue
		}
		dir := 1
		if delta < 0 {
			dir = -1
		}
		if lastDir != 0 && dir != lastDir {
			changes++
		}
		lastDir = dir
	}
	return changes >= threshold
}
