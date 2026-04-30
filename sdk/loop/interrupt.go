package loop

import "context"

// RunInterruptChecker 是 Runner 级别的中断检查接口。
// 由 kernel 层实现并注入到 Runner，避免 loop → kernel 循环依赖。
// 这与 ToolBatchPlanner 的解耦模式一致。
type RunInterruptChecker interface {
	// CheckInterrupt 检查指定 runID 是否有中断信号。
	// 返回 nil 表示无中断。
	CheckInterrupt(ctx context.Context, runID string) *RunInterruptSignal
}

// RunInterruptSignal 是 loop 包内的中断信号表示。
// 与 kernel.InterruptSignal 字段对齐，但独立定义避免依赖。
type RunInterruptSignal struct {
	SignalID string `json:"signal_id"`
	Type     string `json:"type"`   // "plan_changed"/"emergency_stop"/etc
	Action   string `json:"action"` // "pause"/"stop"/"restart"
	Reason   string `json:"reason"`
}
