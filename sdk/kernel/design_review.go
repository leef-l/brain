package kernel

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// DesignReview — MACCS Wave 3 Batch 2 方案审核循环
//
// 方案设计完成后的审核闭环：审核 → 发现问题 → AutoFix → 再审核，
// 直到通过或达到最大轮次。
// ─────────────────────────────────────────────────────────────────────────────

// DesignReviewCriteria 方案审核标准。
type DesignReviewCriteria struct {
	MinScore           float64  `json:"min_score"`            // 最低方案评分，默认 60
	RequiredComponents []string `json:"required_components"`  // 必须包含的组件类型
	MaxRiskLevel       string   `json:"max_risk_level"`       // 可接受的最高风险等级 high/medium/low
	RequireTests       bool     `json:"require_tests"`        // 是否要求包含测试任务
	MaxTasks           int      `json:"max_tasks"`            // 最大任务数限制，0=不限
}

// NewDesignReviewCriteria 返回使用默认值的审核标准。
func NewDesignReviewCriteria() *DesignReviewCriteria {
	return &DesignReviewCriteria{
		MinScore:     60,
		MaxRiskLevel: "high",
	}
}

// DesignReviewResult 单轮审核结果。
type DesignReviewResult struct {
	ReviewID    string              `json:"review_id"`
	ProposalID  string              `json:"proposal_id"`
	Passed      bool                `json:"passed"`
	Score       float64             `json:"score"`
	Issues      []DesignReviewIssue `json:"issues"`
	Suggestions []string            `json:"suggestions"`
	ReviewedAt  time.Time           `json:"reviewed_at"`
	Round       int                 `json:"round"` // 第几轮审核
}

