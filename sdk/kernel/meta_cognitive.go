package kernel

import (
	"fmt"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// ReflectionReport — 元认知反思报告
// ─────────────────────────────────────────────────────────────────────────────

// ReflectionReport 反思报告。
type ReflectionReport struct {
	ProjectID       string          `json:"project_id"`
	PlanID          string          `json:"plan_id"`
	CreatedAt       time.Time       `json:"created_at"`
	PlanDeviation   PlanDeviation   `json:"plan_deviation"`
	TaskAnalysis    []TaskAnalysis  `json:"task_analysis"`
	BudgetAnalysis  BudgetAnalysis  `json:"budget_analysis"`
	QualityAnalysis QualityAnalysis `json:"quality_analysis"`
	Lessons         []Lesson        `json:"lessons"`
	Recommendations []string        `json:"recommendations"`
}

// PlanDeviation 计划偏差分析。
type PlanDeviation struct {
	PlannedTasks   int     `json:"planned_tasks"`
	CompletedTasks int     `json:"completed_tasks"`
	FailedTasks    int     `json:"failed_tasks"`
	CompletionRate float64 `json:"completion_rate"` // 0-1
	OnSchedule     bool    `json:"on_schedule"`
}

// TaskAnalysis 单任务分析。
type TaskAnalysis struct {
	TaskID         string  `json:"task_id"`
	TaskName       string  `json:"task_name"`
	AssignedBrain  string  `json:"assigned_brain"`
	SuggestedBrain string  `json:"suggested_brain,omitempty"` // 事后看应该分配给谁
	EstimatedTurns int     `json:"estimated_turns"`
	ActualTurns    int     `json:"actual_turns"`
	TurnAccuracy   float64 `json:"turn_accuracy"` // min(est,act)/max(est,act)
	Success        bool    `json:"success"`
	Verdict        string  `json:"verdict"` // "optimal"/"suboptimal"/"wrong_brain"/"under_budget"/"over_budget"
}

// BudgetAnalysis 预算分析。
type BudgetAnalysis struct {
	TotalBudget int     `json:"total_budget"`
	TotalUsed   int     `json:"total_used"`
	Utilization float64 `json:"utilization"` // 0-1
	WastedTurns int     `json:"wasted_turns"` // 失败任务消耗的 turns
	Verdict     string  `json:"verdict"`      // "efficient"/"wasteful"/"tight"
}

// QualityAnalysis 质量分析。
type QualityAnalysis struct {
	ReviewIterations int     `json:"review_iterations"` // 审核闭环迭代次数
	IssuesFound      int     `json:"issues_found"`
	IssuesFixed      int     `json:"issues_fixed"`
	FixRate          float64 `json:"fix_rate"` // 0-1
	Verdict          string  `json:"verdict"`
}

// Lesson 经验教训。
type Lesson struct {
	Category    string  `json:"category"`   // brain_selection/budget/quality/workflow
	Description string  `json:"description"`
	Actionable  bool    `json:"actionable"` // 是否可以转化为自动化规则
	Importance  float64 `json:"importance"` // 0-1
}

// ─────────────────────────────────────────────────────────────────────────────
// MetaCognitiveEngine — 元认知反思引擎
// ─────────────────────────────────────────────────────────────────────────────

// MetaCognitiveEngine 元认知反思引擎。
// 每完成一个项目阶段，中央大脑调用 Reflect 自动反思：原计划 vs 实际、
// 任务分配是否最优、预算是否合理、质量门是否漏检，并将经验写入学习引擎。
type MetaCognitiveEngine struct {
	learner *LearningEngine
}

// NewMetaCognitiveEngine 创建元认知反思引擎。
func NewMetaCognitiveEngine(learner *LearningEngine) *MetaCognitiveEngine {
	return &MetaCognitiveEngine{learner: learner}
}

// Reflect 对已完成的项目阶段进行反思分析。
// progress 可能为 nil，此时返回仅基于 plan 本身的分析（无实际执行数据）。
func (e *MetaCognitiveEngine) Reflect(plan *TaskPlan, progress *ProjectProgress) *ReflectionReport {
	report := &ReflectionReport{
		ProjectID: plan.ProjectID,
		PlanID:    plan.PlanID,
		CreatedAt: time.Now(),
	}

	// 1. 计划偏差分析
	report.PlanDeviation = e.analyzePlanDeviation(plan, progress)

	// 2. 单任务分析
	report.TaskAnalysis = e.analyzeTaskPerformance(plan, progress)

	// 3. 预算分析
	report.BudgetAnalysis = e.analyzeBudget(plan, progress)

	// 4. 质量分析
	report.QualityAnalysis = e.analyzeQuality(plan, progress)

	// 5. 从分析中提取经验教训
	report.Lessons = e.GenerateLessons(report)

	// 6. 给出改进建议
	report.Recommendations = e.generateRecommendations(report)

	return report
}

// analyzePlanDeviation 统计计划偏差：planned/completed/failed/completionRate。
func (e *MetaCognitiveEngine) analyzePlanDeviation(plan *TaskPlan, progress *ProjectProgress) PlanDeviation {
	dev := PlanDeviation{
		PlannedTasks: len(plan.SubTasks),
	}

	if dev.PlannedTasks == 0 {
		return dev
	}

	// 从 plan 的 SubTasks 状态统计
	for _, st := range plan.SubTasks {
		switch st.Status {
		case PlanTaskCompleted:
			dev.CompletedTasks++
		case PlanTaskFailed:
			dev.FailedTasks++
		}
	}

	// 如果 progress 不为 nil，用更精确的已完成任务数据补充
	if progress != nil {
		completedFromProgress := len(progress.CompletedTasks)
		if completedFromProgress > dev.CompletedTasks {
			dev.CompletedTasks = completedFromProgress
		}
	}

	dev.CompletionRate = float64(dev.CompletedTasks) / float64(dev.PlannedTasks)

	// 判断是否按计划进行：完成率 >= 80% 且失败率 < 20%
	failRate := float64(dev.FailedTasks) / float64(dev.PlannedTasks)
	dev.OnSchedule = dev.CompletionRate >= 0.8 && failRate < 0.2

	return dev
}

// analyzeTaskPerformance 遍历每个子任务，对比预估 vs 实际。
func (e *MetaCognitiveEngine) analyzeTaskPerformance(plan *TaskPlan, progress *ProjectProgress) []TaskAnalysis {
	if len(plan.SubTasks) == 0 {
		return nil
	}

	// 构建 progress 中已完成任务的查找表：taskID → TaskSummary
	completedMap := make(map[string]TaskSummary)
	if progress != nil {
		for _, ts := range progress.CompletedTasks {
			completedMap[ts.TaskID] = ts
		}
	}

	analyses := make([]TaskAnalysis, 0, len(plan.SubTasks))
	for _, st := range plan.SubTasks {
		ta := TaskAnalysis{
			TaskID:         st.TaskID,
			TaskName:       st.Name,
			AssignedBrain:  string(st.Kind),
			EstimatedTurns: st.EstimatedTurns,
		}

		// 从 progress 获取实际数据
		if summary, ok := completedMap[st.TaskID]; ok {
			ta.ActualTurns = summary.TurnsUsed
			ta.Success = summary.Success
		} else {
			// 没有 progress 数据：从 plan status 判断
			ta.Success = st.Status == PlanTaskCompleted
			ta.ActualTurns = st.EstimatedTurns // 无实际数据，假设等于预估
		}

		// 计算 turn 准确度: min(estimated, actual) / max(estimated, actual)
		ta.TurnAccuracy = computeTurnAccuracy(ta.EstimatedTurns, ta.ActualTurns)

		// 判定 verdict
		ta.Verdict = e.judgeTaskVerdict(ta)

		analyses = append(analyses, ta)
	}

	return analyses
}

// computeTurnAccuracy 计算 turn 预估准确度。
// 返回 min(est, act) / max(est, act)，两者都为 0 时返回 1.0。
func computeTurnAccuracy(estimated, actual int) float64 {
	if estimated == 0 && actual == 0 {
		return 1.0
	}
	minVal := estimated
	maxVal := actual
	if estimated > actual {
		minVal = actual
		maxVal = estimated
	}
	if maxVal == 0 {
		return 0
	}
	return float64(minVal) / float64(maxVal)
}

// judgeTaskVerdict 根据任务分析结果给出判定。
func (e *MetaCognitiveEngine) judgeTaskVerdict(ta TaskAnalysis) string {
	if !ta.Success {
		return "wrong_brain"
	}

	switch {
	case ta.TurnAccuracy >= 0.8:
		return "optimal"
	case ta.TurnAccuracy >= 0.5:
		// 区分是预估偏低还是偏高
		if ta.ActualTurns > ta.EstimatedTurns {
			return "over_budget"
		}
		return "under_budget"
	default:
		return "suboptimal"
	}
}

// analyzeBudget 统计总预算 vs 总使用 vs 浪费。
func (e *MetaCognitiveEngine) analyzeBudget(plan *TaskPlan, progress *ProjectProgress) BudgetAnalysis {
	ba := BudgetAnalysis{
		TotalBudget: plan.Budget.TotalTurns,
	}

	if ba.TotalBudget == 0 {
		// 如果没设计划级预算，从子任务预估汇总
		for _, st := range plan.SubTasks {
			ba.TotalBudget += st.EstimatedTurns
		}
	}

	// 从 progress 获取实际使用量
	if progress != nil {
		ba.TotalUsed = progress.ResourceUsage.TotalTurnsUsed

		// 计算浪费的 turns：失败任务消耗的 turns
		for _, ts := range progress.CompletedTasks {
			if !ts.Success {
				ba.WastedTurns += ts.TurnsUsed
			}
		}
	} else {
		// 无 progress，从 plan 的 Budget 字段获取
		ba.TotalUsed = plan.Budget.UsedTurns
	}

	// 计算利用率
	if ba.TotalBudget > 0 {
		ba.Utilization = float64(ba.TotalUsed) / float64(ba.TotalBudget)
	}

	// 判定 verdict
	switch {
	case ba.TotalBudget == 0:
		ba.Verdict = "no_budget"
	case ba.Utilization > 0.9:
		ba.Verdict = "tight"
	case ba.Utilization >= 0.6:
		ba.Verdict = "efficient"
	default:
		ba.Verdict = "wasteful"
	}

	return ba
}

// analyzeQuality 基于 progress 中的质量门禁和任务结果分析质量。
func (e *MetaCognitiveEngine) analyzeQuality(plan *TaskPlan, progress *ProjectProgress) QualityAnalysis {
	qa := QualityAnalysis{}

	// 从 plan 的 checkpoints 统计审核迭代
	for _, cp := range plan.Checkpoints {
		if cp.Required {
			qa.ReviewIterations++
		}
	}

	// 从子任务的 Result 中统计 issues
	for _, st := range plan.SubTasks {
		if st.Result == nil {
			continue
		}
		qa.IssuesFound += len(st.Result.Issues)
		for _, issue := range st.Result.Issues {
			if issue.AutoFixable || issue.SuggestedFix != "" {
				qa.IssuesFixed++
			}
		}
	}

	// 从 progress 的 QualityGates 中补充
	if progress != nil {
		for _, gate := range progress.QualityGates {
			if gate.Status == "failed" {
				qa.IssuesFound++
			} else if gate.Status == "passed" {
				qa.IssuesFixed++
			}
		}
	}

	// 计算修复率
	if qa.IssuesFound > 0 {
		qa.FixRate = float64(qa.IssuesFixed) / float64(qa.IssuesFound)
		if qa.FixRate > 1.0 {
			qa.FixRate = 1.0
		}
	} else {
		qa.FixRate = 1.0 // 没有发现问题 = 全部通过
	}

	// 判定 verdict
	switch {
	case qa.IssuesFound == 0:
		qa.Verdict = "clean"
	case qa.FixRate >= 0.9:
		qa.Verdict = "well_maintained"
	case qa.FixRate >= 0.6:
		qa.Verdict = "needs_attention"
	default:
		qa.Verdict = "quality_risk"
	}

	return qa
}

// GenerateLessons 从分析中提取经验教训。
func (e *MetaCognitiveEngine) GenerateLessons(report *ReflectionReport) []Lesson {
	var lessons []Lesson

	// ── brain 选择相关经验 ──────────────────────────────────────────────────
	wrongBrainCount := 0
	suboptimalCount := 0
	for _, ta := range report.TaskAnalysis {
		switch ta.Verdict {
		case "wrong_brain":
			wrongBrainCount++
			lessons = append(lessons, Lesson{
				Category: "brain_selection",
				Description: fmt.Sprintf(
					"任务 %q 分配给 %s 但执行失败，建议重新评估该类任务的 brain 选择策略",
					ta.TaskName, ta.AssignedBrain,
				),
				Actionable: true,
				Importance: 0.9,
			})
		case "suboptimal":
			suboptimalCount++
		}
	}

	if wrongBrainCount > 0 {
		lessons = append(lessons, Lesson{
			Category: "brain_selection",
			Description: fmt.Sprintf(
				"本轮有 %d 个任务因 brain 选择不当而失败，建议加强 L1 能力画像更新频率",
				wrongBrainCount,
			),
			Actionable: true,
			Importance: 0.95,
		})
	}

	if suboptimalCount > 0 {
		lessons = append(lessons, Lesson{
			Category: "brain_selection",
			Description: fmt.Sprintf(
				"%d 个任务预估偏差过大（准确度 < 50%%），需要校准复杂度估算模型",
				suboptimalCount,
			),
			Actionable: true,
			Importance: 0.7,
		})
	}

	// ── 预算相关经验 ──────────────────────────────────────────────────────────
	ba := report.BudgetAnalysis
	switch ba.Verdict {
	case "wasteful":
		lessons = append(lessons, Lesson{
			Category: "budget",
			Description: fmt.Sprintf(
				"预算利用率仅 %.0f%%（%d/%d turns），存在资源浪费，建议收紧预算或合并小任务",
				ba.Utilization*100, ba.TotalUsed, ba.TotalBudget,
			),
			Actionable: true,
			Importance: 0.7,
		})
	case "tight":
		lessons = append(lessons, Lesson{
			Category: "budget",
			Description: fmt.Sprintf(
				"预算利用率 %.0f%%，接近极限，建议增加 10-20%% 的缓冲预算",
				ba.Utilization*100,
			),
			Actionable: true,
			Importance: 0.8,
		})
	}

	if ba.WastedTurns > 0 {
		lessons = append(lessons, Lesson{
			Category: "budget",
			Description: fmt.Sprintf(
				"失败任务浪费了 %d turns，建议加强失败检测和早停机制",
				ba.WastedTurns,
			),
			Actionable: true,
			Importance: 0.85,
		})
	}

	// ── 质量相关经验 ──────────────────────────────────────────────────────────
	qa := report.QualityAnalysis
	switch qa.Verdict {
	case "quality_risk":
		lessons = append(lessons, Lesson{
			Category: "quality",
			Description: fmt.Sprintf(
				"问题修复率仅 %.0f%%（%d/%d），质量门禁未能有效拦截问题，建议增加必检检查点",
				qa.FixRate*100, qa.IssuesFixed, qa.IssuesFound,
			),
			Actionable: true,
			Importance: 0.9,
		})
	case "needs_attention":
		lessons = append(lessons, Lesson{
			Category: "quality",
			Description: fmt.Sprintf(
				"发现 %d 个问题，修复率 %.0f%%，建议关注未修复问题并完善自动修复能力",
				qa.IssuesFound, qa.FixRate*100,
			),
			Actionable: true,
			Importance: 0.6,
		})
	}

	// ── 工作流相关经验 ────────────────────────────────────────────────────────
	dev := report.PlanDeviation
	if !dev.OnSchedule && dev.PlannedTasks > 0 {
		lessons = append(lessons, Lesson{
			Category: "workflow",
			Description: fmt.Sprintf(
				"项目未按计划完成（完成率 %.0f%%，失败 %d/%d），建议拆分大任务或引入更多并行",
				dev.CompletionRate*100, dev.FailedTasks, dev.PlannedTasks,
			),
			Actionable: true,
			Importance: 0.8,
		})
	}

	// ── 预估偏差汇总 ─────────────────────────────────────────────────────────
	overBudgetCount := 0
	underBudgetCount := 0
	for _, ta := range report.TaskAnalysis {
		switch ta.Verdict {
		case "over_budget":
			overBudgetCount++
		case "under_budget":
			underBudgetCount++
		}
	}
	if overBudgetCount > 0 {
		lessons = append(lessons, Lesson{
			Category: "workflow",
			Description: fmt.Sprintf(
				"%d 个任务实际消耗超出预估，建议对该类任务增加预估系数",
				overBudgetCount,
			),
			Actionable: true,
			Importance: 0.65,
		})
	}
	if underBudgetCount > 0 {
		lessons = append(lessons, Lesson{
			Category: "workflow",
			Description: fmt.Sprintf(
				"%d 个任务实际消耗远低于预估，预估偏保守，可适当收紧以释放预算给其他任务",
				underBudgetCount,
			),
			Actionable: false,
			Importance: 0.4,
		})
	}

	return lessons
}

// generateRecommendations 基于反思报告生成改进建议。
func (e *MetaCognitiveEngine) generateRecommendations(report *ReflectionReport) []string {
	var recs []string

	dev := report.PlanDeviation
	ba := report.BudgetAnalysis
	qa := report.QualityAnalysis

	// 完成率建议
	if dev.PlannedTasks > 0 && dev.CompletionRate < 0.5 {
		recs = append(recs, "完成率低于 50%，建议重新评估任务拆分粒度和依赖关系")
	} else if dev.PlannedTasks > 0 && dev.CompletionRate < 0.8 {
		recs = append(recs, "完成率未达 80%，建议检查阻塞任务并调整优先级")
	}

	// 失败率建议
	if dev.PlannedTasks > 0 && dev.FailedTasks > 0 {
		failRate := float64(dev.FailedTasks) / float64(dev.PlannedTasks)
		if failRate >= 0.3 {
			recs = append(recs, "任务失败率超过 30%，建议开启预执行验证（dry-run）和更严格的输入校验")
		}
	}

	// 预算建议
	switch ba.Verdict {
	case "wasteful":
		recs = append(recs, "资源利用率偏低，建议合并相似任务或缩减冗余步骤")
	case "tight":
		recs = append(recs, "预算接近耗尽，建议为关键路径预留 15% 的缓冲量")
	}

	// 质量建议
	switch qa.Verdict {
	case "quality_risk":
		recs = append(recs, "质量风险较高，建议增加 required 检查点并启用自动修复")
	case "needs_attention":
		recs = append(recs, "存在未修复问题，建议复盘 issue 分类，优先处理 critical 级别")
	}

	// brain 选择建议
	wrongBrainTasks := 0
	for _, ta := range report.TaskAnalysis {
		if ta.Verdict == "wrong_brain" {
			wrongBrainTasks++
		}
	}
	if wrongBrainTasks > 0 {
		recs = append(recs, fmt.Sprintf(
			"%d 个任务 brain 选择不当，建议运行 L1 RankBrains 重新校准能力画像",
			wrongBrainTasks,
		))
	}

	// 预估偏差建议
	highDeviationCount := 0
	for _, ta := range report.TaskAnalysis {
		if ta.TurnAccuracy < 0.5 && ta.Success {
			highDeviationCount++
		}
	}
	if highDeviationCount > 0 {
		recs = append(recs, fmt.Sprintf(
			"%d 个任务预估偏差超过 50%%，建议引入历史数据驱动的复杂度估算",
			highDeviationCount,
		))
	}

	// 如果一切顺利
	if len(recs) == 0 {
		recs = append(recs, "本轮执行各项指标正常，保持当前策略")
	}

	return recs
}

// FeedbackToLearner 将反思结果反馈给 LearningEngine。
// 将 brain 选择的优劣和预算偏差记录为 L3 用户偏好反馈，以持续改进后续决策。
func (e *MetaCognitiveEngine) FeedbackToLearner(report *ReflectionReport) {
	if e.learner == nil {
		return
	}

	// 反馈 brain 选择质量
	for _, ta := range report.TaskAnalysis {
		category := fmt.Sprintf("brain_accuracy:%s", ta.AssignedBrain)
		value := ta.Verdict
		weight := ta.TurnAccuracy
		if !ta.Success {
			weight = 0.0
		}
		e.learner.RecordUserFeedback(category, value, weight)
	}

	// 反馈预算偏差
	ba := report.BudgetAnalysis
	e.learner.RecordUserFeedback("budget_utilization", ba.Verdict, ba.Utilization)

	// 反馈质量状况
	qa := report.QualityAnalysis
	e.learner.RecordUserFeedback("quality_status", qa.Verdict, qa.FixRate)

	// 反馈整体完成率
	dev := report.PlanDeviation
	onScheduleValue := "off_schedule"
	if dev.OnSchedule {
		onScheduleValue = "on_schedule"
	}
	e.learner.RecordUserFeedback("plan_adherence", onScheduleValue, dev.CompletionRate)

	// 将高重要度经验教训写入学习引擎
	for _, lesson := range report.Lessons {
		if lesson.Importance >= 0.7 {
			category := fmt.Sprintf("lesson:%s", lesson.Category)
			e.learner.RecordUserFeedback(category, lesson.Description, lesson.Importance)
		}
	}
}

// FormatReport 将反思报告格式化为人类可读的文本。
func (e *MetaCognitiveEngine) FormatReport(report *ReflectionReport) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("═══ 元认知反思报告 ═══\n"))
	sb.WriteString(fmt.Sprintf("项目: %s | 计划: %s\n", report.ProjectID, report.PlanID))
	sb.WriteString(fmt.Sprintf("生成时间: %s\n\n", report.CreatedAt.Format(time.RFC3339)))

	// 计划偏差
	dev := report.PlanDeviation
	scheduleIcon := "OFF"
	if dev.OnSchedule {
		scheduleIcon = "ON"
	}
	sb.WriteString(fmt.Sprintf("── 计划偏差 ──\n"))
	sb.WriteString(fmt.Sprintf("  计划任务: %d | 完成: %d | 失败: %d | 完成率: %.0f%% | 按期: %s\n\n",
		dev.PlannedTasks, dev.CompletedTasks, dev.FailedTasks,
		dev.CompletionRate*100, scheduleIcon))

	// 预算分析
	ba := report.BudgetAnalysis
	sb.WriteString(fmt.Sprintf("── 预算分析 ──\n"))
	sb.WriteString(fmt.Sprintf("  预算: %d turns | 使用: %d turns | 利用率: %.0f%% | 浪费: %d turns | 判定: %s\n\n",
		ba.TotalBudget, ba.TotalUsed, ba.Utilization*100, ba.WastedTurns, ba.Verdict))

	// 质量分析
	qa := report.QualityAnalysis
	sb.WriteString(fmt.Sprintf("── 质量分析 ──\n"))
	sb.WriteString(fmt.Sprintf("  审核迭代: %d | 发现问题: %d | 已修复: %d | 修复率: %.0f%% | 判定: %s\n\n",
		qa.ReviewIterations, qa.IssuesFound, qa.IssuesFixed, qa.FixRate*100, qa.Verdict))

	// 任务分析
	if len(report.TaskAnalysis) > 0 {
		sb.WriteString("── 任务分析 ──\n")
		for _, ta := range report.TaskAnalysis {
			sb.WriteString(fmt.Sprintf("  [%s] %s → brain=%s est=%d act=%d acc=%.0f%% verdict=%s\n",
				ta.TaskID, ta.TaskName, ta.AssignedBrain,
				ta.EstimatedTurns, ta.ActualTurns,
				ta.TurnAccuracy*100, ta.Verdict))
		}
		sb.WriteString("\n")
	}

	// 经验教训
	if len(report.Lessons) > 0 {
		sb.WriteString("── 经验教训 ──\n")
		for _, l := range report.Lessons {
			actionable := ""
			if l.Actionable {
				actionable = " [可自动化]"
			}
			sb.WriteString(fmt.Sprintf("  [%s] (%.0f%%) %s%s\n",
				l.Category, l.Importance*100, l.Description, actionable))
		}
		sb.WriteString("\n")
	}

	// 改进建议
	if len(report.Recommendations) > 0 {
		sb.WriteString("── 改进建议 ──\n")
		for i, rec := range report.Recommendations {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, rec))
		}
	}

	return sb.String()
}
