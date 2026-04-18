package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// OrgPolicy 定义组织级授权策略，用于企业场景下的集中 policy 管理。
type OrgPolicy struct {
	OrgID           string   `json:"org_id"`
	AllowedBrains   []string `json:"allowed_brains,omitempty"`   // 允许使用的 brain kind（白名单）
	BlockedBrains   []string `json:"blocked_brains,omitempty"`   // 禁止使用的 brain kind（黑名单）
	MaxConcurrent   int      `json:"max_concurrent,omitempty"`   // 最大并发 run 数
	MaxBrains       int      `json:"max_brains,omitempty"`       // 最大 brain 安装数
	RequireApproval []string `json:"require_approval,omitempty"` // 需要审批的 approval class
	AuditLevel      string   `json:"audit_level,omitempty"`      // none / basic / full
}

// OrgAction 描述一个需要被组织策略检查的操作。
type OrgAction struct {
	Type      string // "install_brain" / "start_run" / "delegate" / "tool_call"
	BrainKind string
	UserID    string
}

// OrgPolicyEnforcer 定义组织策略执行器接口。
type OrgPolicyEnforcer interface {
	// Check 检查给定 action 是否被组织策略允许。
	Check(ctx context.Context, action OrgAction) error
	// LoadPolicy 从指定路径加载组织策略文件。
	LoadPolicy(path string) error
	// Policy 返回当前加载的策略副本（可能为 nil）。
	Policy() *OrgPolicy
}

// FileOrgPolicyEnforcer 从 JSON 文件加载组织策略并执行检查。
type FileOrgPolicyEnforcer struct {
	mu     sync.RWMutex
	policy *OrgPolicy
}

// NewFileOrgPolicyEnforcer 创建一个空的 FileOrgPolicyEnforcer。
// 调用 LoadPolicy 加载策略后才会生效。
func NewFileOrgPolicyEnforcer() *FileOrgPolicyEnforcer {
	return &FileOrgPolicyEnforcer{}
}

// DefaultOrgPolicyPath 返回默认的组织策略文件路径：~/.brain/org-policy.json
func DefaultOrgPolicyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".brain", "org-policy.json")
}

// LoadPolicy 从指定路径加载组织策略 JSON 文件。
func (e *FileOrgPolicyEnforcer) LoadPolicy(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("org policy: read %s: %w", path, err)
	}
	var p OrgPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("org policy: parse %s: %w", path, err)
	}
	e.mu.Lock()
	e.policy = &p
	e.mu.Unlock()
	return nil
}

// Policy 返回当前策略的副本。
func (e *FileOrgPolicyEnforcer) Policy() *OrgPolicy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.policy == nil {
		return nil
	}
	cp := *e.policy
	cp.AllowedBrains = append([]string(nil), e.policy.AllowedBrains...)
	cp.BlockedBrains = append([]string(nil), e.policy.BlockedBrains...)
	cp.RequireApproval = append([]string(nil), e.policy.RequireApproval...)
	return &cp
}

// Check 验证 action 是否被当前组织策略允许。
// 如果没有加载策略，则默认放行（开放模式）。
func (e *FileOrgPolicyEnforcer) Check(ctx context.Context, action OrgAction) error {
	e.mu.RLock()
	p := e.policy
	e.mu.RUnlock()

	if p == nil {
		return nil // 无策略时默认放行
	}

	// 检查 brain 黑名单
	if len(p.BlockedBrains) > 0 && action.BrainKind != "" {
		for _, blocked := range p.BlockedBrains {
			if strings.EqualFold(blocked, action.BrainKind) {
				return fmt.Errorf("org policy: brain %q is blocked by organization policy (org: %s)", action.BrainKind, p.OrgID)
			}
		}
	}

	// 检查 brain 白名单（仅在白名单非空时生效）
	if len(p.AllowedBrains) > 0 && action.BrainKind != "" {
		allowed := false
		for _, a := range p.AllowedBrains {
			if a == "*" || strings.EqualFold(a, action.BrainKind) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("org policy: brain %q is not in the allowed list (org: %s)", action.BrainKind, p.OrgID)
		}
	}

	return nil
}

// LoadOrgPolicyIfExists 尝试从默认路径加载组织策略。
// 如果文件不存在返回 nil enforcer（无策略模式），文件存在但解析失败返回 error。
func LoadOrgPolicyIfExists() (OrgPolicyEnforcer, error) {
	path := DefaultOrgPolicyPath()
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	enforcer := NewFileOrgPolicyEnforcer()
	if err := enforcer.LoadPolicy(path); err != nil {
		return nil, err
	}
	return enforcer, nil
}
