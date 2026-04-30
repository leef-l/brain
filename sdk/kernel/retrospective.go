package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// MACCS Wave 3 Batch 3 — 复盘引擎
//
// 在闭环工作流的最后阶段，对整个项目进行复盘分析，提取经验教训并反馈到
// 学习系统。与 MetaCognitiveEngine 互补：后者关注单阶段执行反思，复盘引擎
// 关注跨阶段全局视角。
// ─────────────────────────────────────────────────────────────────────────────

// RetroInput 复盘输入，汇聚整个项目 Session 的执行数据。
type RetroInput struct {
	Session       *ProjectSession    `json:"session"`
	ExecProgress  *ExecutionProgress `json:"exec_progress,omitempty"`
	AcceptReport  *AcceptanceReport  `json:"accept_report,omitempty"`
	TotalDuration time.Duration      `json:"total_duration"`
	TaskResults   []RetroTaskResult  `json:"task_results"`
}

// RetroTaskResult 单个任务的复盘结果快照。
type RetroTaskResult struct {
	TaskID    string        `json:"task_id"`
	BrainKind string        `json:"brain_kind"`
	Success   bool          `json:"success"`
	TurnsUsed int           `json:"turns_used"`
	Duration  time.Duration `json:"duration"`
	ErrorMsg  string        `json:"error_msg,omitempty"`
}

// RetroReport 复盘报告，包含阶段分析、大脑表现、教训和指标。
type RetroReport struct {
	ReportID        string             `json:"report_id"`
	ProjectID       string             `json:"project_id"`
	SessionID       string             `json:"session_id"`
	PhaseAnalysis   []PhaseAnalysis    `json:"phase_analysis"`
	BrainAnalysis   []BrainPerformance `json:"brain_analysis"`
	Lessons         []RetroLesson      `json:"lessons"`
	Metrics         RetroMetrics       `json:"metrics"`
	Recommendations []string           `json:"recommendations"`
	GeneratedAt     time.Time          `json:"generated_at"`
}

// PhaseAnalysis 单个阶段的复盘分析。
type PhaseAnalysis struct {
	Phase    string        `json:"phase"`
	Duration time.Duration `json:"duration"`
	Status   string        `json:"status"`
	Issues   []string      `json:"issues,omitempty"`
	Score    float64       `json:"score"` // 0-100
}

// BrainPerformance 某类 brain 的整体表现分析。
type BrainPerformance struct {
	BrainKind    string  `json:"brain_kind"`
	TaskCount    int     `json:"task_count"`
	SuccessCount int     `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"` // 0-100
	AvgTurns     float64 `json:"avg_turns"`
	TotalTurns   int     `json:"total_turns"`
}

// RetroLesson 复盘教训，独立于 meta_cognitive.go 的 Lesson 类型。
type RetroLesson struct {
	LessonID    string  `json:"lesson_id"`
	Category    string  `json:"category"` // process/technical/collaboration/resource
	Title       string  `json:"title"`
	Description string  `json:"description"`
	ActionItem  string  `json:"action_item"` // 可执行的改进措施
	Importance  float64 `json:"importance"`  // 0-1
}

// RetroMetrics 复盘综合指标。
type RetroMetrics struct {
	TotalTasks         int           `json:"total_tasks"`
	SuccessfulTasks    int           `json:"successful_tasks"`
	FailedTasks        int           `json:"failed_tasks"`
	TaskSuccessRate    float64       `json:"task_success_rate"`
	TotalTurnsUsed     int           `json:"total_turns_used"`
	AvgTurnsPerTask    float64       `json:"avg_turns_per_task"`
	AcceptancePassRate float64       `json:"acceptance_pass_rate"`
	ProjectDuration    time.Duration `json:"project_duration"`
	EfficiencyScore    float64       `json:"efficiency_score"` // 综合效率评分 0-100
}

// ─────────────────────────────────────────────────────────────────────────────
// RetrospectiveEngine 接口
// ─────────────────────────────────────────────────────────────────────────────

// RetrospectiveEngine 复盘引擎接口。
type RetrospectiveEngine interface {
	Analyze(ctx context.Context, input *RetroInput) (*RetroReport, error)
	ExtractLessons(report *RetroReport) []RetroLesson
	FormatSummary(report *RetroReport) string
}

// ─────────────────────────────────────────────────────────────────────────────
// DefaultRetrospectiveEngine 实现
// ─────────────────────────────────────────────────────────────────────────────

// DefaultRetrospectiveEngine 基于规则的复盘引擎默认实现。
type DefaultRetrospectiveEngine struct{}

