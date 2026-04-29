// performance_spec.go — 性能指标规范（Performance Specification）
//
// 钱学森《工程控制论》主线4（性能指标驱动）的落地载体。
// PerformanceSpec 定义 Brain 系统的期望性能边界，FeedbackController
// 将 SystemState 与 PerformanceSpec 比较，生成控制动作。
package kernel

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// PerformanceSpec — 性能指标规范
// ---------------------------------------------------------------------------

// PerformanceSpec 定义 Brain 系统的期望性能边界。
// 所有阈值均为"上限"或"下限"，实际值突破阈值时触发控制动作。
type PerformanceSpec struct {
	// 全局阈值
	TargetSuccessRate  float64 `json:"target_success_rate"`  // 目标成功率（如 0.95）
	MaxLatencyMs       float64 `json:"max_latency_ms"`       // 最大允许平均延迟(ms)
	MaxErrorRate       float64 `json:"max_error_rate"`       // 最大允许错误率
	MaxLoad            float64 `json:"max_load"`             // 最大允许负载 [0, 1]
	MinThroughput      float64 `json:"min_throughput"`       // 最小吞吐量（任务/秒）
	MaxQueueDepth      int     `json:"max_queue_depth"`      // 最大队列深度
	MaxLeaseOccupancy  float64 `json:"max_lease_occupancy"`  // 最大租约占用率 [0, 1]

	// 单 brain 阈值（按 Kind 覆盖全局默认值）
	BrainOverrides map[string]BrainPerformanceSpec `json:"brain_overrides,omitempty"`

	// 告警与响应参数
	Cooldown      time.Duration `json:"cooldown"`       // 同一 brain 两次控制动作的最小间隔
	Consecutive   int           `json:"consecutive"`    // 连续 N 次越界才触发动作（防抖）
}

// BrainPerformanceSpec 是单个 brain 的性能覆盖。
type BrainPerformanceSpec struct {
	TargetSuccessRate float64 `json:"target_success_rate,omitempty"`
	MaxLatencyMs      float64 `json:"max_latency_ms,omitempty"`
	MaxErrorRate      float64 `json:"max_error_rate,omitempty"`
	MaxLoad           float64 `json:"max_load,omitempty"`
}

// DefaultPerformanceSpec 返回适用于大多数场景的默认规范。
func DefaultPerformanceSpec() *PerformanceSpec {
	return &PerformanceSpec{
		TargetSuccessRate: 0.90,
		MaxLatencyMs:      5000,
		MaxErrorRate:      0.10,
		MaxLoad:           0.80,
		MinThroughput:     0.1,
		MaxQueueDepth:     100,
		MaxLeaseOccupancy: 0.85,
		Cooldown:          30 * time.Second,
		Consecutive:       2,
	}
}

