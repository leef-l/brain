package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// MACCS Wave 3 Batch 3 — 交付生成器
// 验收通过后生成交付物清单，包含源码、文档、摘要等结构化交付物。
// ---------------------------------------------------------------------------

// DeliveryManifest 交付清单，汇总一次项目交付的所有产出物。
type DeliveryManifest struct {
	ManifestID    string             `json:"manifest_id"`
	ProjectID     string             `json:"project_id"`
	ProjectName   string             `json:"project_name"`
	Version       string             `json:"version"`
	Artifacts     []DeliveryArtifact `json:"artifacts"`
	Documentation []DocEntry         `json:"documentation"`
	Summary       DeliverySummary    `json:"summary"`
	CreatedAt     time.Time          `json:"created_at"`
}

// DeliveryArtifact 单个交付物条目。
type DeliveryArtifact struct {
	ArtifactID  string `json:"artifact_id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // source_code/binary/config/test/doc/asset
	Path        string `json:"path"`
	Size        int64  `json:"size,omitempty"`
	Checksum    string `json:"checksum,omitempty"`
	Description string `json:"description"`
	BrainKind   string `json:"brain_kind,omitempty"` // 哪个 brain 产出的
}

// DocEntry 文档条目。
type DocEntry struct {
	DocID   string `json:"doc_id"`
	Title   string `json:"title"`
	Type    string `json:"type"`    // readme/api_doc/architecture/changelog/user_guide
	Content string `json:"content"`
	Format  string `json:"format"` // markdown/plaintext/html
}

// DeliverySummary 交付摘要统计。
type DeliverySummary struct {
	TotalArtifacts int      `json:"total_artifacts"`
	TotalDocs      int      `json:"total_docs"`
	CodeFiles      int      `json:"code_files"`
	TestFiles      int      `json:"test_files"`
	PassRate       float64  `json:"pass_rate"`          // 从 AcceptanceReport 继承
	Highlights     []string `json:"highlights"`         // 关键亮点
	Warnings       []string `json:"warnings,omitempty"` // 注意事项
}

// ---------------------------------------------------------------------------
// DeliveryGenerator 接口
// ---------------------------------------------------------------------------

// DeliveryGenerator 交付生成器接口，负责在验收通过后组装交付清单。
type DeliveryGenerator interface {
	Generate(ctx context.Context, spec *RequirementSpec, proposal *DesignProposal, report *AcceptanceReport) (*DeliveryManifest, error)
	GenerateReadme(spec *RequirementSpec, proposal *DesignProposal) DocEntry
	GenerateChangelog(proposal *DesignProposal) DocEntry
}

// ---------------------------------------------------------------------------
// 辅助构造 / 方法
// ---------------------------------------------------------------------------

// NewDeliveryManifest 创建一个带默认值的交付清单。
func NewDeliveryManifest(projectID, projectName string) *DeliveryManifest {
	return &DeliveryManifest{
		ManifestID:  fmt.Sprintf("manifest-%d", time.Now().UnixNano()),
		ProjectID:   projectID,
		ProjectName: projectName,
		Version:     "1.0.0",
		CreatedAt:   time.Now(),
	}
}

// AddArtifact 向清单中添加交付物。
func (m *DeliveryManifest) AddArtifact(a DeliveryArtifact) {
	m.Artifacts = append(m.Artifacts, a)
}

// AddDoc 向清单中添加文档。
func (m *DeliveryManifest) AddDoc(d DocEntry) {
	m.Documentation = append(m.Documentation, d)
}

// ComputeSummary 基于当前交付物和文档计算摘要。
func (m *DeliveryManifest) ComputeSummary(passRate float64) {
	var codeFiles, testFiles int
	for _, a := range m.Artifacts {
		switch a.Type {
		case "source_code":
			codeFiles++
		case "test":
			testFiles++
		}
	}

	var highlights []string
	if codeFiles > 0 {
		highlights = append(highlights, fmt.Sprintf("包含 %d 个源码文件", codeFiles))
	}
	if testFiles > 0 {
		highlights = append(highlights, fmt.Sprintf("包含 %d 个测试文件", testFiles))
	}
	if len(m.Documentation) > 0 {
		highlights = append(highlights, fmt.Sprintf("附带 %d 份文档", len(m.Documentation)))
	}
	if passRate >= 80 {
		highlights = append(highlights, fmt.Sprintf("验收通过率 %.1f%%", passRate))
	}

	var warnings []string
	if passRate < 100 && passRate > 0 {
		warnings = append(warnings, fmt.Sprintf("部分验收未通过（通过率 %.1f%%），建议复查失败项", passRate))
	}
	if testFiles == 0 {
		warnings = append(warnings, "未包含测试文件，建议补充测试")
	}

	m.Summary = DeliverySummary{
		TotalArtifacts: len(m.Artifacts),
		TotalDocs:      len(m.Documentation),
		CodeFiles:      codeFiles,
		TestFiles:       testFiles,
		PassRate:        passRate,
		Highlights:      highlights,
		Warnings:        warnings,
	}
}

// ---------------------------------------------------------------------------
// DefaultDeliveryGenerator — 启发式实现
// ---------------------------------------------------------------------------

// DefaultDeliveryGenerator 基于规则的交付生成器，从需求、方案和验收报告中组装清单。
type DefaultDeliveryGenerator struct{}

// NewDefaultDeliveryGenerator 创建默认交付生成器。
func NewDefaultDeliveryGenerator() *DefaultDeliveryGenerator {
	return &DefaultDeliveryGenerator{}
}

// Generate 基于需求、方案和验收报告生成完整的交付清单。
func (g *DefaultDeliveryGenerator) Generate(_ context.Context, spec *RequirementSpec, proposal *DesignProposal, report *AcceptanceReport) (*DeliveryManifest, error) {
	if spec == nil {
		return nil, fmt.Errorf("delivery_generator: RequirementSpec 不能为空")
	}
	if proposal == nil {
		return nil, fmt.Errorf("delivery_generator: DesignProposal 不能为空")
	}
	if report == nil {
		return nil, fmt.Errorf("delivery_generator: AcceptanceReport 不能为空")
	}

	projectName := proposal.Title
	if projectName == "" {
		projectName = spec.ParsedGoal
	}

	manifest := NewDeliveryManifest(proposal.SpecID, projectName)

	// 为每个 DesignTask 生成交付物
	for i, task := range proposal.TaskBreakdown {
		artifactType := "source_code"
		if task.BrainKind == "verifier" {
			artifactType = "test"
		}

		artifact := DeliveryArtifact{
			ArtifactID:  fmt.Sprintf("artifact-%d", i+1),
			Name:        task.Name,
			Type:        artifactType,
			Path:        fmt.Sprintf("src/%s", sanitizeName(task.Name)),
			Description: task.Description,
			BrainKind:   task.BrainKind,
		}
		manifest.AddArtifact(artifact)
	}

	// 自动生成 README 和 CHANGELOG 文档
	readme := g.GenerateReadme(spec, proposal)
	manifest.AddDoc(readme)

	changelog := g.GenerateChangelog(proposal)
	manifest.AddDoc(changelog)

	// 从验收报告提取 pass_rate 并计算摘要
	manifest.ComputeSummary(report.PassRate)

	return manifest, nil
}

// GenerateReadme 基于需求和方案生成 markdown 格式的 README 文档。
func (g *DefaultDeliveryGenerator) GenerateReadme(spec *RequirementSpec, proposal *DesignProposal) DocEntry {
	var b strings.Builder

	title := proposal.Title
	if title == "" {
		title = spec.ParsedGoal
	}

	b.WriteString(fmt.Sprintf("# %s\n\n", title))

	// 项目目标
	b.WriteString("## 项目目标\n\n")
	b.WriteString(spec.RawGoal)
	b.WriteString("\n\n")

	// 功能列表
	if len(spec.Features) > 0 {
		b.WriteString("## 功能列表\n\n")
		for _, feat := range spec.Features {
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", feat.Name, feat.Description))
		}
		b.WriteString("\n")
	}

	// 架构概述
	if proposal.Architecture.Pattern != "" {
		b.WriteString("## 架构概述\n\n")
		b.WriteString(fmt.Sprintf("- 架构模式: %s\n", proposal.Architecture.Pattern))
		if proposal.Architecture.Rationale != "" {
			b.WriteString(fmt.Sprintf("- 设计理由: %s\n", proposal.Architecture.Rationale))
		}
		if proposal.Architecture.DataFlow != "" {
			b.WriteString(fmt.Sprintf("- 数据流: %s\n", proposal.Architecture.DataFlow))
		}
		b.WriteString("\n")
	}

	// 技术栈
	if len(proposal.Architecture.TechStack) > 0 {
		b.WriteString("## 技术栈\n\n")
		for _, tech := range proposal.Architecture.TechStack {
			b.WriteString(fmt.Sprintf("- %s\n", tech))
		}
		b.WriteString("\n")
	}

	return DocEntry{
		DocID:   "doc-readme",
		Title:   "README",
		Type:    "readme",
		Content: b.String(),
		Format:  "markdown",
	}
}

// GenerateChangelog 基于方案的任务列表生成 changelog 文档。
func (g *DefaultDeliveryGenerator) GenerateChangelog(proposal *DesignProposal) DocEntry {
	var b strings.Builder

	b.WriteString("# Changelog\n\n")
	b.WriteString(fmt.Sprintf("## v1.0.0 (%s)\n\n", time.Now().Format("2006-01-02"))	)

	if len(proposal.TaskBreakdown) > 0 {
		b.WriteString("### 变更列表\n\n")
		for _, task := range proposal.TaskBreakdown {
			prefix := "feat"
			if task.BrainKind == "verifier" {
				prefix = "test"
			}
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", prefix, task.Name))
		}
		b.WriteString("\n")
	}

	if len(proposal.RiskAssessment) > 0 {
		b.WriteString("### 已知风险\n\n")
		for _, risk := range proposal.RiskAssessment {
			b.WriteString(fmt.Sprintf("- [%s] %s\n", risk.Severity, risk.Description))
		}
		b.WriteString("\n")
	}

	return DocEntry{
		DocID:   "doc-changelog",
		Title:   "CHANGELOG",
		Type:    "changelog",
		Content: b.String(),
		Format:  "markdown",
	}
}
