// learner.go — DesktopBrain 领域特化 L0 学习器
//
// 通过 ToolOutcomeRecorder 接口接收每次桌面工具调用的结果，
// 追踪窗口操作、应用启动、快捷键发送三类核心操作的成功率，
// 为 Adapt() 提供闭环领域信号。
package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
)

// DesktopBrainLearner 是 Desktop Brain 的领域特化 L0 学习器。
type DesktopBrainLearner struct {
	mu           sync.Mutex
	taskCount    int
	successCount int
	openCount    int
	openOk       int
	windowCount  int
	windowOk     int
	hotkeyCount  int
	hotkeyOk     int
	latencyEWMA  kernel.EWMAScore
	startTime    time.Time

	// adaptSuggestion 由 Adapt() 生成，描述最近一次自适应决策。
	adaptSuggestion string
}

// NewDesktopBrainLearner 创建领域特化学习器实例。
func NewDesktopBrainLearner() *DesktopBrainLearner {
	return &DesktopBrainLearner{
		latencyEWMA: kernel.EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// RecordOutcome 记录通用任务结果。
func (dl *DesktopBrainLearner) RecordOutcome(_ context.Context, outcome kernel.TaskOutcome) error {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.taskCount++
	if outcome.Success {
		dl.successCount++
	}
	dl.latencyEWMA.Update(outcome.Duration.Seconds())
	return nil
}

// RecordToolOutcome 根据 tool name 分类更新领域指标。
// 实现 kernel.ToolOutcomeRecorder 接口。
func (dl *DesktopBrainLearner) RecordToolOutcome(toolName string, success bool) {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	switch {
	case strings.Contains(toolName, "open_path"):
		dl.openCount++
		if success {
			dl.openOk++
		}
	case strings.Contains(toolName, "list_windows"):
		dl.windowCount++
		if success {
			dl.windowOk++
		}
	case strings.Contains(toolName, "send_hotkey"):
		dl.hotkeyCount++
		if success {
			dl.hotkeyOk++
		}
	}
}

// ExportMetrics 导出标准 BrainMetrics，综合领域指标计算趋势。
func (dl *DesktopBrainLearner) ExportMetrics() kernel.BrainMetrics {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	successRate := 0.0
	if dl.taskCount > 0 {
		successRate = float64(dl.successCount) / float64(dl.taskCount)
	}

	trend := successRate - 0.5
	totalDomain := dl.openCount + dl.windowCount + dl.hotkeyCount
	if totalDomain > 0 {
		domainOk := dl.openOk + dl.windowOk + dl.hotkeyOk
		domainRate := float64(domainOk) / float64(totalDomain)
		trend = trend*0.5 + (domainRate-0.5)*0.5
	}

	return kernel.BrainMetrics{
		BrainKind:       agent.KindDesktop,
		Period:          time.Since(dl.startTime),
		TaskCount:       dl.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    dl.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 根据历史指标生成自适应建议。
// 当某类工具成功率持续低于阈值时，建议调整策略或检查系统环境。
func (dl *DesktopBrainLearner) Adapt(_ context.Context) error {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	openRate := 0.0
	if dl.openCount > 0 {
		openRate = float64(dl.openOk) / float64(dl.openCount)
	}
	windowRate := 0.0
	if dl.windowCount > 0 {
		windowRate = float64(dl.windowOk) / float64(dl.windowCount)
	}
	hotkeyRate := 0.0
	if dl.hotkeyCount > 0 {
		hotkeyRate = float64(dl.hotkeyOk) / float64(dl.hotkeyCount)
	}

	var suggestions []string

	if dl.openCount >= 5 && openRate < 0.5 {
		suggestions = append(suggestions, "open_path_rate_low: suggest checking file paths and OS default handlers")
	}
	if dl.windowCount >= 5 && windowRate < 0.5 {
		suggestions = append(suggestions, "list_windows_rate_low: suggest checking window manager availability (xdotool/wmctrl on Linux)")
	}
	if dl.hotkeyCount >= 5 && hotkeyRate < 0.5 {
		suggestions = append(suggestions, "send_hotkey_rate_low: suggest verifying target window focus and key sequence validity")
	}

	if len(suggestions) == 0 {
		dl.adaptSuggestion = "desktop_metrics_healthy: maintain current strategy"
	} else {
		dl.adaptSuggestion = strings.Join(suggestions, "; ")
	}
	return nil
}

// LastAdaptSuggestion 返回最近一次 Adapt() 生成的建议文本。
func (dl *DesktopBrainLearner) LastAdaptSuggestion() string {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.adaptSuggestion
}

var _ kernel.BrainLearner = (*DesktopBrainLearner)(nil)
var _ kernel.ToolOutcomeRecorder = (*DesktopBrainLearner)(nil)
