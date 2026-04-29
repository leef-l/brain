// control_law.go — 控制律综合器（Control Law Synthesizer）
//
// 钱学森《工程控制论》主线4（性能指标驱动）的算法核心。
// ControlLawSynthesizer 接收 PerformanceGap、SystemState 和 PI 历史状态，
// 输出具体的 ControlAction。
//
// 当前实现采用简化 PI 控制律：
//   control = Kp * error + Ki * integral_error
//   当 control 超过阈值时，映射为 Throttle / Boost / Restart / Reroute / Alert 动作。
package kernel

import (
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// ControlLawSynthesizer — 控制律综合器
// ---------------------------------------------------------------------------

// ControlLawSynthesizer 根据性能差距和系统状态综合控制动作。
type ControlLawSynthesizer struct {
	// PI 控制参数（可公开调整）
	Kp float64 // 比例增益
	Ki float64 // 积分增益
	// 动作阈值
	ThrottleThreshold float64 // 超过此值触发限流
	RestartThreshold  float64 // 超过此值触发重启
	BoostThreshold    float64 // 低于此负值触发增压（提高权重）
}

// NewControlLawSynthesizer 创建默认参数的控制律综合器。
func NewControlLawSynthesizer() *ControlLawSynthesizer {
	return &ControlLawSynthesizer{
		Kp:                2.0,
		Ki:                0.5,
		ThrottleThreshold: 0.15,
		RestartThreshold:  100.0, // 临时禁用：success_rate 冷启动/样本不足时误触发重启，待学习引擎成熟后恢复
		BoostThreshold:    -0.10, // 负值表示实际优于目标（有容量可增压）
	}
}

// Synthesize 综合控制动作。
// integralErr 是该指标的累积误差，lastErr 是上次误差（供微分项扩展）。
func (cls *ControlLawSynthesizer) Synthesize(gap PerformanceGap, state *SystemState, integralErr, lastErr float64) ControlAction {
	now := time.Now().UTC()

	// 计算控制量（简化 PI）
	control := cls.Kp*gap.Delta + cls.Ki*integralErr

	// 根据指标类型和严重程度映射为动作
	action := ControlAction{
		TargetKind: gap.Kind,
		Timestamp:  now,
	}

	// 全局指标 → Alert（不直接操作单个 brain）
	if gap.Kind == "" {
		if gap.Severity == "critical" {
			action.Action = ActionAlert
			action.Priority = 8
			action.Reason = fmt.Sprintf("global %s critical: control=%.3f", gap.Metric, control)
		} else {
			action.Action = ActionNoOp
		}
		return action
	}

	// 单 brain 指标映射
	switch gap.Metric {
	case "success_rate":
		if control > cls.RestartThreshold {
			action.Action = ActionRestart
			action.Priority = 10
			action.Reason = fmt.Sprintf("success_rate too low (%.3f < %.3f), control=%.3f, restarting sidecar", gap.Actual, gap.Target, control)
		} else if control > cls.ThrottleThreshold {
			action.Action = ActionReroute
			action.Priority = 7
			action.Reason = fmt.Sprintf("success_rate degraded (%.3f < %.3f), control=%.3f, rerouting tasks", gap.Actual, gap.Target, control)
		} else if control < cls.BoostThreshold {
			// 实际成功率远高于目标（负偏差大）→ 可以增压分配更多任务
			action.Action = ActionBoost
			action.Priority = 3
			action.Reason = fmt.Sprintf("success_rate excellent (%.3f > %.3f), control=%.3f, boosting allocation", gap.Actual, gap.Target, control)
		} else {
			action.Action = ActionNoOp
		}

	case "latency":
		if control > cls.RestartThreshold*gap.Target {
			action.Action = ActionThrottle
			action.Priority = 8
			action.Reason = fmt.Sprintf("latency too high (%.1fms > %.1fms), control=%.3f, throttling", gap.Actual, gap.Target, control)
		} else if control > cls.ThrottleThreshold*gap.Target {
			action.Action = ActionThrottle
			action.Priority = 5
			action.Reason = fmt.Sprintf("latency elevated (%.1fms > %.1fms), control=%.3f, throttling", gap.Actual, gap.Target, control)
		} else {
			action.Action = ActionNoOp
		}

	case "error_rate":
		if control > cls.RestartThreshold {
			action.Action = ActionRestart
			action.Priority = 9
			action.Reason = fmt.Sprintf("error_rate critical (%.3f > %.3f), control=%.3f, restarting sidecar", gap.Actual, gap.Target, control)
		} else if control > cls.ThrottleThreshold {
			action.Action = ActionReroute
			action.Priority = 6
			action.Reason = fmt.Sprintf("error_rate high (%.3f > %.3f), control=%.3f, rerouting tasks", gap.Actual, gap.Target, control)
		} else {
			action.Action = ActionNoOp
		}

	case "load":
		if control > cls.RestartThreshold {
			action.Action = ActionThrottle
			action.Priority = 9
			action.Reason = fmt.Sprintf("load critical (%.3f > %.3f), control=%.3f, throttling", gap.Actual, gap.Target, control)
		} else if control > cls.ThrottleThreshold {
			action.Action = ActionThrottle
			action.Priority = 5
			action.Reason = fmt.Sprintf("load high (%.3f > %.3f), control=%.3f, throttling", gap.Actual, gap.Target, control)
		} else {
			action.Action = ActionNoOp
		}

	default:
		action.Action = ActionNoOp
	}

	return action
}

// ---------------------------------------------------------------------------
// ControlLawConfig — 控制律配置（供外部 JSON/YAML 注入）
// ---------------------------------------------------------------------------

// ControlLawConfig 允许外部调整控制律参数。
type ControlLawConfig struct {
	Kp                float64 `json:"kp,omitempty"`
	Ki                float64 `json:"ki,omitempty"`
	ThrottleThreshold float64 `json:"throttle_threshold,omitempty"`
	RestartThreshold  float64 `json:"restart_threshold,omitempty"`
	BoostThreshold    float64 `json:"boost_threshold,omitempty"`
}

// Apply 将配置应用到 ControlLawSynthesizer。
func (cfg *ControlLawConfig) Apply(cls *ControlLawSynthesizer) {
	if cfg == nil || cls == nil {
		return
	}
	if cfg.Kp != 0 {
		cls.Kp = cfg.Kp
	}
	if cfg.Ki != 0 {
		cls.Ki = cfg.Ki
	}
	if cfg.ThrottleThreshold != 0 {
		cls.ThrottleThreshold = cfg.ThrottleThreshold
	}
	if cfg.RestartThreshold != 0 {
		cls.RestartThreshold = cfg.RestartThreshold
	}
	if cfg.BoostThreshold != 0 {
		cls.BoostThreshold = cfg.BoostThreshold
	}
}