// NewRetrospectiveEngine 创建默认复盘引擎。
func NewRetrospectiveEngine() *DefaultRetrospectiveEngine {
	return &DefaultRetrospectiveEngine{}
}

// Analyze 对整个项目执行复盘分析，生成复盘报告。
func (e *DefaultRetrospectiveEngine) Analyze(_ context.Context, input *RetroInput) (*RetroReport, error) {
	if input == nil {
		return nil, fmt.Errorf("retrospective: input 不能为空")
	}
	if input.Session == nil {
		return nil, fmt.Errorf("retrospective: session 不能为空")
	}

	report := &RetroReport{
		ReportID:    fmt.Sprintf("retro-%d", time.Now().UnixNano()),
		ProjectID:   input.Session.ProjectID,
		SessionID:   input.Session.SessionID,
		GeneratedAt: time.Now(),
	}

	// 1. 阶段分析
	report.PhaseAnalysis = e.analyzePhases(input.Session)

	// 2. 大脑表现分析
	report.BrainAnalysis = e.analyzeBrains(input.TaskResults)

	// 3. 计算综合指标
	report.Metrics = e.computeMetrics(input)

	// 4. 提取教训
	report.Lessons = e.ExtractLessons(report)

	// 5. 生成建议
	report.Recommendations = e.generateRecommendations(report)

	return report, nil
}

// analyzePhases 分析每个阶段的耗时和状态。
func (e *DefaultRetrospectiveEngine) analyzePhases(session *ProjectSession) []PhaseAnalysis {
	var analyses []PhaseAnalysis

	for _, phase := range phaseOrder {
		rec := session.GetPhaseRecord(phase)
		if rec == nil {
			continue
		}

		pa := PhaseAnalysis{
			Phase:  string(phase),
			Status: rec.Status,
		}

		// 计算阶段耗时
		if rec.StartedAt != nil {
			if rec.EndedAt != nil {
				pa.Duration = rec.EndedAt.Sub(*rec.StartedAt)
			} else {
				pa.Duration = time.Since(*rec.StartedAt)
			}
		}

		// 识别问题
		if rec.Error != "" {
			pa.Issues = append(pa.Issues, rec.Error)
		}

		// 评分
		pa.Score = e.scorePhase(rec)

		analyses = append(analyses, pa)
	}

	return analyses
}

// scorePhase 为单个阶段打分 (0-100)。
func (e *DefaultRetrospectiveEngine) scorePhase(rec *PhaseRecord) float64 {
	switch rec.Status {
	case "completed":
		return 100
	case "skipped":
		return 80 // 跳过视为可接受
	case "failed":
		return 20
	case "running":
		return 50
	default: // pending
		return 0
	}
}

// analyzeBrains 按 brain_kind 分组分析表现。
func (e *DefaultRetrospectiveEngine) analyzeBrains(results []RetroTaskResult) []BrainPerformance {
	// 按 brain_kind 分组
	groups := make(map[string]*BrainPerformance)
	for _, r := range results {
		bp, ok := groups[r.BrainKind]
		if !ok {
			bp = &BrainPerformance{BrainKind: r.BrainKind}
			groups[r.BrainKind] = bp
		}
		bp.TaskCount++
		bp.TotalTurns += r.TurnsUsed
		if r.Success {
			bp.SuccessCount++
		}
	}

	var out []BrainPerformance
	for _, bp := range groups {
		if bp.TaskCount > 0 {
			bp.SuccessRate = float64(bp.SuccessCount) / float64(bp.TaskCount) * 100
			bp.AvgTurns = float64(bp.TotalTurns) / float64(bp.TaskCount)
		}
		out = append(out, *bp)
	}
	return out
}

// computeMetrics 计算综合指标。
func (e *DefaultRetrospectiveEngine) computeMetrics(input *RetroInput) RetroMetrics {
	m := RetroMetrics{
		TotalTasks:      len(input.TaskResults),
		ProjectDuration: input.TotalDuration,
	}

	for _, r := range input.TaskResults {
		m.TotalTurnsUsed += r.TurnsUsed
		if r.Success {
			m.SuccessfulTasks++
		} else {
			m.FailedTasks++
		}
	}

	if m.TotalTasks > 0 {
		m.TaskSuccessRate = float64(m.SuccessfulTasks) / float64(m.TotalTasks) * 100
		m.AvgTurnsPerTask = float64(m.TotalTurnsUsed) / float64(m.TotalTasks)
	}

	// 验收通过率
	if input.AcceptReport != nil {
		m.AcceptancePassRate = input.AcceptReport.PassRate
	}

	// 综合效率评分: 加权 (任务成功率 40% + 验收通过率 30% + turn 效率 30%)
	turnEfficiency := 100.0
	if m.AvgTurnsPerTask > 0 {
		// 假设平均 10 turns/task 为标准，低于标准得满分，超出按比例扣分
		turnEfficiency = 100.0 * (10.0 / (m.AvgTurnsPerTask + 10.0)) * 2
		if turnEfficiency > 100 {
			turnEfficiency = 100
		}
	}
	m.EfficiencyScore = m.TaskSuccessRate*0.4 + m.AcceptancePassRate*0.3 + turnEfficiency*0.3

	return m
}

