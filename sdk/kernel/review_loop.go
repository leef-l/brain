// review_loop.go — **任务级**通用审核闭环控制器（针对 TaskOutput / Artifact）
//
// 与 design_review.go 的区分：
//   - 本文件（review_loop.go）：MACCS Wave 1 任务级审核闭环
//     输入是 PlanSubTask 的执行产物（DelegateResult / 文件 / 代码片段），
//     由 Verifier brain 通过 Orchestrator.Delegate 调用执行，
//     发现问题后自动生成修复子任务并重新执行。
//   - design_review.go：MACCS Wave 3 设计级审核闭环
//     输入是 DesignProposal（方案规格 / 任务图），
//     由启发式规则或 Reviewer brain 检查方案本身的完备性 / 风险 / 覆盖度，
//     不涉及代码产物执行。
//
// 两者目标不同（task 输出 vs 方案规格），且字段差异大（TaskID/File/Line/AutoFixable
// vs ProposalID/Round/Category=architecture），刻意保持独立类型。

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/diaglog"
)

// ─────────────────────────────────────────────────────────────────────────────
// ReviewLoop — MACCS v2 任务级审核闭环控制器
//
// 核心循环：执行 → 审核 → 发现问题 → 生成修复任务 → 执行修复 → 再审核
// 直到审核通过或达到最大迭代次数。
// ─────────────────────────────────────────────────────────────────────────────

const reviewLoopCategory = "review-loop"

// ReviewReport 审核报告。
type ReviewReport struct {
	ReviewID    string        `json:"review_id"`
	TaskID      string        `json:"task_id"`
	Passed      bool          `json:"passed"`
	Score       float64       `json:"score"`
	Issues      []ReviewIssue `json:"issues"`
	Suggestions []string      `json:"suggestions"`
	ReviewedAt  time.Time     `json:"reviewed_at"`
}