// GetBrainSpec 返回指定 brain 的生效规范（优先覆盖值，否则回退全局）。
func (ps *PerformanceSpec) GetBrainSpec(kind string) BrainPerformanceSpec {
	if ps == nil {
		return BrainPerformanceSpec{}
	}
	out := BrainPerformanceSpec{
		TargetSuccessRate: ps.TargetSuccessRate,
		MaxLatencyMs:      ps.MaxLatencyMs,
		MaxErrorRate:      ps.MaxErrorRate,
		MaxLoad:           ps.MaxLoad,
	}
	if ps.BrainOverrides == nil {
		return out
	}
	if ov, ok := ps.BrainOverrides[kind]; ok {
		if ov.TargetSuccessRate > 0 {
			out.TargetSuccessRate = ov.TargetSuccessRate
		}
		if ov.MaxLatencyMs > 0 {
			out.MaxLatencyMs = ov.MaxLatencyMs
		}
		if ov.MaxErrorRate > 0 {
			out.MaxErrorRate = ov.MaxErrorRate
		}
		if ov.MaxLoad > 0 {
			out.MaxLoad = ov.MaxLoad
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// PerformanceGap — 性能差距
// ---------------------------------------------------------------------------

// PerformanceGap 描述某一指标的实际值与目标值之间的偏差。
type PerformanceGap struct {
	Metric   string  `json:"metric"`    // 指标名，如 "success_rate", "latency"
	Target   float64 `json:"target"`    // 目标值
	Actual   float64 `json:"actual"`    // 实际值
	Delta    float64 `json:"delta"`     // 偏差 = target - actual（正数表示未达标）
	Severity string  `json:"severity"`  // "critical", "warning", "ok"
	Kind     string  `json:"kind"`      // 关联的 brain kind，空字符串表示全局
}

// Check 将 SystemState 与 PerformanceSpec 比较，返回所有 PerformanceGap。
func (ps *PerformanceSpec) Check(state *SystemState) []PerformanceGap {
	if ps == nil || state == nil {
		return nil
	}
	var gaps []PerformanceGap
	now := time.Now().UTC()
	_ = now

	// 全局指标检查
	if state.GlobalSuccessRate < ps.TargetSuccessRate {
		gaps = append(gaps, PerformanceGap{
			Metric:   "global_success_rate",
			Target:   ps.TargetSuccessRate,
			Actual:   state.GlobalSuccessRate,
			Delta:    ps.TargetSuccessRate - state.GlobalSuccessRate,
			Severity: severityForDelta(ps.TargetSuccessRate - state.GlobalSuccessRate, 0.05, 0.15),
		})
	}
	if state.GlobalAvgLatencyMs > ps.MaxLatencyMs {
		gaps = append(gaps, PerformanceGap{
			Metric:   "global_latency",
			Target:   ps.MaxLatencyMs,
			Actual:   state.GlobalAvgLatencyMs,
			Delta:    state.GlobalAvgLatencyMs - ps.MaxLatencyMs,
			Severity: severityForDelta(state.GlobalAvgLatencyMs-ps.MaxLatencyMs, ps.MaxLatencyMs*0.1, ps.MaxLatencyMs*0.3),
		})
	}
	if state.GlobalErrorRate > ps.MaxErrorRate {
		gaps = append(gaps, PerformanceGap{
			Metric:   "global_error_rate",
			Target:   ps.MaxErrorRate,
			Actual:   state.GlobalErrorRate,
			Delta:    state.GlobalErrorRate - ps.MaxErrorRate,
			Severity: severityForDelta(state.GlobalErrorRate-ps.MaxErrorRate, 0.02, 0.08),
		})
	}
	if state.LeaseOccupancy > ps.MaxLeaseOccupancy {
		gaps = append(gaps, PerformanceGap{
			Metric:   "lease_occupancy",
			Target:   ps.MaxLeaseOccupancy,
			Actual:   state.LeaseOccupancy,
			Delta:    state.LeaseOccupancy - ps.MaxLeaseOccupancy,
			Severity: severityForDelta(state.LeaseOccupancy-ps.MaxLeaseOccupancy, 0.05, 0.15),
		})
	}
	if state.QueueDepth > ps.MaxQueueDepth {
		gaps = append(gaps, PerformanceGap{
			Metric:   "queue_depth",
			Target:   float64(ps.MaxQueueDepth),
			Actual:   float64(state.QueueDepth),
			Delta:    float64(state.QueueDepth - ps.MaxQueueDepth),
			Severity: severityForDelta(float64(state.QueueDepth-ps.MaxQueueDepth), 10, 50),
		})
	}
	if state.Throughput < ps.MinThroughput {
		gaps = append(gaps, PerformanceGap{
			Metric:   "throughput",
			Target:   ps.MinThroughput,
			Actual:   state.Throughput,
			Delta:    ps.MinThroughput - state.Throughput,
			Severity: severityForDelta(ps.MinThroughput-state.Throughput, ps.MinThroughput*0.1, ps.MinThroughput*0.3),
		})
	}

	// 单 brain 检查
	for kind, bs := range state.Brains {
		bSpec := ps.GetBrainSpec(string(kind))
		if bs.SuccessRate < bSpec.TargetSuccessRate {
			gaps = append(gaps, PerformanceGap{
				Metric:   "success_rate",
				Target:   bSpec.TargetSuccessRate,
				Actual:   bs.SuccessRate,
				Delta:    bSpec.TargetSuccessRate - bs.SuccessRate,
				Severity: severityForDelta(bSpec.TargetSuccessRate-bs.SuccessRate, 0.05, 0.15),
				Kind:     string(kind),
			})
		}
		if bs.AvgLatencyMs > bSpec.MaxLatencyMs {
			gaps = append(gaps, PerformanceGap{
				Metric:   "latency",
				Target:   bSpec.MaxLatencyMs,
				Actual:   bs.AvgLatencyMs,
				Delta:    bs.AvgLatencyMs - bSpec.MaxLatencyMs,
				Severity: severityForDelta(bs.AvgLatencyMs-bSpec.MaxLatencyMs, bSpec.MaxLatencyMs*0.1, bSpec.MaxLatencyMs*0.3),
				Kind:     string(kind),
			})
		}
		if bs.ErrorRate > bSpec.MaxErrorRate {
			gaps = append(gaps, PerformanceGap{
				Metric:   "error_rate",
				Target:   bSpec.MaxErrorRate,
				Actual:   bs.ErrorRate,
				Delta:    bs.ErrorRate - bSpec.MaxErrorRate,
				Severity: severityForDelta(bs.ErrorRate-bSpec.MaxErrorRate, 0.02, 0.08),
				Kind:     string(kind),
			})
		}
		if bs.Load > bSpec.MaxLoad {
			gaps = append(gaps, PerformanceGap{
				Metric:   "load",
				Target:   bSpec.MaxLoad,
				Actual:   bs.Load,
				Delta:    bs.Load - bSpec.MaxLoad,
				Severity: severityForDelta(bs.Load-bSpec.MaxLoad, 0.05, 0.15),
				Kind:     string(kind),
			})
		}
	}

	return gaps
}

// severityForDelta 根据偏差大小判断严重级别。
func severityForDelta(delta, warningThreshold, criticalThreshold float64) string {
	if delta < 0 {
		delta = -delta
	}
	if delta >= criticalThreshold {
		return "critical"
	}
	if delta >= warningThreshold {
		return "warning"
	}
	return "ok"
}

// String 返回 PerformanceGap 的可读描述。
func (pg PerformanceGap) String() string {
	if pg.Kind != "" {
		return fmt.Sprintf("[%s] %s: target=%.3f actual=%.3f delta=%.3f severity=%s",
			pg.Kind, pg.Metric, pg.Target, pg.Actual, pg.Delta, pg.Severity)
	}
	return fmt.Sprintf("[global] %s: target=%.3f actual=%.3f delta=%.3f severity=%s",
		pg.Metric, pg.Target, pg.Actual, pg.Delta, pg.Severity)
}