// ExtractLessons 从分析结果中提取教训。
func (e *DefaultRetrospectiveEngine) ExtractLessons(report *RetroReport) []RetroLesson {
	var lessons []RetroLesson
	lessonIdx := 0

	nextID := func() string {
		lessonIdx++
		return fmt.Sprintf("rl-%d", lessonIdx)
	}

	// ── 大脑协作类教训 ─────────────────────────────────────────────────────
	for _, bp := range report.BrainAnalysis {
		if bp.TaskCount > 0 && bp.SuccessRate < 50 {
			lessons = append(lessons, RetroLesson{
				LessonID:    nextID(),
				Category:    "collaboration",
				Title:       fmt.Sprintf("%s 成功率偏低", bp.BrainKind),
				Description: fmt.Sprintf("%s 执行 %d 个任务，成功率仅 %.0f%%，需要排查能力匹配问题", bp.BrainKind, bp.TaskCount, bp.SuccessRate),
				ActionItem:  fmt.Sprintf("重新评估 %s 的任务分配策略，考虑引入能力预检", bp.BrainKind),
				Importance:  0.9,
			})
		}
	}

	// ── 流程类教训 ───────────────────────────────────────────────────────────
	for _, pa := range report.PhaseAnalysis {
		if pa.Status == "failed" {
			lessons = append(lessons, RetroLesson{
				LessonID:    nextID(),
				Category:    "process",
				Title:       fmt.Sprintf("阶段 %s 执行失败", pa.Phase),
				Description: fmt.Sprintf("阶段 %s 失败，相关问题: %s", pa.Phase, strings.Join(pa.Issues, "; ")),
				ActionItem:  fmt.Sprintf("为 %s 阶段增加前置校验和失败恢复机制", pa.Phase),
				Importance:  0.85,
			})
		}
		if pa.Duration > 10*time.Minute && pa.Status == "completed" {
			lessons = append(lessons, RetroLesson{
				LessonID:    nextID(),
				Category:    "process",
				Title:       fmt.Sprintf("阶段 %s 耗时过长", pa.Phase),
				Description: fmt.Sprintf("阶段 %s 耗时 %s，超过 10 分钟阈值", pa.Phase, pa.Duration.Round(time.Second)),
				ActionItem:  "分析耗时瓶颈，考虑拆分子步骤或引入并行处理",
				Importance:  0.6,
			})
		}
	}

	// ── 资源类教训 ───────────────────────────────────────────────────────────
	if report.Metrics.TotalTasks > 0 && report.Metrics.AvgTurnsPerTask > 20 {
		lessons = append(lessons, RetroLesson{
			LessonID:    nextID(),
			Category:    "resource",
			Title:       "平均 turn 消耗超预期",
			Description: fmt.Sprintf("平均每任务消耗 %.1f turns，超过 20 turns 阈值", report.Metrics.AvgTurnsPerTask),
			ActionItem:  "优化提示词精度，减少无效对话轮次；对高消耗任务启用早停策略",
			Importance:  0.75,
		})
	}

	// ── 技术类教训 ───────────────────────────────────────────────────────────
	if report.Metrics.AcceptancePassRate > 0 && report.Metrics.AcceptancePassRate < 80 {
		lessons = append(lessons, RetroLesson{
			LessonID:    nextID(),
			Category:    "technical",
			Title:       "验收通过率不达标",
			Description: fmt.Sprintf("验收通过率 %.1f%%，低于 80%% 的目标阈值", report.Metrics.AcceptancePassRate),
			ActionItem:  "加强执行阶段的中间检查，引入增量验证而非仅在末尾验收",
			Importance:  0.8,
		})
	}

	return lessons
}