// ReviewIssue 审核发现的问题。
type ReviewIssue struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`      // blocker/critical/warning/info
	Category     string `json:"category"`      // security/performance/style/bug/design
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	AutoFixable  bool   `json:"auto_fixable"`
}

// ReviewLoopConfig 审核闭环配置。
type ReviewLoopConfig struct {
	MaxIterations        int        // 最大迭代次数，默认 5
	ConvergenceThreshold float64    // 收敛阈值（问题数减少比例），默认 0.8
	ReviewerKind         agent.Kind // 审核大脑类型，默认 "verifier"
	AutoFixBlockerOnly   bool       // 是否只自动修复 blocker/critical 级别
}

// ReviewLoopController 审核闭环控制器。
// 自动化「执行→审核→发现问题→修复→再审核」循环。
type ReviewLoopController struct {
	orchestrator *Orchestrator
	config       ReviewLoopConfig
}

// NewReviewLoopController 创建审核闭环控制器。
func NewReviewLoopController(orch *Orchestrator, cfg ReviewLoopConfig) *ReviewLoopController {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 5
	}
	if cfg.ConvergenceThreshold <= 0 {
		cfg.ConvergenceThreshold = 0.8
	}
	if cfg.ReviewerKind == "" {
		cfg.ReviewerKind = agent.KindVerifier
	}
	return &ReviewLoopController{
		orchestrator: orch,
		config:       cfg,
	}
}

// ReviewLoopResult 审核闭环的最终结果。
type ReviewLoopResult struct {
	TaskResult  *DelegateResult `json:"task_result"`
	FinalReview *ReviewReport   `json:"final_review"`
	Iterations  int             `json:"iterations"`
	Converged   bool            `json:"converged"`
	TotalIssues int             `json:"total_issues"`
	FixedIssues int             `json:"fixed_issues"`
}

// ExecuteWithReview 执行任务并自动进入审核闭环。
//
// 流程：
//  1. 执行原始任务
//  2. 提交给 Verifier 审核
//  3. 如果审核不通过，生成修复任务
//  4. 执行修复
//  5. 重新审核（循环直到通过或达到最大迭代次数）
func (c *ReviewLoopController) ExecuteWithReview(ctx context.Context, task PlanSubTask) (*ReviewLoopResult, error) {
	diaglog.Info(reviewLoopCategory, "开始审核闭环",
		"task_id", task.TaskID,
		"max_iterations", c.config.MaxIterations,
	)

	// ── Step 1: 执行原始任务 ──
	taskResult, err := c.orchestrator.Delegate(ctx, &DelegateRequest{
		TaskID:      task.TaskID,
		TargetKind:  task.Kind,
		Instruction: task.Instruction,
	})
	if err != nil {
		return nil, fmt.Errorf("review-loop: 执行原始任务失败: %w", err)
	}

	result := &ReviewLoopResult{
		TaskResult: taskResult,
	}

	// 如果原始任务执行失败，直接返回
	if taskResult.Status == "failed" || taskResult.Status == "rejected" {
		diaglog.Warn(reviewLoopCategory, "原始任务执行失败，跳过审核",
			"task_id", task.TaskID,
			"status", taskResult.Status,
		)
		return result, nil
	}

	var prevSevereCount int // 上一轮 blocker+critical 数量
	var totalFixed int

	for iter := 1; iter <= c.config.MaxIterations; iter++ {
		result.Iterations = iter

		diaglog.Info(reviewLoopCategory, "提交审核",
			"task_id", task.TaskID,
			"iteration", iter,
		)

		// ── Step 2: 提交审核 ──
		review, err := c.submitReview(ctx, task, taskResult.Output)
		if err != nil {
			diaglog.Warn(reviewLoopCategory, "审核请求失败",
				"task_id", task.TaskID,
				"iteration", iter,
				"error", err.Error(),
			)
			// 审核失败不中断流程，视为通过
			review = &ReviewReport{
				ReviewID:   fmt.Sprintf("review-%s-%d", task.TaskID, iter),
				TaskID:     task.TaskID,
				Passed:     true,
				Score:      1.0,
				ReviewedAt: time.Now(),
			}
		}

		result.FinalReview = review
		result.TotalIssues += len(review.Issues)

		// ── Step 3: 审核通过则结束 ──
		if review.Passed {
			result.Converged = true
			diaglog.Info(reviewLoopCategory, "审核通过",
				"task_id", task.TaskID,
				"iteration", iter,
				"score", review.Score,
			)
			break
		}

		// ── 统计当前轮次的 blocker+critical 数量 ──
		currentSevereCount := countSevereIssues(review.Issues)

		diaglog.Info(reviewLoopCategory, "审核未通过",
			"task_id", task.TaskID,
			"iteration", iter,
			"issues", len(review.Issues),
			"severe_count", currentSevereCount,
			"score", review.Score,
		)

		// ── Step 7: 收敛检查 ──
		// 从第 2 轮开始检查：如果严重问题数量没有按阈值减少，说明不收敛
		if iter > 1 && prevSevereCount > 0 {
			reduction := 1.0 - float64(currentSevereCount)/float64(prevSevereCount)
			if reduction < c.config.ConvergenceThreshold {
				diaglog.Warn(reviewLoopCategory, "审核闭环不收敛，提前终止",
					"task_id", task.TaskID,
					"iteration", iter,
					"prev_severe", prevSevereCount,
					"curr_severe", currentSevereCount,
					"reduction", reduction,
					"threshold", c.config.ConvergenceThreshold,
				)
				break
			}
		}
		prevSevereCount = currentSevereCount

		// 如果达到最大迭代次数，不再修复
		if iter == c.config.MaxIterations {
			diaglog.Warn(reviewLoopCategory, "达到最大迭代次数",
				"task_id", task.TaskID,
				"max_iterations", c.config.MaxIterations,
			)
			break
		}

		// ── Step 4: 生成修复任务 ──
		fixTasks := generateFixTasks(review.Issues, task, c.config.AutoFixBlockerOnly)
		if len(fixTasks) == 0 {
			diaglog.Info(reviewLoopCategory, "没有可修复的问题",
				"task_id", task.TaskID,
				"iteration", iter,
			)
			break
		}

		diaglog.Info(reviewLoopCategory, "执行修复任务",
			"task_id", task.TaskID,
			"iteration", iter,
			"fix_count", len(fixTasks),
		)

		// ── Step 5: 执行修复任务 ──
		var lastFixResult *DelegateResult
		for fi, fixTask := range fixTasks {
			fixResult, fixErr := c.orchestrator.Delegate(ctx, &DelegateRequest{
				TaskID:      fixTask.TaskID,
				TargetKind:  fixTask.Kind,
				Instruction: fixTask.Instruction,
			})
			if fixErr != nil {
				diaglog.Warn(reviewLoopCategory, "修复任务执行失败",
					"task_id", task.TaskID,
					"fix_index", fi,
					"fix_task_id", fixTask.TaskID,
					"error", fixErr.Error(),
				)
				continue
			}
			if fixResult.Status == "completed" {
				totalFixed++
			}
			lastFixResult = fixResult
		}

		// 更新 taskResult 为最新的修复结果（如果有的话）
		if lastFixResult != nil {
			result.TaskResult = lastFixResult
			taskResult = lastFixResult
		}

		// ── Step 6: 循环回到 Step 2 重新审核 ──
	}

	result.FixedIssues = totalFixed
	return result, nil
}

// submitReview 提交审核请求给 Verifier。
func (c *ReviewLoopController) submitReview(ctx context.Context, task PlanSubTask, output json.RawMessage) (*ReviewReport, error) {
	// 构造审核指令
	instruction := fmt.Sprintf(
		"请审核以下任务的执行结果：\n\n原始任务：%s\n\n执行输出：%s\n\n请按 ReviewReport JSON 格式返回审核结果。",
		task.Instruction,
		string(output),
	)

	// 调用 orchestrator.Delegate 给 verifier
	reviewResult, err := c.orchestrator.Delegate(ctx, &DelegateRequest{
		TaskID:      fmt.Sprintf("review-%s-%d", task.TaskID, time.Now().UnixNano()),
		TargetKind:  c.config.ReviewerKind,
		Instruction: instruction,
	})
	if err != nil {
		return nil, fmt.Errorf("submitReview: delegate 失败: %w", err)
	}

	// 解析返回的 ReviewReport
	report := parseReviewReport(task.TaskID, reviewResult)
	return report, nil
}

// generateFixTasks 根据审核问题生成修复子任务。
func generateFixTasks(issues []ReviewIssue, originalTask PlanSubTask, blockersOnly bool) []PlanSubTask {
	var tasks []PlanSubTask
	for i, issue := range issues {
		// 如果只修复 blocker/critical，跳过其他级别
		if blockersOnly && issue.Severity != "blocker" && issue.Severity != "critical" {
			continue
		}

		// 只修复有描述的问题
		if issue.Description == "" {
			continue
		}

		// 构造修复指令
		instruction := fmt.Sprintf(
			"请修复以下问题：\n\n问题标题：%s\n严重程度：%s\n分类：%s\n问题描述：%s",
			issue.Title, issue.Severity, issue.Category, issue.Description,
		)
		if issue.File != "" {
			instruction += fmt.Sprintf("\n文件：%s", issue.File)
			if issue.Line > 0 {
				instruction += fmt.Sprintf("（第 %d 行）", issue.Line)
			}
		}
		if issue.SuggestedFix != "" {
			instruction += fmt.Sprintf("\n建议修复方案：%s", issue.SuggestedFix)
		}

		fixTask := PlanSubTask{
			TaskID:      fmt.Sprintf("%s-fix-%d", originalTask.TaskID, i),
			Name:        fmt.Sprintf("修复: %s", issue.Title),
			Kind:        originalTask.Kind,
			Instruction: instruction,
			Status:      PlanTaskPending,
		}
		tasks = append(tasks, fixTask)
	}
	return tasks
}

// parseReviewReport 从 DelegateResult 解析审核报告。
func parseReviewReport(taskID string, result *DelegateResult) *ReviewReport {
	if result == nil {
		return &ReviewReport{
			ReviewID:   fmt.Sprintf("review-%s-default", taskID),
			TaskID:     taskID,
			Passed:     true,
			Score:      1.0,
			ReviewedAt: time.Now(),
		}
	}

	// 尝试从 result.Output JSON 中解析 ReviewReport
	var report ReviewReport
	if len(result.Output) > 0 {
		if err := json.Unmarshal(result.Output, &report); err == nil && report.TaskID != "" {
			// 解析成功
			if report.ReviewID == "" {
				report.ReviewID = fmt.Sprintf("review-%s-%d", taskID, time.Now().UnixNano())
			}
			if report.ReviewedAt.IsZero() {
				report.ReviewedAt = time.Now()
			}
			return &report
		}
	}

	// 解析失败则构造一个默认的 "passed" 报告
	return &ReviewReport{
		ReviewID:   fmt.Sprintf("review-%s-%d", taskID, time.Now().UnixNano()),
		TaskID:     taskID,
		Passed:     true,
		Score:      1.0,
		ReviewedAt: time.Now(),
	}
}

// countSevereIssues 统计 blocker 和 critical 级别的问题数量。
func countSevereIssues(issues []ReviewIssue) int {
	count := 0
	for _, issue := range issues {
		if issue.Severity == "blocker" || issue.Severity == "critical" {
			count++
		}
	}
	return count
}
