package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// MACCS Wave 3 — EasyMVP 方案设计接口
// 提供从需求到执行方案的转换能力。
// 注意：RequirementSpec / FeatureSpec 定义在 requirement_parser.go 中。

// DesignProposal 是由 DesignGenerator 生成的完整方案提案。
type DesignProposal struct {
	ProposalID      string               `json:"proposal_id"`
	SpecID          string               `json:"spec_id"`           // 关联的 RequirementSpec
	Title           string               `json:"title"`
	Description     string               `json:"description"`
	Architecture    ArchitectureDecision  `json:"architecture"`
	TaskBreakdown   []DesignTask          `json:"task_breakdown"`
	RiskAssessment  []RiskItem            `json:"risk_assessment"`
	EstimatedBudget DesignBudget          `json:"estimated_budget"`
	Alternatives    []string              `json:"alternatives,omitempty"` // 备选方案摘要
	CreatedAt       time.Time             `json:"created_at"`
	Score           float64              `json:"score"` // 方案评分 0-100
}

// ArchitectureDecision 记录架构层面的关键决策。
type ArchitectureDecision struct {
	Pattern    string          `json:"pattern"`    // mvc/microservice/monolith/event_driven
	TechStack  []string        `json:"tech_stack"`  // 技术栈选择
	Components []ComponentSpec `json:"components"`  // 组件列表
	DataFlow   string          `json:"data_flow"`   // 数据流描述
	Rationale  string          `json:"rationale"`   // 选择理由
}

// ComponentSpec 描述架构中的一个组件。
type ComponentSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // frontend/backend/database/service/library
	Description string   `json:"description"`
	BrainKind   string   `json:"brain_kind,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

// DesignTask 是设计阶段拆分出的单个任务定义。
type DesignTask struct {
	TaskID      string   `json:"task_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BrainKind   string   `json:"brain_kind"` // code/browser/data/verifier
	Priority    int      `json:"priority"`    // 1=最高
	DependsOn   []string `json:"depends_on,omitempty"`
	EstTurns    int      `json:"est_turns"` // 预估 turn 数
}

// RiskItem 描述一个风险项及其缓解措施。
type RiskItem struct {
	RiskID      string `json:"risk_id"`
	Description string `json:"description"`
	Severity    string `json:"severity"`    // high/medium/low
	Mitigation  string `json:"mitigation"`
	Probability string `json:"probability"` // likely/possible/unlikely
}

// DesignBudget 是方案级的资源预算估算。
type DesignBudget struct {
	TotalTurns     int `json:"total_turns"`
	TotalBrains    int `json:"total_brains"`
	ParallelSlots  int `json:"parallel_slots"`
	TimeoutMinutes int `json:"timeout_minutes"`
}

type DesignGenerator interface { // 方案生成、比较与转换的标准接口
	Generate(ctx context.Context, spec *RequirementSpec) (*DesignProposal, error)
	Compare(proposals []*DesignProposal) *DesignProposal // 选最优
	ToTaskPlan(proposal *DesignProposal) *TaskPlan        // 转换为可执行 TaskPlan
}

// NewDesignProposal 创建一个新的方案提案。
func NewDesignProposal(specID, title string) *DesignProposal {
	now := time.Now()
	return &DesignProposal{
		ProposalID: fmt.Sprintf("proposal-%d", now.UnixNano()),
		SpecID:     specID,
		Title:      title,
		CreatedAt:  now,
	}
}

// AddTask 向方案中添加一个设计任务。
func (p *DesignProposal) AddTask(t DesignTask) {
	p.TaskBreakdown = append(p.TaskBreakdown, t)
}

// AddRisk 向方案中添加一个风险项。
func (p *DesignProposal) AddRisk(r RiskItem) {
	p.RiskAssessment = append(p.RiskAssessment, r)
}