// generateRecommendations 基于复盘报告生成改进建议。
func (e *DefaultRetrospectiveEngine) generateRecommendations(report *RetroReport) []string {
	var recs []string

	m := report.Metrics

	if m.TaskSuccessRate < 50 {
		recs = append(recs, "任务成功率低于 50%，建议重新评估任务拆分和 brain 分配策略")
	} else if m.TaskSuccessRate < 80 {
		recs = append(recs, "任务成功率未达 80%，建议加强失败任务的根因分析和重试机制")
	}

	if m.EfficiencyScore < 60 {
		recs = append(recs, "综合效率评分偏低，建议优化工作流管线、缩减冗余步骤")
	}

	if m.AcceptancePassRate > 0 && m.AcceptancePassRate < 80 {
		recs = append(recs, "验收通过率不足，建议增加执行阶段的中间验证节点")
	}

	failedPhases := 0
	for _, pa := range report.PhaseAnalysis {
		if pa.Status == "failed" {
			failedPhases++
		}
	}
	if failedPhases > 0 {
		recs = append(recs, fmt.Sprintf("%d 个阶段执行失败，建议完善阶段间的容错和回退机制", failedPhases))
	}

	for _, bp := range report.BrainAnalysis {
		if bp.TaskCount >= 3 && bp.SuccessRate < 50 {
			recs = append(recs, fmt.Sprintf("%s 的成功率仅 %.0f%%（%d 个任务），建议运行 L1 RankBrains 重新校准", bp.BrainKind, bp.SuccessRate, bp.TaskCount))
		}
	}

	if len(recs) == 0 {
		recs = append(recs, "本次项目各项指标正常，保持当前策略")
	}

	return recs
}

// FormatSummary 生成人类可读的复盘摘要。
func (e *DefaultRetrospectiveEngine) FormatSummary(report *RetroReport) string {
	var sb strings.Builder

	sb.WriteString("═══ 项目复盘报告 ═══\n")
	sb.WriteString(fmt.Sprintf("项目: %s | Session: %s\n", report.ProjectID, report.SessionID))
	sb.WriteString(fmt.Sprintf("生成时间: %s\n\n", report.GeneratedAt.Format(time.RFC3339)))

	// 综合指标
	m := report.Metrics
	sb.WriteString("── 综合指标 ──\n")
	sb.WriteString(fmt.Sprintf("  任务: %d 总计 | %d 成功 | %d 失败 | 成功率 %.1f%%\n",
		m.TotalTasks, m.SuccessfulTasks, m.FailedTasks, m.TaskSuccessRate))
	sb.WriteString(fmt.Sprintf("  Turns: %d 总计 | 平均 %.1f/任务\n", m.TotalTurnsUsed, m.AvgTurnsPerTask))
	sb.WriteString(fmt.Sprintf("  验收通过率: %.1f%% | 效率评分: %.1f/100\n", m.AcceptancePassRate, m.EfficiencyScore))
	sb.WriteString(fmt.Sprintf("  项目耗时: %s\n\n", m.ProjectDuration.Round(time.Second)))

	// 阶段分析
	if len(report.PhaseAnalysis) > 0 {
		sb.WriteString("── 阶段分析 ──\n")
		for _, pa := range report.PhaseAnalysis {
			sb.WriteString(fmt.Sprintf("  [%s] 状态=%s 耗时=%s 评分=%.0f",
				pa.Phase, pa.Status, pa.Duration.Round(time.Second), pa.Score))
			if len(pa.Issues) > 0 {
				sb.WriteString(fmt.Sprintf(" 问题=%s", strings.Join(pa.Issues, ",")))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// 大脑表现
	if len(report.BrainAnalysis) > 0 {
		sb.WriteString("── 大脑表现 ──\n")
		for _, bp := range report.BrainAnalysis {
			sb.WriteString(fmt.Sprintf("  [%s] 任务=%d 成功=%d 成功率=%.0f%% 平均turns=%.1f\n",
				bp.BrainKind, bp.TaskCount, bp.SuccessCount, bp.SuccessRate, bp.AvgTurns))
		}
		sb.WriteString("\n")
	}

	// 教训
	if len(report.Lessons) > 0 {
		sb.WriteString("── 经验教训 ──\n")
		for _, l := range report.Lessons {
			sb.WriteString(fmt.Sprintf("  [%s] (%.0f%%) %s — %s\n",
				l.Category, l.Importance*100, l.Title, l.ActionItem))
		}
		sb.WriteString("\n")
	}

	// 建议
	if len(report.Recommendations) > 0 {
		sb.WriteString("── 改进建议 ──\n")
		for i, rec := range report.Recommendations {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, rec))
		}
	}

	return sb.String()
}
