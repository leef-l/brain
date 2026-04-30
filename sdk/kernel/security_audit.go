// security_audit.go — 安全审计框架（Security Audit）
// MACCS Wave 6 Batch 2 — 提供输入验证、沙箱检查、权限审计和安全报告能力。
package kernel

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SecurityRisk 标识安全风险类型。
type SecurityRisk string

const (
	RiskCommandInjection SecurityRisk = "command_injection"
	RiskPathTraversal    SecurityRisk = "path_traversal"
	RiskSensitiveData    SecurityRisk = "sensitive_data"
	RiskExcessivePerms   SecurityRisk = "excessive_permissions"
	RiskUnsafeInput      SecurityRisk = "unsafe_input"
	RiskResourceAbuse    SecurityRisk = "resource_abuse"
)

// SecurityFinding 描述一个安全发现。
type SecurityFinding struct {
	FindingID   string       `json:"finding_id"`
	Risk        SecurityRisk `json:"risk"`
	Severity    string       `json:"severity"` // critical/high/medium/low
	Component   string       `json:"component"`
	Description string       `json:"description"`
	Evidence    string       `json:"evidence,omitempty"`
	Remediation string       `json:"remediation"`
	DetectedAt  time.Time    `json:"detected_at"`
}

// SecurityAuditReport 安全审计报告，汇总所有检查结果。
type SecurityAuditReport struct {
	ReportID      string            `json:"report_id"`
	Findings      []SecurityFinding `json:"findings"`
	TotalFindings int               `json:"total_findings"`
	CriticalCount int               `json:"critical_count"`
	HighCount     int               `json:"high_count"`
	PassedChecks  int               `json:"passed_checks"`
	TotalChecks   int               `json:"total_checks"`
	RiskScore     float64           `json:"risk_score"` // 0-100, 越低越安全
	AuditedAt     time.Time         `json:"audited_at"`
}

// Summary 返回审计报告的可读摘要。
func (r *SecurityAuditReport) Summary() string {
	return fmt.Sprintf(
		"SecurityAuditReport[%s] score=%.1f findings=%d (critical=%d high=%d) passed=%d/%d audited_at=%s",
		r.ReportID, r.RiskScore, r.TotalFindings,
		r.CriticalCount, r.HighCount,
		r.PassedChecks, r.TotalChecks,
		r.AuditedAt.Format(time.RFC3339),
	)
}

// SecurityRule 定义一条安全检查规则。
type SecurityRule struct {
	RuleID   string                              `json:"rule_id"`
	Name     string                              `json:"name"`
	Risk     SecurityRisk                        `json:"risk"`
	Severity string                              `json:"severity"`
	CheckFn  func(input string) *SecurityFinding `json:"-"`
	Enabled  bool                                `json:"enabled"`
}

// SecurityAuditor 安全审计器接口。
type SecurityAuditor interface {
	ValidateInput(input string) []SecurityFinding
	AuditPath(path string) []SecurityFinding
	AuditCommand(command string) []SecurityFinding
	FullAudit(inputs []string, paths []string, commands []string) *SecurityAuditReport
	AddRule(rule SecurityRule)
	GetRules() []SecurityRule
}

// DefaultSecurityAuditor 默认安全审计器实现。
type DefaultSecurityAuditor struct {
	rules     []SecurityRule
	findingNo int
}

// 注入模式正则（预编译）
var (
	reSubshell = regexp.MustCompile(`\$\(`)
	reBacktick = regexp.MustCompile("`")
	rePipeExec = regexp.MustCompile(`\|\s*\S`)
	reCurlPipe = regexp.MustCompile(`(?i)curl\s.*\|\s*sh`)
)

// NewSecurityAuditor 创建带默认规则集的安全审计器。
func NewSecurityAuditor() *DefaultSecurityAuditor {
	a := &DefaultSecurityAuditor{}
	a.registerDefaults()
	return a
}

func (a *DefaultSecurityAuditor) nextID() string {
	a.findingNo++
	return fmt.Sprintf("SF-%04d", a.findingNo)
}