// ComputeScore 基于完整度、风险和复杂度计算方案评分（0-100）。
// 基础 50 分 + 任务完整度（最高 25）+ 风险评估（最高 25）+ 架构 5 分。
func (p *DesignProposal) ComputeScore() float64 {
	score := 50.0
	// 任务完整度
	if n := len(p.TaskBreakdown); n > 0 {
		bonus := 10.0 + float64(n)
		if bonus > 25 {
			bonus = 25
		}
		score += bonus
	}
	// 风险评估
	if len(p.RiskAssessment) > 0 {
		rb := 10.0
		for _, r := range p.RiskAssessment {
			switch r.Severity {
			case "high":
				rb -= 3
			case "medium":
				rb -= 1
			case "low":
				rb += 1
			}
		}
		if rb < 0 {
			rb = 0
		}
		if rb > 25 {
			rb = 25
		}
		score += rb
	}
	if p.Architecture.Pattern != "" {
		score += 5
	}
	if score > 100 {
		score = 100
	}
	p.Score = score
	return score
}

// ---------------------------------------------------------------------------
// DefaultDesignGenerator — 启发式默认实现
// ---------------------------------------------------------------------------

// DefaultDesignGenerator 是 DesignGenerator 的启发式默认实现。
type DefaultDesignGenerator struct{}

// NewDefaultDesignGenerator 创建默认方案生成器。
func NewDefaultDesignGenerator() *DefaultDesignGenerator { return &DefaultDesignGenerator{} }

// Generate 基于 RequirementSpec 启发式生成方案。
func (g *DefaultDesignGenerator) Generate(_ context.Context, spec *RequirementSpec) (*DesignProposal, error) {
	if spec == nil {
		return nil, fmt.Errorf("design: RequirementSpec 不能为空")
	}
	title := spec.ParsedGoal
	if title == "" {
		title = spec.RawGoal
	}
	proposal := NewDesignProposal(spec.SpecID, fmt.Sprintf("%s 方案", truncate(title, 40)))
	proposal.Description = spec.RawGoal
	proposal.Architecture = g.pickArchitecture(spec)

	// 将 features 拆分为 DesignTask
	for i, feat := range spec.Features {
		task := DesignTask{
			TaskID:      fmt.Sprintf("dt-%s-%d", spec.SpecID, i+1),
			Name:        feat.Name,
			Description: feat.Description,
			BrainKind:   g.pickBrainKind(feat),
			Priority:    priorityToInt(feat.Priority),
			EstTurns:    g.estimateTurns(feat),
		}
		if i > 0 {
			task.DependsOn = []string{fmt.Sprintf("dt-%s-%d", spec.SpecID, i)}
		}
		proposal.AddTask(task)
	}
	g.assessRisks(proposal, spec)
	proposal.EstimatedBudget = g.computeBudget(proposal)
	proposal.ComputeScore()
	return proposal, nil
}

// Compare 从多份提案中选出 Score 最高的。
func (g *DefaultDesignGenerator) Compare(proposals []*DesignProposal) *DesignProposal {
	if len(proposals) == 0 {
		return nil
	}
	best := proposals[0]
	for _, p := range proposals[1:] {
		if p.Score > best.Score {
			best = p
		}
	}
	return best
}

