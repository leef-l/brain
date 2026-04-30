package kernel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/diaglog"
)

// AcceptanceCommandTimeout 是单条测试命令的默认超时（在 ctx 没有 deadline 时启用）。
var AcceptanceCommandTimeout = 60 * time.Second

// ---------------------------------------------------------------------------
// MACCS Wave 3 Batch 2 — 验收测试层
// 执行阶段完成后的验收验证，确认交付物是否满足需求。
// ---------------------------------------------------------------------------

// AcceptanceTestSuite 验收测试套件，包含一组针对项目的验收测试。
type AcceptanceTestSuite struct {
	SuiteID   string            `json:"suite_id"`
	ProjectID string            `json:"project_id"`
	Tests     []AcceptanceTest  `json:"tests"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AcceptanceTest 单个验收测试项，关联到具体的 AcceptanceCriteria。
type AcceptanceTest struct {
	TestID      string `json:"test_id"`
	CriteriaID  string `json:"criteria_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TestType    string `json:"test_type"`
	AutoRun     bool   `json:"auto_run"`
	Command     string `json:"command,omitempty"`
	Expected    string `json:"expected,omitempty"`
}

// AcceptanceTestResult 单个测试的执行结果。
type AcceptanceTestResult struct {
	ResultID     string        `json:"result_id"`
	TestID       string        `json:"test_id"`
	Passed       bool          `json:"passed"`
	ActualResult string        `json:"actual_result"`
	ErrorMsg     string        `json:"error_msg,omitempty"`
	Stdout       string        `json:"stdout,omitempty"`
	Stderr       string        `json:"stderr,omitempty"`
	ExitCode     int           `json:"exit_code,omitempty"`
	Duration     time.Duration `json:"duration"`
	ExecutedAt   time.Time     `json:"executed_at"`
}

// AcceptanceReport 验收报告，汇总整个测试套件的执行结果与最终裁定。
type AcceptanceReport struct {
	ReportID     string                 `json:"report_id"`
	SuiteID      string                 `json:"suite_id"`
	ProjectID    string                 `json:"project_id"`
	TotalTests   int                    `json:"total_tests"`
	PassedTests  int                    `json:"passed_tests"`
	FailedTests  int                    `json:"failed_tests"`
	SkippedTests int                    `json:"skipped_tests"`
	PassRate     float64                `json:"pass_rate"`
	Results      []AcceptanceTestResult `json:"results"`
	Verdict      string                 `json:"verdict"`
	FailedItems  []string               `json:"failed_items,omitempty"`
	GeneratedAt  time.Time              `json:"generated_at"`
}

// ---------------------------------------------------------------------------
// AcceptanceTester 接口
// ---------------------------------------------------------------------------

// AcceptanceTester 验收测试器接口，负责生成、执行验收测试并给出裁定。
type AcceptanceTester interface {
	GenerateTests(ctx context.Context, spec *RequirementSpec, proposal *DesignProposal) (*AcceptanceTestSuite, error)
	RunTests(ctx context.Context, suite *AcceptanceTestSuite, artifacts map[string]string) (*AcceptanceReport, error)
	Verdict(report *AcceptanceReport) string
}

// ---------------------------------------------------------------------------
// 辅助构造 / 方法
// ---------------------------------------------------------------------------

// NewAcceptanceTestSuite 创建一个带默认值的验收测试套件。
func NewAcceptanceTestSuite(projectID string) *AcceptanceTestSuite {
	return &AcceptanceTestSuite{
		SuiteID:   fmt.Sprintf("suite-%d", time.Now().UnixNano()),
		ProjectID: projectID,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]string),
	}
}

// AddTest 向套件中添加一个验收测试。
func (s *AcceptanceTestSuite) AddTest(t AcceptanceTest) {
	s.Tests = append(s.Tests, t)
}

// Summary 返回验收报告的一行摘要。
func (r *AcceptanceReport) Summary() string {
	return fmt.Sprintf("[%s] 总计 %d 项: 通过 %d, 失败 %d, 跳过 %d, 通过率 %.1f%%",
		r.Verdict, r.TotalTests, r.PassedTests, r.FailedTests, r.SkippedTests, r.PassRate)
}

// ---------------------------------------------------------------------------
// DefaultAcceptanceTester — 启发式实现（不调用 LLM）
// ---------------------------------------------------------------------------