// registerDefaults 注册内置安全规则。
func (a *DefaultSecurityAuditor) registerDefaults() {
	// 输入验证规则
	a.rules = append(a.rules, SecurityRule{
		RuleID: "INP-001", Name: "命令注入检测", Risk: RiskCommandInjection, Severity: "critical", Enabled: true,
		CheckFn: func(input string) *SecurityFinding {
			patterns := []string{"; rm", "$(", "&&", "||"}
			for _, p := range patterns {
				if strings.Contains(input, p) {
					return &SecurityFinding{
						Risk: RiskCommandInjection, Severity: "critical", Component: "input_validator",
						Description: "检测到潜在命令注入模式", Evidence: p, Remediation: "对输入进行转义或使用白名单校验",
					}
				}
			}
			if reBacktick.MatchString(input) {
				return &SecurityFinding{
					Risk: RiskCommandInjection, Severity: "critical", Component: "input_validator",
					Description: "检测到反引号命令替换", Evidence: "`", Remediation: "禁止使用反引号执行命令",
				}
			}
			return nil
		},
	})
	a.rules = append(a.rules, SecurityRule{
		RuleID: "INP-002", Name: "管道注入检测", Risk: RiskUnsafeInput, Severity: "high", Enabled: true,
		CheckFn: func(input string) *SecurityFinding {
			if rePipeExec.MatchString(input) {
				return &SecurityFinding{
					Risk: RiskUnsafeInput, Severity: "high", Component: "input_validator",
					Description: "检测到管道执行模式", Evidence: "| <command>", Remediation: "避免将用户输入直接传入管道",
				}
			}
			return nil
		},
	})

	// 路径审计规则
	a.rules = append(a.rules, SecurityRule{
		RuleID: "PATH-001", Name: "路径遍历检测", Risk: RiskPathTraversal, Severity: "high", Enabled: true,
		CheckFn: func(path string) *SecurityFinding {
			if strings.Contains(path, "../") {
				return &SecurityFinding{
					Risk: RiskPathTraversal, Severity: "high", Component: "path_auditor",
					Description: "检测到路径遍历模式", Evidence: "../", Remediation: "使用 filepath.Clean 规范化路径",
				}
			}
			return nil
		},
	})
	a.rules = append(a.rules, SecurityRule{
		RuleID: "PATH-002", Name: "敏感目录访问检测", Risk: RiskSensitiveData, Severity: "critical", Enabled: true,
		CheckFn: func(path string) *SecurityFinding {
			sensitive := []string{"/etc/shadow", "/etc/passwd", "/root/"}
			for _, s := range sensitive {
				if strings.HasPrefix(path, s) {
					return &SecurityFinding{
						Risk: RiskSensitiveData, Severity: "critical", Component: "path_auditor",
						Description: "检测到敏感目录访问", Evidence: s, Remediation: "限制访问路径到工作目录范围内",
					}
				}
			}
			return nil
		},
	})

	// 命令审计规则
	a.rules = append(a.rules, SecurityRule{
		RuleID: "CMD-001", Name: "危险删除命令检测", Risk: RiskCommandInjection, Severity: "critical", Enabled: true,
		CheckFn: func(cmd string) *SecurityFinding {
			if strings.Contains(cmd, "rm -rf") {
				return &SecurityFinding{
					Risk: RiskCommandInjection, Severity: "critical", Component: "command_auditor",
					Description: "检测到危险删除命令", Evidence: "rm -rf", Remediation: "使用安全删除 API 并限制删除范围",
				}
			}
			return nil
		},
	})
	a.rules = append(a.rules, SecurityRule{
		RuleID: "CMD-002", Name: "权限滥用检测", Risk: RiskExcessivePerms, Severity: "high", Enabled: true,
		CheckFn: func(cmd string) *SecurityFinding {
			if strings.Contains(cmd, "chmod 777") {
				return &SecurityFinding{
					Risk: RiskExcessivePerms, Severity: "high", Component: "command_auditor",
					Description: "检测到过度宽松权限设置", Evidence: "chmod 777", Remediation: "使用最小权限原则设置文件权限",
				}
			}
			return nil
		},
	})
	a.rules = append(a.rules, SecurityRule{
		RuleID: "CMD-003", Name: "远程代码执行检测", Risk: RiskCommandInjection, Severity: "critical", Enabled: true,
		CheckFn: func(cmd string) *SecurityFinding {
			if reCurlPipe.MatchString(cmd) {
				return &SecurityFinding{
					Risk: RiskCommandInjection, Severity: "critical", Component: "command_auditor",
					Description: "检测到远程代码执行模式", Evidence: "curl | sh", Remediation: "先下载脚本审查后再执行",
				}
			}
			return nil
		},
	})
	a.rules = append(a.rules, SecurityRule{
		RuleID: "CMD-004", Name: "动态执行检测", Risk: RiskUnsafeInput, Severity: "high", Enabled: true,
		CheckFn: func(cmd string) *SecurityFinding {
			for _, kw := range []string{"eval ", "exec "} {
				if strings.Contains(cmd, kw) {
					return &SecurityFinding{
						Risk: RiskUnsafeInput, Severity: "high", Component: "command_auditor",
						Description: "检测到动态执行调用", Evidence: strings.TrimSpace(kw), Remediation: "避免使用 eval/exec 执行动态内容",
					}
				}
			}
			return nil
		},
	})
}

