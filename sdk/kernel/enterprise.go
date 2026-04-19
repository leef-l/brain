package kernel

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PermissionEntry 描述一条细粒度权限规则。
type PermissionEntry struct {
	Resource string `json:"resource"` // "brain:code" / "tool:file.write" / "feature:learning"
	Action   string `json:"action"`   // "execute" / "configure" / "view" / "*"
	Effect   string `json:"effect"`   // "allow" / "deny"
}

// PermissionMatrix 是基于 resource-action 的细粒度权限矩阵。
type PermissionMatrix struct {
	Version     int               `json:"version"`
	OrgID       string            `json:"org_id"`
	Edition     string            `json:"edition"`
	Permissions []PermissionEntry `json:"permissions"`
}

// IsAllowed 检查指定 resource+action 是否被允许。
// 评估顺序：显式 deny → 显式 allow → 默认 deny。
func (m *PermissionMatrix) IsAllowed(resource, action string) bool {
	if m == nil {
		return true // 无权限矩阵时默认放行
	}
	hasExplicitAllow := false
	for _, p := range m.Permissions {
		if !matchResource(p.Resource, resource) {
			continue
		}
		if p.Action != "*" && !strings.EqualFold(p.Action, action) {
			continue
		}
		if p.Effect == "deny" {
			return false
		}
		if p.Effect == "allow" {
			hasExplicitAllow = true
		}
	}
	return hasExplicitAllow
}

func matchResource(pattern, resource string) bool {
	if pattern == "*" {
		return true
	}
	if strings.EqualFold(pattern, resource) {
		return true
	}
	// 前缀匹配：pattern "brain:*" 匹配 "brain:code"
	if strings.HasSuffix(pattern, ":*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(strings.ToLower(resource), strings.ToLower(prefix))
	}
	return false
}