// DefaultAcceptanceTester 基于规则的验收测试器，通过启发式逻辑生成和执行测试。
type DefaultAcceptanceTester struct{}

// NewDefaultAcceptanceTester 创建默认验收测试器。
func NewDefaultAcceptanceTester() *DefaultAcceptanceTester {
	return &DefaultAcceptanceTester{}
}

// GenerateTests 为每个 AcceptanceCriteria 生成对应的验收测试。
// 如果没有 AcceptanceCriteria，则为每个 feature 生成一个基础功能测试。
func (t *DefaultAcceptanceTester) GenerateTests(_ context.Context, spec *RequirementSpec, proposal *DesignProposal) (*AcceptanceTestSuite, error) {
	if spec == nil {
		return nil, fmt.Errorf("acceptance_tester: RequirementSpec 不能为空")
	}
	if proposal == nil {
		return nil, fmt.Errorf("acceptance_tester: DesignProposal 不能为空")
	}

	suite := NewAcceptanceTestSuite(proposal.SpecID)
	suite.Metadata["proposal_id"] = proposal.ProposalID

	if len(spec.Acceptance) > 0 {
		// 为每个 AcceptanceCriteria 生成测试
		for i, ac := range spec.Acceptance {
			test := t.buildTestFromCriteria(i+1, ac)
			suite.AddTest(test)
		}
	} else {
		// 没有验收标准时，为每个 feature 生成基础功能测试
		for i, feat := range spec.Features {
			suite.AddTest(AcceptanceTest{
				TestID:      fmt.Sprintf("at-%d", i+1),
				CriteriaID:  "",
				Name:        fmt.Sprintf("验证: %s", truncate(feat.Name, 40)),
				Description: fmt.Sprintf("验证功能 %s 是否正常工作", feat.Name),
				TestType:    "functional",
				AutoRun:     true,
				Command:     fmt.Sprintf("verify_%s", sanitizeName(feat.Name)),
				Expected:    "功能正常运行",
			})
		}
	}

	return suite, nil
}

