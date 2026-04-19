package kernel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPermissionMatrixAllowDeny(t *testing.T) {
	pm := &PermissionMatrix{
		Permissions: []PermissionEntry{
			{Resource: "brain:code", Action: "execute", Effect: "allow"},
			{Resource: "brain:browser", Action: "*", Effect: "deny"},
			{Resource: "tool:*", Action: "execute", Effect: "allow"},
		},
	}

	tests := []struct {
		resource string
		action   string
		want     bool
	}{
		{"brain:code", "execute", true},
		{"brain:code", "configure", false},
		{"brain:browser", "execute", false},
		{"brain:browser", "view", false},
		{"tool:file.read", "execute", true},
		{"tool:file.write", "execute", true},
		{"brain:data", "execute", false},
	}
	for _, tt := range tests {
		got := pm.IsAllowed(tt.resource, tt.action)
		if got != tt.want {
			t.Errorf("IsAllowed(%q, %q) = %v, want %v", tt.resource, tt.action, got, tt.want)
		}
	}
}

func TestPermissionMatrixNil(t *testing.T) {
	var pm *PermissionMatrix
	if !pm.IsAllowed("brain:code", "execute") {
		t.Error("nil matrix should allow everything")
	}
}

func TestRevocationStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revocation.json")
	store := NewFileRevocationStore(path)
	ctx := context.Background()

	// 初始加载（文件不存在）
	crl, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if crl == nil {
		t.Fatal("Load returned nil")
	}

	// 未吊销
	revoked, _ := store.IsRevoked(ctx, "lic-001")
	if revoked {
		t.Error("should not be revoked")
	}

	// 添加吊销
	err = store.Update(ctx, &RevocationList{
		Version:    1,
		RevokedIDs: []string{"lic-001", "lic-002"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	revoked, _ = store.IsRevoked(ctx, "lic-001")
	if !revoked {
		t.Error("lic-001 should be revoked")
	}
	revoked, _ = store.IsRevoked(ctx, "lic-003")
	if revoked {
		t.Error("lic-003 should not be revoked")
	}

	// 持久化验证
	store2 := NewFileRevocationStore(path)
	store2.Load(ctx)
	revoked, _ = store2.IsRevoked(ctx, "lic-002")
	if !revoked {
		t.Error("lic-002 should be revoked after reload")
	}
}

func TestEnterpriseEnforcerIntegration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// 写组织策略
	policyPath := filepath.Join(dir, "org-policy.json")
	policy := OrgPolicy{
		OrgID:         "test-org",
		AllowedBrains: []string{"code", "central"},
		BlockedBrains: []string{"browser"},
	}
	data, _ := json.Marshal(policy)
	os.WriteFile(policyPath, data, 0o600)

	// 写权限矩阵
	permPath := filepath.Join(dir, "permissions.json")
	pm := PermissionMatrix{
		Version: 1,
		OrgID:   "test-org",
		Permissions: []PermissionEntry{
			{Resource: "brain:code", Action: "execute", Effect: "allow"},
			{Resource: "brain:central", Action: "*", Effect: "allow"},
		},
	}
	data, _ = json.Marshal(pm)
	os.WriteFile(permPath, data, 0o600)

	ee, err := NewEnterpriseEnforcer(EnterpriseConfig{
		OrgPolicyPath:  policyPath,
		PermissionPath: permPath,
		RevocationPath: filepath.Join(dir, "revocation.json"),
		LicenseID:      "lic-active",
	})
	if err != nil {
		t.Fatalf("NewEnterpriseEnforcer: %v", err)
	}

	// code execute → 允许
	err = ee.Check(ctx, OrgAction{Type: "execute", BrainKind: "code"})
	if err != nil {
		t.Errorf("code execute should be allowed: %v", err)
	}

	// browser → 被组织策略黑名单拒绝
	err = ee.Check(ctx, OrgAction{Type: "execute", BrainKind: "browser"})
	if err == nil {
		t.Error("browser should be blocked by org policy")
	}

	// data → 不在白名单中
	err = ee.Check(ctx, OrgAction{Type: "execute", BrainKind: "data"})
	if err == nil {
		t.Error("data should not be in allowed list")
	}

	// 吊销 license 后检查
	ee.UpdateRevocations(ctx, &RevocationList{
		Version:    1,
		RevokedIDs: []string{"lic-active"},
	})
	err = ee.Check(ctx, OrgAction{Type: "execute", BrainKind: "code"})
	if err == nil {
		t.Error("should fail after license revocation")
	}
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(3, 2, 50*time.Millisecond)

	// 初始允许
	if !cb.Allow() {
		t.Fatal("should allow initially")
	}

	// 连续失败触发熔断
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.Allow() {
		t.Fatal("should be open after 3 failures")
	}

	// 等超时进入半开
	time.Sleep(60 * time.Millisecond)
	if !cb.Allow() {
		t.Fatal("should allow in half-open")
	}

	// 半开状态下成功恢复
	cb.RecordSuccess()
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Errorf("state = %d, want closed", cb.State())
	}
}