// ValidateInput 检测输入中的常见注入模式。
func (a *DefaultSecurityAuditor) ValidateInput(input string) []SecurityFinding {
	return a.runRules(input, "input_validator")
}

// AuditPath 检测路径遍历和敏感目录访问。
func (a *DefaultSecurityAuditor) AuditPath(path string) []SecurityFinding {
	return a.runRules(path, "path_auditor")
}

// AuditCommand 检测危险命令。
func (a *DefaultSecurityAuditor) AuditCommand(command string) []SecurityFinding {
	return a.runRules(command, "command_auditor")
}

// runRules 对给定输入执行匹配组件的所有已启用规则。
func (a *DefaultSecurityAuditor) runRules(input, component string) []SecurityFinding {
	var findings []SecurityFinding
	for _, rule := range a.rules {
		if !rule.Enabled || rule.CheckFn == nil {
			continue
		}
		if f := rule.CheckFn(input); f != nil && f.Component == component {
			f.FindingID = a.nextID()
			f.DetectedAt = time.Now()
			findings = append(findings, *f)
		}
	}
	return findings
}

// FullAudit 执行所有检查并生成审计报告。
func (a *DefaultSecurityAuditor) FullAudit(inputs []string, paths []string, commands []string) *SecurityAuditReport {
	var allFindings []SecurityFinding
	totalChecks := 0
	for _, in := range inputs {
		totalChecks++
		allFindings = append(allFindings, a.ValidateInput(in)...)
	}
	for _, p := range paths {
		totalChecks++
		allFindings = append(allFindings, a.AuditPath(p)...)
	}
	for _, c := range commands {
		totalChecks++
		allFindings = append(allFindings, a.AuditCommand(c)...)
	}
	if totalChecks == 0 {
		totalChecks = 1 // 避免除零
	}

	var critCount, highCount, medCount, lowCount int
	for _, f := range allFindings {
		switch f.Severity {
		case "critical":
			critCount++
		case "high":
			highCount++
		case "medium":
			medCount++
		case "low":
			lowCount++
		}
	}

	score := float64(critCount*25+highCount*15+medCount*5+lowCount*1) / float64(totalChecks) * 100
	if score > 100 {
		score = 100
	}

	return &SecurityAuditReport{
		ReportID:      fmt.Sprintf("SAR-%d", time.Now().UnixMilli()),
		Findings:      allFindings,
		TotalFindings: len(allFindings),
		CriticalCount: critCount,
		HighCount:     highCount,
		PassedChecks:  totalChecks - len(allFindings),
		TotalChecks:   totalChecks,
		RiskScore:     score,
		AuditedAt:     time.Now(),
	}
}

// AddRule 添加自定义安全规则。
func (a *DefaultSecurityAuditor) AddRule(rule SecurityRule) {
	a.rules = append(a.rules, rule)
}

// GetRules 返回所有已注册的安全规则。
func (a *DefaultSecurityAuditor) GetRules() []SecurityRule {
	out := make([]SecurityRule, len(a.rules))
	copy(out, a.rules)
	return out
}