// RunTests 执行验收测试套件，返回验收报告。
// 优先级：
//  1. AutoRun=false → skipped（需人工验证）
//  2. AutoRun=true 且 Command 非空 → 通过 sh -c 真实执行命令；
//     退出码 0=Passed，非 0/超时/错误=Failed；超时默认 60s（AcceptanceCommandTimeout）
//  3. AutoRun=true 且 Command 为空 → 退化到 artifacts 查找（手工/基于产物的测试）
func (t *DefaultAcceptanceTester) RunTests(ctx context.Context, suite *AcceptanceTestSuite, artifacts map[string]string) (*AcceptanceReport, error) {
	if suite == nil {
		return nil, fmt.Errorf("acceptance_tester: AcceptanceTestSuite 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	report := &AcceptanceReport{
		ReportID:    fmt.Sprintf("report-%d", time.Now().UnixNano()),
		SuiteID:     suite.SuiteID,
		ProjectID:   suite.ProjectID,
		TotalTests:  len(suite.Tests),
		GeneratedAt: time.Now(),
	}

	for _, test := range suite.Tests {
		start := time.Now()
		result := AcceptanceTestResult{
			ResultID:   fmt.Sprintf("res-%s", test.TestID),
			TestID:     test.TestID,
			ExecutedAt: start,
		}

		if !test.AutoRun {
			result.ActualResult = "需要人工验证，已跳过"
			result.Duration = time.Since(start)
			report.SkippedTests++
			report.Results = append(report.Results, result)
			continue
		}

		if test.Command != "" {
			// 真实命令执行
			runCtx := ctx
			var cancel context.CancelFunc
			if _, hasDeadline := ctx.Deadline(); !hasDeadline {
				runCtx, cancel = context.WithTimeout(ctx, AcceptanceCommandTimeout)
			}
			cmd := exec.CommandContext(runCtx, "sh", "-c", test.Command)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			if cancel != nil {
				cancel()
			}
			result.Stdout = stdout.String()
			result.Stderr = stderr.String()
			if cmd.ProcessState != nil {
				result.ExitCode = cmd.ProcessState.ExitCode()
			}
			if runErr == nil && result.ExitCode == 0 {
				result.Passed = true
				result.ActualResult = strings.TrimSpace(result.Stdout)
				if result.ActualResult == "" {
					result.ActualResult = "命令成功执行（无 stdout 输出）"
				}
			} else {
				result.Passed = false
				if runErr != nil {
					result.ErrorMsg = runErr.Error()
				} else {
					result.ErrorMsg = fmt.Sprintf("非零退出码: %d", result.ExitCode)
				}
				result.ActualResult = strings.TrimSpace(result.Stderr)
				if result.ActualResult == "" {
					result.ActualResult = result.ErrorMsg
				}
				report.FailedItems = append(report.FailedItems, fmt.Sprintf("%s: %s", test.TestID, test.Name))
			}
			diaglog.Info("acceptance_tester", "executed test command",
				"test_id", test.TestID,
				"exit_code", result.ExitCode,
				"passed", result.Passed)
		} else {
			// 无 Command：退化到 artifacts 查找
			diaglog.Info("acceptance_tester", "no command, falling back to artifacts lookup",
				"test_id", test.TestID,
				"criteria_id", test.CriteriaID)
			if artifacts != nil {
				if val, ok := artifacts[test.TestID]; ok {
					result.Passed = true
					result.ActualResult = val
				} else if val, ok := artifacts[test.CriteriaID]; ok {
					result.Passed = true
					result.ActualResult = val
				} else {
					result.Passed = false
					result.ActualResult = "未找到对应交付物"
					result.ErrorMsg = fmt.Sprintf("artifacts 中无匹配 key: %s/%s", test.TestID, test.CriteriaID)
					report.FailedItems = append(report.FailedItems, fmt.Sprintf("%s: %s", test.TestID, test.Name))
				}
			} else {
				result.Passed = false
				result.ActualResult = "无交付物"
				result.ErrorMsg = "artifacts 为空且 Command 为空"
				report.FailedItems = append(report.FailedItems, fmt.Sprintf("%s: %s", test.TestID, test.Name))
			}
		}

		result.Duration = time.Since(start)
		if result.Passed {
			report.PassedTests++
		} else {
			report.FailedTests++
		}
		report.Results = append(report.Results, result)
	}

	// 计算通过率（排除 skipped）
	executed := report.PassedTests + report.FailedTests
	if executed > 0 {
		report.PassRate = float64(report.PassedTests) / float64(executed) * 100
	}

	report.Verdict = t.Verdict(report)
	return report, nil
}

// Verdict 根据通过率给出验收裁定。
//   - PassRate >= 80% → "accepted"
//   - PassRate >= 50% → "partial"
//   - 其他 → "rejected"
func (t *DefaultAcceptanceTester) Verdict(report *AcceptanceReport) string {
	if report == nil {
		return "rejected"
	}
	switch {
	case report.PassRate >= 80:
		return "accepted"
	case report.PassRate >= 50:
		return "partial"
	default:
		return "rejected"
	}
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// buildTestFromCriteria 根据 AcceptanceCriteria 构建测试。
func (t *DefaultAcceptanceTester) buildTestFromCriteria(idx int, ac AcceptanceCriteria) AcceptanceTest {
	test := AcceptanceTest{
		TestID:      fmt.Sprintf("at-%d", idx),
		CriteriaID:  ac.CriteriaID,
		Name:        fmt.Sprintf("验收: %s", truncate(ac.Description, 40)),
		Description: ac.Description,
		TestType:    ac.TestType,
	}

	switch ac.TestType {
	case "functional":
		test.AutoRun = true
		test.Command = fmt.Sprintf("verify_%s", sanitizeName(ac.Description))
		test.Expected = "功能验证通过"
	case "performance":
		test.AutoRun = true
		test.Command = fmt.Sprintf("benchmark_%s", sanitizeName(ac.Description))
		test.Expected = "性能指标达标"
	case "security", "ux":
		test.AutoRun = false
		test.Expected = "需人工审核确认"
	default:
		test.AutoRun = ac.AutoTestable
		if ac.AutoTestable {
			test.Command = fmt.Sprintf("verify_%s", sanitizeName(ac.Description))
		}
		test.Expected = "验证通过"
	}

	return test
}

// sanitizeName 将名称转换为安全的标识符形式（小写、下划线分隔）。
func sanitizeName(name string) string {
	r := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ".", "_", "-", "_")
	s := r.Replace(strings.ToLower(strings.TrimSpace(name)))
	// 截断过长标识符
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