// RevocationList 记录被吊销的 license ID 列表。
type RevocationList struct {
	Version    int       `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
	RevokedIDs []string  `json:"revoked_ids"`
	Signature  string    `json:"signature,omitempty"`
}

// RevocationStore 管理 license 吊销列表。
type RevocationStore interface {
	IsRevoked(ctx context.Context, licenseID string) (bool, error)
	Update(ctx context.Context, crl *RevocationList) error
	Load(ctx context.Context) (*RevocationList, error)
}

// FileRevocationStore 基于 JSON 文件的吊销列表存储。
type FileRevocationStore struct {
	path string
	mu   sync.RWMutex
	list *RevocationList
}

func NewFileRevocationStore(path string) *FileRevocationStore {
	if path == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "."
		}
		path = filepath.Join(home, ".brain", "revocation.json")
	}
	return &FileRevocationStore{path: path}
}

func (s *FileRevocationStore) IsRevoked(_ context.Context, licenseID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.list == nil {
		return false, nil
	}
	for _, id := range s.list.RevokedIDs {
		if id == licenseID {
			return true, nil
		}
	}
	return false, nil
}

func (s *FileRevocationStore) Update(_ context.Context, crl *RevocationList) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = crl
	data, err := json.MarshalIndent(crl, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *FileRevocationStore) Load(_ context.Context) (*RevocationList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.list = &RevocationList{Version: 1}
			return s.list, nil
		}
		return nil, err
	}
	var crl RevocationList
	if err := json.Unmarshal(data, &crl); err != nil {
		return nil, err
	}
	s.list = &crl
	return &crl, nil
}

// EnterpriseEnforcer 是企业级授权执行器。
// 整合 OrgPolicy + PermissionMatrix + License + RevocationList。
type EnterpriseEnforcer struct {
	orgEnforcer  OrgPolicyEnforcer
	permissions  *PermissionMatrix
	revocations  RevocationStore
	licenseID    string
	mu           sync.RWMutex
}

// EnterpriseConfig 配置企业级授权。
type EnterpriseConfig struct {
	OrgPolicyPath    string // 组织策略 JSON 路径
	PermissionPath   string // 权限矩阵 JSON 路径
	RevocationPath   string // 吊销列表路径
	LicenseID        string // 当前 license ID（用于吊销检查）
}

// NewEnterpriseEnforcer 创建企业级授权执行器。
func NewEnterpriseEnforcer(cfg EnterpriseConfig) (*EnterpriseEnforcer, error) {
	ee := &EnterpriseEnforcer{
		licenseID: cfg.LicenseID,
	}

	// 加载组织策略
	if cfg.OrgPolicyPath != "" {
		enforcer := NewFileOrgPolicyEnforcer()
		if err := enforcer.LoadPolicy(cfg.OrgPolicyPath); err != nil {
			return nil, fmt.Errorf("enterprise: load org policy: %w", err)
		}
		ee.orgEnforcer = enforcer
	}

	// 加载权限矩阵
	if cfg.PermissionPath != "" {
		data, err := os.ReadFile(cfg.PermissionPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("enterprise: load permissions: %w", err)
			}
		} else {
			var pm PermissionMatrix
			if err := json.Unmarshal(data, &pm); err != nil {
				return nil, fmt.Errorf("enterprise: parse permissions: %w", err)
			}
			ee.permissions = &pm
		}
	}

	// 加载吊销列表
	ee.revocations = NewFileRevocationStore(cfg.RevocationPath)
	if _, err := ee.revocations.Load(context.Background()); err != nil {
		return nil, fmt.Errorf("enterprise: load revocations: %w", err)
	}

	return ee, nil
}

// Check 执行企业级授权三层检查：License 吊销 → 组织策略 → 权限矩阵。
func (ee *EnterpriseEnforcer) Check(ctx context.Context, action OrgAction) error {
	// 1. License 吊销检查
	if ee.licenseID != "" && ee.revocations != nil {
		revoked, err := ee.revocations.IsRevoked(ctx, ee.licenseID)
		if err != nil {
			return fmt.Errorf("enterprise: revocation check: %w", err)
		}
		if revoked {
			return fmt.Errorf("enterprise: license %s has been revoked", ee.licenseID)
		}
	}

	// 2. 组织策略检查
	if ee.orgEnforcer != nil {
		if err := ee.orgEnforcer.Check(ctx, action); err != nil {
			return err
		}
	}

	// 3. 权限矩阵检查
	ee.mu.RLock()
	pm := ee.permissions
	ee.mu.RUnlock()
	if pm != nil {
		resource := "brain:" + action.BrainKind
		if !pm.IsAllowed(resource, action.Type) {
			return fmt.Errorf("enterprise: action %s on %s denied by permission matrix (org: %s)",
				action.Type, resource, pm.OrgID)
		}
	}

	return nil
}

// UpdatePermissions 运行时更新权限矩阵。
func (ee *EnterpriseEnforcer) UpdatePermissions(pm *PermissionMatrix) {
	ee.mu.Lock()
	defer ee.mu.Unlock()
	ee.permissions = pm
}

// UpdateRevocations 运行时更新吊销列表。
func (ee *EnterpriseEnforcer) UpdateRevocations(ctx context.Context, crl *RevocationList) error {
	return ee.revocations.Update(ctx, crl)
}

// VerifyRevocationSignature 校验吊销列表签名（防伪造）。
func VerifyRevocationSignature(crl *RevocationList, publicKey ed25519.PublicKey) bool {
	if crl == nil || crl.Signature == "" || len(publicKey) == 0 {
		return false
	}
	payload, err := json.Marshal(struct {
		Version    int      `json:"version"`
		RevokedIDs []string `json:"revoked_ids"`
	}{Version: crl.Version, RevokedIDs: crl.RevokedIDs})
	if err != nil {
		return false
	}
	sig := []byte(crl.Signature)
	return ed25519.Verify(publicKey, payload, sig)
}

// compile-time check
var _ OrgPolicyEnforcer = (*EnterpriseEnforcer)(nil)

// Policy 返回内嵌 org enforcer 的策略。
func (ee *EnterpriseEnforcer) Policy() *OrgPolicy {
	if ee.orgEnforcer != nil {
		return ee.orgEnforcer.Policy()
	}
	return nil
}

// LoadPolicy 委托给内嵌 org enforcer。
func (ee *EnterpriseEnforcer) LoadPolicy(path string) error {
	if ee.orgEnforcer == nil {
		ee.orgEnforcer = NewFileOrgPolicyEnforcer()
	}
	return ee.orgEnforcer.LoadPolicy(path)
}
