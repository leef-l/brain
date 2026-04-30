package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 结构化需求模型
// ---------------------------------------------------------------------------

// RequirementSpec 结构化需求文档，将用户自然语言目标解析为可执行规格。
type RequirementSpec struct {
	SpecID         string              `json:"spec_id"`
	RawGoal        string              `json:"raw_goal"`        // 原始用户输入
	ParsedGoal     string              `json:"parsed_goal"`     // 结构化后的目标
	Category       string              `json:"category"`        // web_app/cli_tool/library/game/api_service
	Features       []FeatureSpec       `json:"features"`        // 功能列表
	Constraints    []ConstraintSpec    `json:"constraints"`     // 约束条件
	Acceptance     []AcceptanceCriteria `json:"acceptance"`     // 验收标准
	Priority       string              `json:"priority"`        // high/medium/low
	Complexity     string              `json:"complexity"`      // simple/moderate/complex
	EstimatedTasks int                 `json:"estimated_tasks"` // 预估任务数
	CreatedAt      time.Time           `json:"created_at"`
}

// FeatureSpec 功能规格。
type FeatureSpec struct {
	FeatureID   string   `json:"feature_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Priority    string   `json:"priority"`             // must_have/should_have/nice_to_have
	BrainKind   string   `json:"brain_kind,omitempty"` // 建议的执行 brain
	DependsOn   []string `json:"depends_on,omitempty"` // 依赖的其他 feature ID
}

// ConstraintSpec 约束条件。
type ConstraintSpec struct {
	Type        string `json:"type"`        // technical/business/resource/time
	Description string `json:"description"`
	Severity    string `json:"severity"`    // hard/soft
}

// AcceptanceCriteria 验收标准。
type AcceptanceCriteria struct {
	CriteriaID  string `json:"criteria_id"`
	Description string `json:"description"`
	TestType    string `json:"test_type"`    // functional/performance/security/ux
	AutoTestable bool  `json:"auto_testable"` // 是否可自动化验证
}

// ---------------------------------------------------------------------------
// 辅助构造 / 方法
// ---------------------------------------------------------------------------

// NewRequirementSpec 创建一个带默认值的 RequirementSpec。
func NewRequirementSpec(rawGoal string) *RequirementSpec {
	return &RequirementSpec{
		SpecID:    fmt.Sprintf("spec-%d", time.Now().UnixNano()),
		RawGoal:   rawGoal,
		Priority:  "medium",
		CreatedAt: time.Now(),
	}
}

// AddFeature 添加功能规格。
func (s *RequirementSpec) AddFeature(f FeatureSpec) {
	s.Features = append(s.Features, f)
}

// AddConstraint 添加约束条件。
func (s *RequirementSpec) AddConstraint(c ConstraintSpec) {
	s.Constraints = append(s.Constraints, c)
}

// AddAcceptance 添加验收标准。
func (s *RequirementSpec) AddAcceptance(a AcceptanceCriteria) {
	s.Acceptance = append(s.Acceptance, a)
}

// ---------------------------------------------------------------------------
// RequirementParser 接口
// ---------------------------------------------------------------------------

// RequirementParser 需求解析器接口。
type RequirementParser interface {
	// Parse 将原始自然语言目标解析为结构化需求。
	Parse(ctx context.Context, rawGoal string) (*RequirementSpec, error)
	// Refine 根据反馈修正已有需求。
	Refine(ctx context.Context, spec *RequirementSpec, feedback string) (*RequirementSpec, error)
	// Validate 校验需求完整性，返回错误描述列表（空 = 通过）。
	Validate(spec *RequirementSpec) []string
}

// ---------------------------------------------------------------------------
// DefaultRequirementParser — 基于关键词的启发式实现（不调 LLM）
// ---------------------------------------------------------------------------

// DefaultRequirementParser 启发式需求解析器，通过关键词匹配推断类别与复杂度。
type DefaultRequirementParser struct{}

// NewDefaultRequirementParser 创建默认解析器。
func NewDefaultRequirementParser() *DefaultRequirementParser {
	return &DefaultRequirementParser{}
}

// categoryRule 类别推断规则：关键词 → category。
var categoryRules = []struct {
	keywords []string
	category string
}{
	{[]string{"网页", "web", "前端", "frontend", "html", "页面"}, "web_app"},
	{[]string{"命令行", "cli", "terminal", "终端", "shell"}, "cli_tool"},
	{[]string{"游戏", "game"}, "game"},
	{[]string{"api", "接口", "服务", "service", "微服务", "后端", "backend"}, "api_service"},
	{[]string{"库", "library", "sdk", "包", "package", "模块"}, "library"},
}

// Parse 解析原始需求文本为结构化 RequirementSpec。
func (p *DefaultRequirementParser) Parse(_ context.Context, rawGoal string) (*RequirementSpec, error) {
	if strings.TrimSpace(rawGoal) == "" {
		return nil, fmt.Errorf("requirement_parser: 原始需求不能为空")
	}

	spec := NewRequirementSpec(rawGoal)
	lower := strings.ToLower(rawGoal)

	// 1. 推断 category
	spec.Category = p.inferCategory(lower)

	// 2. 提取 features（按句号 / 换行 / 分号拆分）
	spec.Features = p.extractFeatures(rawGoal)

	// 3. 生成 parsed_goal
	spec.ParsedGoal = p.buildParsedGoal(spec)

	// 4. 推断 complexity 和 estimated_tasks
	featureCount := len(spec.Features)
	switch {
	case featureCount <= 2:
		spec.Complexity = "simple"
		spec.EstimatedTasks = featureCount + 1 // 至少 2
	case featureCount <= 5:
		spec.Complexity = "moderate"
		spec.EstimatedTasks = featureCount + 2
	default:
		spec.Complexity = "complex"
		spec.EstimatedTasks = featureCount + 3
	}
	if spec.EstimatedTasks < 2 {
		spec.EstimatedTasks = 2
	}

	// 5. 默认验收标准
	spec.Acceptance = p.defaultAcceptance(spec)

	return spec, nil
}

// Refine 将反馈合并到已有 spec 中。
func (p *DefaultRequirementParser) Refine(_ context.Context, spec *RequirementSpec, feedback string) (*RequirementSpec, error) {
	if spec == nil {
		return nil, fmt.Errorf("requirement_parser: spec 不能为 nil")
	}
	if strings.TrimSpace(feedback) == "" {
		return spec, nil
	}

	// 将反馈追加为额外 feature
	newFeatures := p.extractFeatures(feedback)
	for _, f := range newFeatures {
		spec.AddFeature(f)
	}

	// 重新推断 complexity
	featureCount := len(spec.Features)
	switch {
	case featureCount <= 2:
		spec.Complexity = "simple"
		spec.EstimatedTasks = featureCount + 1
	case featureCount <= 5:
		spec.Complexity = "moderate"
		spec.EstimatedTasks = featureCount + 2
	default:
		spec.Complexity = "complex"
		spec.EstimatedTasks = featureCount + 3
	}

	// 更新 parsed_goal
	spec.ParsedGoal = p.buildParsedGoal(spec)

	return spec, nil
}

// Validate 校验 spec 必填字段，返回错误描述列表。
func (p *DefaultRequirementParser) Validate(spec *RequirementSpec) []string {
	var errs []string
	if spec == nil {
		return []string{"spec 为 nil"}
	}
	if strings.TrimSpace(spec.RawGoal) == "" {
		errs = append(errs, "raw_goal 不能为空")
	}
	if len(spec.Features) == 0 {
		errs = append(errs, "至少需要 1 个 feature")
	}
	for i, f := range spec.Features {
		if strings.TrimSpace(f.Name) == "" {
			errs = append(errs, fmt.Sprintf("feature[%d] 名称不能为空", i))
		}
	}
	return errs
}

// ---------------------------------------------------------------------------
// 内部辅助方法
// ---------------------------------------------------------------------------

// inferCategory 基于关键词推断项目类别。
func (p *DefaultRequirementParser) inferCategory(lower string) string {
	for _, rule := range categoryRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.category
			}
		}
	}
	return "library" // 默认归类为 library
}

// splitSentences 将文本按常见分隔符拆分为句子列表。
func splitSentences(text string) []string {
	// 统一换行符
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// 依次按换行 → 句号 → 分号拆分
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		for _, seg := range strings.Split(line, "。") {
			for _, s := range strings.Split(seg, ";") {
				s = strings.TrimSpace(s)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return parts
}

// extractFeatures 从文本中提取功能列表。
func (p *DefaultRequirementParser) extractFeatures(text string) []FeatureSpec {
	sentences := splitSentences(text)
	var features []FeatureSpec

	for i, s := range sentences {
		if len(s) < 2 {
			continue // 跳过过短片段
		}
		features = append(features, FeatureSpec{
			FeatureID:   fmt.Sprintf("feat-%d", i+1),
			Name:        truncate(s, 50),
			Description: s,
			Priority:    "must_have",
		})
	}

	// 确保至少返回 1 个 feature
	if len(features) == 0 {
		features = append(features, FeatureSpec{
			FeatureID:   "feat-1",
			Name:        truncate(text, 50),
			Description: text,
			Priority:    "must_have",
		})
	}
	return features
}

// buildParsedGoal 基于 spec 信息生成结构化目标描述。
func (p *DefaultRequirementParser) buildParsedGoal(spec *RequirementSpec) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("构建一个 %s 项目", spec.Category))
	if len(spec.Features) > 0 {
		b.WriteString("，包含以下功能：")
		for i, f := range spec.Features {
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(f.Name)
		}
	}
	return b.String()
}

// defaultAcceptance 生成默认验收标准。
func (p *DefaultRequirementParser) defaultAcceptance(spec *RequirementSpec) []AcceptanceCriteria {
	ac := []AcceptanceCriteria{
		{
			CriteriaID:   "ac-1",
			Description:  "项目能够成功编译/构建",
			TestType:     "functional",
			AutoTestable: true,
		},
	}
	// 根据类别追加特定验收标准
	switch spec.Category {
	case "web_app":
		ac = append(ac, AcceptanceCriteria{
			CriteriaID:   "ac-2",
			Description:  "页面可在浏览器中正常访问",
			TestType:     "functional",
			AutoTestable: true,
		})
	case "api_service":
		ac = append(ac, AcceptanceCriteria{
			CriteriaID:   "ac-2",
			Description:  "API 端点返回正确 HTTP 状态码",
			TestType:     "functional",
			AutoTestable: true,
		})
	case "cli_tool":
		ac = append(ac, AcceptanceCriteria{
			CriteriaID:   "ac-2",
			Description:  "命令行帮助信息正常输出",
			TestType:     "functional",
			AutoTestable: true,
		})
	case "game":
		ac = append(ac, AcceptanceCriteria{
			CriteriaID:   "ac-2",
			Description:  "游戏主循环正常运行",
			TestType:     "functional",
			AutoTestable: false,
		})
	}
	return ac
}

// truncate 截断字符串到最大长度，超出部分用省略号代替。
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