// ToTaskPlan 将 DesignProposal 转换为可执行的 TaskPlan。
func (g *DefaultDesignGenerator) ToTaskPlan(proposal *DesignProposal) *TaskPlan {
	if proposal == nil {
		return nil
	}
	plan := NewTaskPlan(proposal.SpecID, proposal.Title)
	plan.Budget = PlanBudget{
		TotalTurns:  proposal.EstimatedBudget.TotalTurns,
		TotalTokens: proposal.EstimatedBudget.TotalTurns * 4000, // 粗估每 turn 4000 token
	}
	deps := make(map[string][]string)
	for _, dt := range proposal.TaskBreakdown {
		plan.AddSubTask(PlanSubTask{
			TaskID:         dt.TaskID,
			Name:           dt.Name,
			Kind:           agent.Kind(dt.BrainKind),
			Instruction:    dt.Description,
			EstimatedTurns: dt.EstTurns,
			RetryPolicy:    RetryPolicy{MaxRetries: 2},
		})
		if len(dt.DependsOn) > 0 {
			deps[dt.TaskID] = dt.DependsOn
		}
	}
	plan.Dependencies = deps
	_ = plan.ComputeParallelLayers()
	return plan
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// pickArchitecture 根据需求 category 选择架构模式。
func (g *DefaultDesignGenerator) pickArchitecture(spec *RequirementSpec) ArchitectureDecision {
	ad := ArchitectureDecision{}
	switch spec.Category {
	case "web_app":
		ad.Pattern, ad.TechStack = "mvc", []string{"go", "html", "javascript"}
		ad.Rationale = "Web 应用适合 MVC 分层模式"
	case "api_service":
		ad.Pattern, ad.TechStack = "microservice", []string{"go", "grpc", "protobuf"}
		ad.Rationale = "API 服务适合微服务模式"
	case "cli_tool":
		ad.Pattern, ad.TechStack = "monolith", []string{"go"}
		ad.Rationale = "CLI 工具适合单体架构"
	case "game":
		ad.Pattern, ad.TechStack = "event_driven", []string{"go"}
		ad.Rationale = "游戏适合事件驱动模式"
	default:
		ad.Pattern, ad.TechStack = "monolith", []string{"go"}
		ad.Rationale = "默认采用单体架构"
	}
	ad.DataFlow = fmt.Sprintf("input → %s pipeline → output", ad.Pattern)
	return ad
}

// pickBrainKind 沿用 FeatureSpec.BrainKind，缺省 code。
func (g *DefaultDesignGenerator) pickBrainKind(feat FeatureSpec) string {
	if feat.BrainKind != "" {
		return feat.BrainKind
	}
	return "code"
}

// estimateTurns 根据优先级预估 turn 数。
func (g *DefaultDesignGenerator) estimateTurns(feat FeatureSpec) int {
	switch feat.Priority {
	case "must_have":
		return 8
	case "should_have":
		return 5
	case "nice_to_have":
		return 3
	default:
		return 5
	}
}

// assessRisks 基于需求特征生成风险项。
func (g *DefaultDesignGenerator) assessRisks(proposal *DesignProposal, spec *RequirementSpec) {
	idx := 1
	if len(spec.Features) > 5 {
		proposal.AddRisk(RiskItem{
			RiskID: fmt.Sprintf("risk-%d", idx), Severity: "medium", Probability: "possible",
			Description: "功能数量较多，可能导致集成复杂度上升",
			Mitigation:  "分批交付，优先实现核心功能",
		})
		idx++
	}
	if spec.Complexity == "complex" {
		proposal.AddRisk(RiskItem{
			RiskID: fmt.Sprintf("risk-%d", idx), Severity: "high", Probability: "possible",
			Description: "需求整体复杂度高，可能超出预估",
			Mitigation:  "预留额外 turn 预算，提前拆分子任务",
		})
		idx++
	}
	if len(spec.Constraints) > 0 {
		proposal.AddRisk(RiskItem{
			RiskID: fmt.Sprintf("risk-%d", idx), Severity: "low", Probability: "unlikely",
			Description: "存在外部约束条件，可能限制技术选型",
			Mitigation:  "在方案设计阶段充分评估约束影响",
		})
	}
}

// priorityToInt 将字符串优先级转换为整数（1=最高）。
func priorityToInt(p string) int {
	switch p {
	case "must_have":
		return 1
	case "should_have":
		return 2
	case "nice_to_have":
		return 3
	default:
		return 2
	}
}

// computeBudget 根据任务列表计算整体预算。
func (g *DefaultDesignGenerator) computeBudget(proposal *DesignProposal) DesignBudget {
	totalTurns := 0
	brainSet := make(map[string]bool)
	for _, t := range proposal.TaskBreakdown {
		totalTurns += t.EstTurns
		brainSet[t.BrainKind] = true
	}
	slots := len(proposal.TaskBreakdown)
	if slots > 4 {
		slots = 4
	}
	if slots == 0 {
		slots = 1
	}
	timeout := totalTurns * 2
	if timeout < 10 {
		timeout = 10
	}
	return DesignBudget{
		TotalTurns:     totalTurns,
		TotalBrains:    len(brainSet),
		ParallelSlots:  slots,
		TimeoutMinutes: timeout,
	}
}
