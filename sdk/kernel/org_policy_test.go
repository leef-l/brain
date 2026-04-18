package kernel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOrgPolicyCheck_NoPolicyAllowsAll(t *testing.T) {
	e := NewFileOrgPolicyEnforcer()
	err := e.Check(context.Background(), OrgAction{
		Type:      "start_run",
		BrainKind: "code",
		UserID:    "user1",
	})
	if err != nil {
		t.Fatalf("expected nil error when no policy loaded, got: %v", err)
	}
}

func TestOrgPolicyCheck_AllowedBrains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org-policy.json")
	data := `{
		"org_id": "acme-corp",
		"allowed_brains": ["central", "code", "verifier"],
		"max_concurrent": 10,
		"audit_level": "full"
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	if err := e.LoadPolicy(path); err != nil {
		t.Fatal(err)
	}

	// 允许的 brain 应该通过
	for _, kind := range []string{"central", "code", "verifier"} {
		err := e.Check(context.Background(), OrgAction{Type: "start_run", BrainKind: kind})
		if err != nil {
			t.Errorf("expected brain %q to be allowed, got: %v", kind, err)
		}
	}

	// 不在白名单中的 brain 应该被拒绝
	err := e.Check(context.Background(), OrgAction{Type: "start_run", BrainKind: "quant"})
	if err == nil {
		t.Error("expected error for brain 'quant' not in allowed list")
	}
}

func TestOrgPolicyCheck_BlockedBrains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org-policy.json")
	data := `{
		"org_id": "acme-corp",
		"blocked_brains": ["quant", "data"],
		"audit_level": "basic"
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	if err := e.LoadPolicy(path); err != nil {
		t.Fatal(err)
	}

	// 被黑名单阻止的 brain
	err := e.Check(context.Background(), OrgAction{Type: "install_brain", BrainKind: "quant"})
	if err == nil {
		t.Error("expected error for blocked brain 'quant'")
	}

	// 未被阻止的 brain
	err = e.Check(context.Background(), OrgAction{Type: "start_run", BrainKind: "code"})
	if err != nil {
		t.Errorf("expected brain 'code' to pass, got: %v", err)
	}
}

func TestOrgPolicyCheck_WildcardAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org-policy.json")
	data := `{
		"org_id": "open-corp",
		"allowed_brains": ["*"]
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	if err := e.LoadPolicy(path); err != nil {
		t.Fatal(err)
	}

	err := e.Check(context.Background(), OrgAction{Type: "start_run", BrainKind: "anything"})
	if err != nil {
		t.Errorf("wildcard should allow any brain, got: %v", err)
	}
}

func TestOrgPolicyLoadPolicy_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	err := e.LoadPolicy(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestOrgPolicyLoadPolicy_FileNotFound(t *testing.T) {
	e := NewFileOrgPolicyEnforcer()
	err := e.LoadPolicy("/nonexistent/path/org-policy.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestOrgPolicyPolicy_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org-policy.json")
	data := `{
		"org_id": "test",
		"allowed_brains": ["code"],
		"blocked_brains": ["quant"],
		"require_approval": ["dangerous"]
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	if err := e.LoadPolicy(path); err != nil {
		t.Fatal(err)
	}

	p := e.Policy()
	if p == nil {
		t.Fatal("expected non-nil policy")
	}
	if p.OrgID != "test" {
		t.Errorf("OrgID = %q, want %q", p.OrgID, "test")
	}

	// 修改副本不影响原始
	p.AllowedBrains = append(p.AllowedBrains, "hacked")
	original := e.Policy()
	if len(original.AllowedBrains) != 1 {
		t.Error("modifying Policy() return value should not affect original")
	}
}

func TestOrgPolicyCheck_EmptyBrainKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "org-policy.json")
	data := `{
		"org_id": "strict-corp",
		"allowed_brains": ["code"],
		"blocked_brains": ["quant"]
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewFileOrgPolicyEnforcer()
	if err := e.LoadPolicy(path); err != nil {
		t.Fatal(err)
	}

	// 空 BrainKind 不触发白/黑名单检查
	err := e.Check(context.Background(), OrgAction{Type: "tool_call", BrainKind: ""})
	if err != nil {
		t.Errorf("empty BrainKind should skip brain checks, got: %v", err)
	}
}