// DesignReviewIssue 审核发现的具体问题。
type DesignReviewIssue struct {
	Severity    string `json:"severity"`              // blocker/critical/warning/info
	Category    string `json:"category"`              // architecture/risk/coverage/budget/dependency
	Description string `json:"description"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// DesignReviewer 方案审核接口。
type DesignReviewer interface {
	Review(ctx context.Context, proposal *DesignProposal, criteria *DesignReviewCriteria) (*DesignReviewResult, error)
	AutoFix(ctx context.Context, proposal *DesignProposal, issues []DesignReviewIssue) (*DesignProposal, error)
}

// ---------------------------------------------------------------------------
// DefaultDesignReviewer — 启发式审核实现（不调用 LLM）
// ---------------------------------------------------------------------------

// DefaultDesignReviewer 基于规则的方案审核器。
type DefaultDesignReviewer struct{}

// NewDefaultDesignReviewer 创建默认方案审核器。
func NewDefaultDesignReviewer() *DefaultDesignReviewer { return &DefaultDesignReviewer{} }

// Review 对方案执行启发式审核，返回审核结果。
func (r *DefaultDesignReviewer) Review(_ context.Context, proposal *DesignProposal, criteria *DesignReviewCriteria) (*DesignReviewResult, error) {
	if proposal == nil {
		return nil, fmt.Errorf("design-review: proposal 不能为空")
	}
	if criteria == nil {
		criteria = NewDesignReviewCriteria()
	}

	now := time.Now()
	result := &DesignReviewResult{
		ReviewID:   fmt.Sprintf("dr-%s-%d", proposal.ProposalID, now.UnixNano()),
		ProposalID: proposal.ProposalID,
		ReviewedAt: now,
	}

	var issues []DesignReviewIssue

	// 1. 检查方案评分是否达标
	score := proposal.Score
	if score <= 0 {
		score = proposal.ComputeScore()
	}
	result.Score = score
	if score < criteria.MinScore {
		issues = append(issues, DesignReviewIssue{
			Severity:    "critical",
			Category:    "budget",
			Description: fmt.Sprintf("方案评分 %.1f 低于最低要求 %.1f", score, criteria.MinScore),
			Suggestion:  "增加任务覆盖度、补充风险评估或完善架构设计以提高评分",
		})
	}

	// 2. 检查必须组件是否包含
	if len(criteria.RequiredComponents) > 0 {
		compSet := make(map[string]bool)
		for _, c := range proposal.Architecture.Components {
			compSet[c.Type] = true
		}
		for _, req := range criteria.RequiredComponents {
			if !compSet[req] {
				issues = append(issues, DesignReviewIssue{
					Severity:    "blocker",
					Category:    "architecture",
					Description: fmt.Sprintf("缺少必须组件类型: %s", req),
					Suggestion:  fmt.Sprintf("在架构组件中添加 %s 类型的组件", req),
				})
			}
		}
	}

	// 3. 检查风险等级
	if criteria.MaxRiskLevel != "" && criteria.MaxRiskLevel != "high" {
		for _, risk := range proposal.RiskAssessment {
			if riskExceeds(risk.Severity, criteria.MaxRiskLevel) {
				issues = append(issues, DesignReviewIssue{
					Severity:    "critical",
					Category:    "risk",
					Description: fmt.Sprintf("风险项 %s 等级为 %s，超过允许的最高等级 %s", risk.RiskID, risk.Severity, criteria.MaxRiskLevel),
					Suggestion:  risk.Mitigation,
				})
			}
		}
	}

	// 4. 检查是否有 verifier 类型的测试任务
	if criteria.RequireTests {
		hasVerifier := false
		for _, t := range proposal.TaskBreakdown {
			if t.BrainKind == "verifier" {
				hasVerifier = true
				break
			}
		}
		if !hasVerifier {
			issues = append(issues, DesignReviewIssue{
				Severity:    "critical",
				Category:    "coverage",
				Description: "方案缺少 verifier 类型的测试任务",
				Suggestion:  "添加一个 brain_kind=verifier 的验证任务",
			})
		}
	}

	// 5. 检查任务数限制
	if criteria.MaxTasks > 0 && len(proposal.TaskBreakdown) > criteria.MaxTasks {
		issues = append(issues, DesignReviewIssue{
			Severity:    "warning",
			Category:    "budget",
			Description: fmt.Sprintf("任务数 %d 超过限制 %d", len(proposal.TaskBreakdown), criteria.MaxTasks),
			Suggestion:  "移除低优先级任务或合并相似任务",
		})
	}

	// 6. 检查任务依赖是否有环
	if hasCycle(proposal.TaskBreakdown) {
		issues = append(issues, DesignReviewIssue{
			Severity:    "blocker",
			Category:    "dependency",
			Description: "任务依赖存在循环",
			Suggestion:  "重新梳理任务依赖关系，消除循环依赖",
		})
	}

	// 7. 检查是否有任务无 brain_kind
	for _, t := range proposal.TaskBreakdown {
		if t.BrainKind == "" {
			issues = append(issues, DesignReviewIssue{
				Severity:    "warning",
				Category:    "coverage",
				Description: fmt.Sprintf("任务 %s 未指定 brain_kind", t.TaskID),
				Suggestion:  "为该任务指定合适的 brain_kind（code/browser/data/verifier）",
			})
		}
	}

	// 汇总
	result.Issues = issues
	blockerOrCritical := 0
	for _, iss := range issues {
		if iss.Severity == "blocker" || iss.Severity == "critical" {
			blockerOrCritical++
		}
	}
	result.Passed = blockerOrCritical == 0

	// 生成建议
	if !result.Passed {
		result.Suggestions = append(result.Suggestions, fmt.Sprintf("发现 %d 个阻塞/严重问题，请修复后重新审核", blockerOrCritical))
	}
	if len(issues) > 0 && result.Passed {
		result.Suggestions = append(result.Suggestions, fmt.Sprintf("审核通过，但有 %d 个警告建议关注", len(issues)))
	}

	return result, nil
}

// AutoFix 尝试自动修复方案中的问题。
func (r *DefaultDesignReviewer) AutoFix(_ context.Context, proposal *DesignProposal, issues []DesignReviewIssue) (*DesignProposal, error) {
	if proposal == nil {
		return nil, fmt.Errorf("design-review: proposal 不能为空")
	}

	// 复制一份，避免修改原始对象
	fixed := *proposal
	fixed.TaskBreakdown = make([]DesignTask, len(proposal.TaskBreakdown))
	copy(fixed.TaskBreakdown, proposal.TaskBreakdown)
	fixed.RiskAssessment = make([]RiskItem, len(proposal.RiskAssessment))
	copy(fixed.RiskAssessment, proposal.RiskAssessment)

	for _, iss := range issues {
		switch {
		// 评分不够：刷新评分
		case iss.Category == "budget" && iss.Severity == "critical" && fixed.Score < 60:
			fixed.ComputeScore()

		// 缺少测试任务：自动添加一个 verifier 类型的 DesignTask
		case iss.Category == "coverage" && iss.Description == "方案缺少 verifier 类型的测试任务":
			verifierTask := DesignTask{
				TaskID:      fmt.Sprintf("dt-%s-verify", fixed.ProposalID),
				Name:        "自动验证任务",
				Description: "自动添加的验证任务，确保方案质量",
				BrainKind:   "verifier",
				Priority:    2,
				EstTurns:    3,
			}
			fixed.TaskBreakdown = append(fixed.TaskBreakdown, verifierTask)

		// 任务无 brain_kind：默认设为 "code"
		case iss.Category == "coverage" && iss.Severity == "warning":
			for i := range fixed.TaskBreakdown {
				if fixed.TaskBreakdown[i].BrainKind == "" {
					fixed.TaskBreakdown[i].BrainKind = "code"
				}
			}

		// 任务数超限：移除最低优先级的 nice_to_have 任务
		case iss.Category == "budget" && iss.Severity == "warning":
			fixed.TaskBreakdown = trimLowPriorityTasks(fixed.TaskBreakdown)
		}
	}

	return &fixed, nil
}

// ---------------------------------------------------------------------------
// DesignReviewLoop — 方案审核闭环控制器
// ---------------------------------------------------------------------------

// DesignReviewLoop 自动化方案审核闭环：审核 → AutoFix → 再审核。
type DesignReviewLoop struct {
	reviewer  DesignReviewer
	maxRounds int // 默认 3
	criteria  *DesignReviewCriteria
}

// NewDesignReviewLoop 创建方案审核闭环控制器。
func NewDesignReviewLoop(reviewer DesignReviewer, criteria *DesignReviewCriteria) *DesignReviewLoop {
	if criteria == nil {
		criteria = NewDesignReviewCriteria()
	}
	return &DesignReviewLoop{
		reviewer:  reviewer,
		maxRounds: 3,
		criteria:  criteria,
	}
}

// DesignReviewLoopResult 审核闭环的最终结果。
type DesignReviewLoopResult struct {
	FinalProposal *DesignProposal      `json:"final_proposal"`
	Reviews       []*DesignReviewResult `json:"reviews"`
	Converged     bool                  `json:"converged"`     // 是否收敛（通过审核）
	TotalRounds   int                   `json:"total_rounds"`
}

// Execute 执行审核闭环：审核 → 不通过则 AutoFix → 再审核，直到通过或达到 maxRounds。
func (l *DesignReviewLoop) Execute(ctx context.Context, proposal *DesignProposal) (*DesignReviewLoopResult, error) {
	if proposal == nil {
		return nil, fmt.Errorf("design-review-loop: proposal 不能为空")
	}

	result := &DesignReviewLoopResult{}
	current := proposal

	for round := 1; round <= l.maxRounds; round++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// 审核
		review, err := l.reviewer.Review(ctx, current, l.criteria)
		if err != nil {
			return nil, fmt.Errorf("design-review-loop: 第 %d 轮审核失败: %w", round, err)
		}
		review.Round = round
		result.Reviews = append(result.Reviews, review)
		result.TotalRounds = round

		// 通过则返回
		if review.Passed {
			result.FinalProposal = current
			result.Converged = true
			return result, nil
		}

		// 未通过且已是最后一轮，不再 AutoFix
		if round == l.maxRounds {
			result.FinalProposal = current
			return result, nil
		}

		// AutoFix
		fixed, err := l.reviewer.AutoFix(ctx, current, review.Issues)
		if err != nil {
			// AutoFix 失败不中断流程，用原始 proposal 继续
			result.FinalProposal = current
			return result, nil
		}
		current = fixed
	}

	result.FinalProposal = current
	return result, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// riskLevelValue 将风险等级转为数值，用于比较。
func riskLevelValue(level string) int {
	switch level {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	default:
		return 0
	}
}

// riskExceeds 判断实际风险等级是否超过允许的最高等级。
func riskExceeds(actual, maxAllowed string) bool {
	return riskLevelValue(actual) > riskLevelValue(maxAllowed)
}

// hasCycle 使用 DFS 检测任务依赖是否存在环。
func hasCycle(tasks []DesignTask) bool {
	taskSet := make(map[string]bool, len(tasks))
	deps := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		taskSet[t.TaskID] = true
		deps[t.TaskID] = t.DependsOn
	}

	const (
		white = 0 // 未访问
		gray  = 1 // 访问中
		black = 2 // 已完成
	)
	color := make(map[string]int, len(tasks))

	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = gray
		for _, dep := range deps[id] {
			if !taskSet[dep] {
				continue // 依赖不在任务集中，忽略
			}
			switch color[dep] {
			case gray:
				return true // 发现环
			case white:
				if dfs(dep) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}

	for _, t := range tasks {
		if color[t.TaskID] == white {
			if dfs(t.TaskID) {
				return true
			}
		}
	}
	return false
}

// trimLowPriorityTasks 移除优先级最低（Priority 数值最大）的 nice_to_have 任务。
// nice_to_have 对应 Priority=3。
func trimLowPriorityTasks(tasks []DesignTask) []DesignTask {
	if len(tasks) == 0 {
		return tasks
	}

	// 按 Priority 降序排列索引，优先移除优先级最低的
	type indexed struct {
		idx  int
		prio int
	}
	var candidates []indexed
	for i, t := range tasks {
		if t.Priority >= 3 { // nice_to_have 或更低
			candidates = append(candidates, indexed{idx: i, prio: t.Priority})
		}
	}
	if len(candidates) == 0 {
		return tasks
	}

	// 按优先级数值降序排，移除最不重要的那个
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].prio > candidates[j].prio
	})

	removeIdx := candidates[0].idx
	result := make([]DesignTask, 0, len(tasks)-1)
	for i, t := range tasks {
		if i != removeIdx {
			result = append(result, t)
		}
	}
	return result
}
