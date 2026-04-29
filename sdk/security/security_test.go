package security

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// ---------- MemVault ----------

func TestMemVault_PutGetRoundTrip(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	if err := v.Put(ctx, "brain_id/openai_key", "sk-xxx"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := v.Get(ctx, "brain_id/openai_key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "sk-xxx" {
		t.Fatalf("got %q, want sk-xxx", got)
	}
}

func TestMemVault_GetMissing(t *testing.T) {
	v := NewMemVault()
	_, err := v.Get(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("want BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeRecordNotFound {
		t.Fatalf("want %s, got %s", brainerrors.CodeRecordNotFound, be.ErrorCode)
	}
}

func TestMemVault_InvalidKey(t *testing.T) {
	v := NewMemVault()
	cases := []string{"", " leading", "trailing ", "has\nnewline", "has\rcr"}
	for _, k := range cases {
		if err := v.Put(context.Background(), k, "v"); err == nil {
			t.Errorf("put(%q) expected error", k)
		}
	}
}

func TestMemVault_TTLExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	v := NewMemVault(WithMemVaultClock(func() time.Time { return now }))
	if err := v.PutWithTTL(context.Background(), "k", "v", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(context.Background(), "k"); err != nil {
		t.Fatalf("expected hit before expiry: %v", err)
	}
	now = now.Add(11 * time.Second)
	_, err := v.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("expected expiry miss")
	}
	if v.Len() != 0 {
		t.Fatalf("expired entry should have been swept, got len=%d", v.Len())
	}
}

func TestMemVault_Delete(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "k", "v")
	if err := v.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, "k"); err == nil {
		t.Fatal("expected miss after delete")
	}
	// Deleting a missing key is idempotent.
	if err := v.Delete(ctx, "k"); err != nil {
		t.Fatalf("delete on missing should be idempotent, got %v", err)
	}
}

func TestMemVault_ConcurrentAccess(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "k" + string(rune('0'+i%10))
			_ = v.Put(ctx, key, "v")
			_, _ = v.Get(ctx, key)
		}(i)
	}
	wg.Wait()
}

func TestMemVault_AuditorRecordsEvents(t *testing.T) {
	audit := NewHashChainAuditLogger()
	v := NewMemVault(WithMemVaultAuditor(audit))
	ctx := context.Background()
	_ = v.Put(ctx, "k", "v")
	_, _ = v.Get(ctx, "k")
	_, _ = v.Get(ctx, "missing")
	_ = v.Delete(ctx, "k")
	snap := audit.Snapshot()
	if len(snap) != 4 {
		t.Fatalf("want 4 audit events, got %d", len(snap))
	}
	for _, ev := range snap {
		for _, v := range ev.Payload {
			if s, ok := v.(string); ok && strings.Contains(s, "sk-") {
				t.Fatalf("raw secret leaked into audit payload: %v", ev.Payload)
			}
		}
	}
	if err := audit.Verify(); err != nil {
		t.Fatalf("audit chain verify: %v", err)
	}
}

// ---------- SandboxChecker ----------

type staticSandbox struct {
	fs  FSPolicy
	net NetPolicy
	pr  ProcPolicy
	sys SysPolicy
}

func (s *staticSandbox) FS() FSPolicy    { return s.fs }
func (s *staticSandbox) Net() NetPolicy  { return s.net }
func (s *staticSandbox) Proc() ProcPolicy { return s.pr }
func (s *staticSandbox) Sys() SysPolicy  { return s.sys }

func newTestSandbox() *staticSandbox {
	return &staticSandbox{
		fs: FSPolicy{
			ReadAllowed:  []string{filepath.FromSlash("/workspace/")},
			WriteAllowed: []string{filepath.FromSlash("/workspace/out/")},
			Denied:       []string{filepath.FromSlash("/workspace/out/secrets/")},
		},
		net: NetPolicy{
			AllowedHosts: []string{"api.anthropic.com", "api.openai.com"},
			DeniedHosts:  []string{"169.254.169.254"},
			AllowedPorts: []int{443},
		},
		pr: ProcPolicy{
			MaxProcs:   4,
			AllowedExe: []string{"git", "go"},
		},
		sys: SysPolicy{
			MaxMemoryMB: 1024,
			MaxCPUTime:  10 * time.Second,
		},
	}
}

func TestSandboxChecker_FS(t *testing.T) {
	c := NewSandboxChecker(newTestSandbox())
	if err := c.CheckRead(filepath.FromSlash("/workspace/src/main.go")); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckWrite(filepath.FromSlash("/workspace/out/artifact.bin")); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckRead(filepath.FromSlash("/etc/passwd")); err == nil {
		t.Fatal("expected deny")
	}
	if err := c.CheckWrite(filepath.FromSlash("/workspace/out/secrets/key.pem")); err == nil {
		t.Fatal("expected deny (denied list wins)")
	}
}

func TestSandboxChecker_Net(t *testing.T) {
	c := NewSandboxChecker(newTestSandbox())
	if err := c.CheckDial("api.anthropic.com", 443); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckDial("169.254.169.254", 80); err == nil {
		t.Fatal("expected deny (denied host)")
	}
	if err := c.CheckDial("evil.example.com", 443); err == nil {
		t.Fatal("expected deny (not on allow list)")
	}
	if err := c.CheckDial("api.openai.com", 8080); err == nil {
		t.Fatal("expected deny (port not allowed)")
	}
}

func TestSandboxChecker_Proc(t *testing.T) {
	c := NewSandboxChecker(newTestSandbox())
	if err := c.CheckExec("git"); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckExec("rm"); err == nil {
		t.Fatal("expected deny")
	}
	if err := c.CheckExec(""); err == nil {
		t.Fatal("expected deny (empty)")
	}
	if err := c.CheckProcCount(4); err == nil {
		t.Fatal("expected deny at max procs")
	}
	if err := c.CheckProcCount(3); err != nil {
		t.Fatalf("expected allow under max procs, got %v", err)
	}
}

func TestSandboxChecker_Sys(t *testing.T) {
	c := NewSandboxChecker(newTestSandbox())
	if err := c.CheckMemory(512); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckMemory(2048); err == nil {
		t.Fatal("expected deny")
	}
	if err := c.CheckCPU(5 * time.Second); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
	if err := c.CheckCPU(15 * time.Second); err == nil {
		t.Fatal("expected deny")
	}
}

func TestSandboxChecker_ErrorCode(t *testing.T) {
	c := NewSandboxChecker(newTestSandbox())
	err := c.CheckRead("/etc/passwd")
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("want BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeToolSandboxDenied {
		t.Fatalf("want %s, got %s", brainerrors.CodeToolSandboxDenied, be.ErrorCode)
	}
}

// ---------- Vault Rotate / List ----------

func TestMemVault_Rotate(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "k", "old")

	if err := v.Rotate(ctx, "k", "new"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	got, err := v.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get after Rotate: %v", err)
	}
	if got != "new" {
		t.Errorf("value = %q, want %q", got, "new")
	}
}

func TestMemVault_RotateMissing(t *testing.T) {
	v := NewMemVault()
	err := v.Rotate(context.Background(), "missing", "new")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("want BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeRecordNotFound {
		t.Fatalf("want %s, got %s", brainerrors.CodeRecordNotFound, be.ErrorCode)
	}
}

func TestMemVault_RotateExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	v := NewMemVault(WithMemVaultClock(func() time.Time { return now }))
	_ = v.PutWithTTL(context.Background(), "k", "v", 5*time.Second)
	now = now.Add(10 * time.Second)

	err := v.Rotate(context.Background(), "k", "new")
	if err == nil {
		t.Fatal("expected error for expired key")
	}
}

func TestMemVault_RotatePreservesTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	v := NewMemVault(WithMemVaultClock(func() time.Time { return now }))
	_ = v.PutWithTTL(context.Background(), "k", "old", 60*time.Second)

	if err := v.Rotate(context.Background(), "k", "new"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Should still be accessible before TTL.
	got, err := v.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "new" {
		t.Errorf("value = %q, want %q", got, "new")
	}

	// Should expire at original TTL.
	now = now.Add(61 * time.Second)
	_, err = v.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("expected expired after original TTL")
	}
}

func TestMemVault_List(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "llm/anthropic/api_key", "sk-1")
	_ = v.Put(ctx, "llm/openai/api_key", "sk-2")
	_ = v.Put(ctx, "other/key", "v")

	keys, err := v.List(ctx, "llm/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List(llm/) = %d, want 2", len(keys))
	}

	all, err := v.List(ctx, "")
	if err != nil {
		t.Fatalf("List(''): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List('') = %d, want 3", len(all))
	}
}

func TestMemVault_ListSkipsExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	v := NewMemVault(WithMemVaultClock(func() time.Time { return now }))
	_ = v.PutWithTTL(context.Background(), "fresh", "v", 60*time.Second)
	_ = v.PutWithTTL(context.Background(), "stale", "v", 5*time.Second)
	now = now.Add(10 * time.Second)

	keys, err := v.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("List = %d, want 1 (stale should be skipped)", len(keys))
	}
	if keys[0] != "fresh" {
		t.Errorf("key = %q, want %q", keys[0], "fresh")
	}
}

func TestMemVault_RotateAudit(t *testing.T) {
	audit := NewHashChainAuditLogger()
	v := NewMemVault(WithMemVaultAuditor(audit))
	ctx := context.Background()
	_ = v.Put(ctx, "k", "v")
	_ = v.Rotate(ctx, "k", "new")
	_, _ = v.List(ctx, "")

	snap := audit.Snapshot()
	// Put + Rotate + List = 3 events.
	if len(snap) != 3 {
		t.Fatalf("want 3 audit events, got %d", len(snap))
	}
	if snap[1].Action != "vault_rotate" {
		t.Errorf("event[1].Action = %q, want vault_rotate", snap[1].Action)
	}
	if snap[2].Action != "vault_list" {
		t.Errorf("event[2].Action = %q, want vault_list", snap[2].Action)
	}
}

// ---------- ProxiedLLMAccess ----------

func TestProxiedLLMAccess(t *testing.T) {
	s := NewProxiedLLMAccess()
	if s.Mode() != "proxied" {
		t.Fatalf("want proxied, got %q", s.Mode())
	}
	creds, err := s.Credentials(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("proxied must return empty credentials, got %v", creds)
	}
}

// ---------- DirectLLMAccess ----------

func TestDirectLLMAccess_Mode(t *testing.T) {
	d := NewDirectLLMAccess(nil, nil)
	if d.Mode() != "direct" {
		t.Fatalf("want direct, got %q", d.Mode())
	}
}

func TestDirectLLMAccess_Credentials(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "llm/anthropic/api_key", "sk-test-123")

	audit := NewHashChainAuditLogger()
	d := NewDirectLLMAccess(v, audit)

	creds, err := d.Credentials(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds["api_key"] != "sk-test-123" {
		t.Errorf("api_key = %q, want sk-test-123", creds["api_key"])
	}

	// Verify audit event was emitted.
	snap := audit.Snapshot()
	found := false
	for _, ev := range snap {
		if ev.Action == "llm_credential_issued" {
			found = true
		}
	}
	if !found {
		t.Error("expected llm_credential_issued audit event")
	}
}

func TestDirectLLMAccess_MissingCredential(t *testing.T) {
	v := NewMemVault()
	d := NewDirectLLMAccess(v, nil)
	_, err := d.Credentials(context.Background(), "openai")
	if err == nil {
		t.Fatal("expected error for missing credential")
	}
}

func TestDirectLLMAccess_NilVault(t *testing.T) {
	d := NewDirectLLMAccess(nil, nil)
	_, err := d.Credentials(context.Background(), "anthropic")
	if err == nil {
		t.Fatal("expected error for nil vault")
	}
}

// ---------- HybridLLMAccess ----------

func TestHybridLLMAccess_Mode(t *testing.T) {
	h := NewHybridLLMAccess(nil, nil)
	if h.Mode() != "hybrid" {
		t.Fatalf("want hybrid, got %q", h.Mode())
	}
}

func TestHybridLLMAccess_WithCredentials(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "llm/anthropic/api_key", "sk-hybrid-123")

	h := NewHybridLLMAccess(v, nil)
	creds, err := h.Credentials(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds["api_key"] != "sk-hybrid-123" {
		t.Errorf("api_key = %q, want sk-hybrid-123", creds["api_key"])
	}
}

func TestHybridLLMAccess_FallbackToProxied(t *testing.T) {
	v := NewMemVault()
	h := NewHybridLLMAccess(v, nil)
	creds, err := h.Credentials(context.Background(), "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected empty creds (proxied fallback), got %v", creds)
	}
}

func TestHybridLLMAccess_ProviderWhitelist(t *testing.T) {
	v := NewMemVault()
	ctx := context.Background()
	_ = v.Put(ctx, "llm/anthropic/api_key", "sk-1")
	_ = v.Put(ctx, "llm/openai/api_key", "sk-2")

	audit := NewHashChainAuditLogger()
	h := NewHybridLLMAccess(v, audit, WithAllowedProviders("anthropic"))

	// Anthropic is allowed — should get credentials.
	creds, err := h.Credentials(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Credentials(anthropic): %v", err)
	}
	if creds["api_key"] != "sk-1" {
		t.Errorf("api_key = %q, want sk-1", creds["api_key"])
	}

	// OpenAI is not in whitelist — should fall back to proxied.
	creds, err = h.Credentials(ctx, "openai")
	if err != nil {
		t.Fatalf("Credentials(openai): %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected empty creds for non-allowed provider, got %v", creds)
	}
}

func TestHybridLLMAccess_IsProxiedFallback(t *testing.T) {
	v := NewMemVault()
	h := NewHybridLLMAccess(v, nil)

	if !h.IsProxiedFallback(context.Background(), "openai") {
		t.Error("expected proxied fallback for missing credential")
	}

	_ = v.Put(context.Background(), "llm/openai/api_key", "sk-1")
	if h.IsProxiedFallback(context.Background(), "openai") {
		t.Error("expected direct access when credential exists")
	}
}

// ---------- SandboxEnforcer ----------

func TestSandboxEnforcer_L0(t *testing.T) {
	e := NewSandboxEnforcer(newTestSandbox(), SandboxL0)
	if e.Level() != SandboxL0 {
		t.Errorf("Level = %v, want L0", e.Level())
	}
	if err := e.ValidateLevel(); err != nil {
		t.Fatalf("ValidateLevel L0: %v", err)
	}
	if e.Checker() == nil {
		t.Error("Checker is nil")
	}
	// Policy checks still work through the enforcer.
	if err := e.Checker().CheckRead(filepath.FromSlash("/workspace/src/main.go")); err != nil {
		t.Fatalf("CheckRead: %v", err)
	}
}

func TestSandboxEnforcer_L1(t *testing.T) {
	e := NewSandboxEnforcer(newTestSandbox(), SandboxL1)
	if err := e.ValidateLevel(); err != nil {
		t.Fatalf("ValidateLevel L1: %v", err)
	}
}

func TestSandboxEnforcer_L2Available(t *testing.T) {
	e := NewSandboxEnforcer(newTestSandbox(), SandboxL2)
	if err := e.ValidateLevel(); err != nil {
		t.Fatalf("expected L2 to be available, got: %v", err)
	}
}

func TestSandboxEnforcer_L3NotImplemented(t *testing.T) {
	e := NewSandboxEnforcer(newTestSandbox(), SandboxL3)
	if err := e.ValidateLevel(); err == nil {
		t.Fatal("expected error for L3")
	}
}

func TestSandboxLevel_String(t *testing.T) {
	cases := []struct {
		level SandboxLevel
		want  string
	}{
		{SandboxL0, "L0-none"},
		{SandboxL1, "L1-seccomp"},
		{SandboxL2, "L2-container"},
		{SandboxL3, "L3-vm"},
		{SandboxLevel(99), "unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.level.String(); got != tc.want {
			t.Errorf("SandboxLevel(%d).String() = %q, want %q", tc.level, got, tc.want)
		}
	}
}

// ---------- HashChainAuditLogger ----------

func TestHashChainAudit_EmitAndVerify(t *testing.T) {
	l := NewHashChainAuditLogger()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		ev := &AuditEvent{
			EventID:   "e" + string(rune('0'+i)),
			Actor:     "system",
			Action:    "test",
			Resource:  "run/1",
			Timestamp: time.Unix(int64(1000+i), 0).UTC(),
			Payload:   map[string]interface{}{"i": i},
		}
		if err := l.Emit(ctx, ev); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	snap := l.Snapshot()
	if len(snap) != 10 {
		t.Fatalf("want 10 events, got %d", len(snap))
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Genesis row has empty PrevHash.
	if snap[0].PrevHash != "" {
		t.Fatalf("genesis PrevHash must be empty, got %q", snap[0].PrevHash)
	}
	// Each subsequent row's PrevHash == previous SelfHash.
	for i := 1; i < len(snap); i++ {
		if snap[i].PrevHash != snap[i-1].SelfHash {
			t.Fatalf("chain break at %d: prev=%q self[%d]=%q",
				i, snap[i].PrevHash, i-1, snap[i-1].SelfHash)
		}
	}
}

func TestHashChainAudit_MismatchDetected(t *testing.T) {
	l := NewHashChainAuditLogger()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = l.Emit(ctx, &AuditEvent{
			EventID:   "e" + string(rune('0'+i)),
			Actor:     "system",
			Action:    "test",
			Timestamp: time.Unix(int64(1000+i), 0).UTC(),
			Payload:   map[string]interface{}{"i": i},
		})
	}
	// Tamper with internal state: flip a payload value.
	l.events[1].Payload["i"] = 999
	if err := l.Verify(); err == nil {
		t.Fatal("expected verify to detect tampering")
	}
}

func TestHashChainAudit_PinnedPrevHash(t *testing.T) {
	l := NewHashChainAuditLogger()
	ctx := context.Background()
	_ = l.Emit(ctx, &AuditEvent{
		EventID:   "e0",
		Action:    "test",
		Timestamp: time.Unix(1000, 0).UTC(),
	})
	// Caller pins the wrong tail → chain_mismatch.
	err := l.Emit(ctx, &AuditEvent{
		EventID:   "e1",
		Action:    "test",
		Timestamp: time.Unix(1001, 0).UTC(),
		PrevHash:  "deadbeef",
	})
	if err == nil {
		t.Fatal("expected chain_mismatch error")
	}
	var be *brainerrors.BrainError
	if !errors.As(err, &be) {
		t.Fatalf("want BrainError, got %T", err)
	}
	if be.ErrorCode != brainerrors.CodeInvariantViolated {
		t.Fatalf("want %s, got %s", brainerrors.CodeInvariantViolated, be.ErrorCode)
	}
}

func TestHashChainAudit_DeepCopyOnStore(t *testing.T) {
	l := NewHashChainAuditLogger()
	ctx := context.Background()
	ev := &AuditEvent{
		EventID:   "e0",
		Action:    "test",
		Timestamp: time.Unix(1000, 0).UTC(),
		Payload:   map[string]interface{}{"k": "v"},
	}
	_ = l.Emit(ctx, ev)
	// Mutate caller's pointer after Emit.
	ev.Payload["k"] = "tampered"
	if err := l.Verify(); err != nil {
		t.Fatalf("mutation of caller event after Emit must not affect chain: %v", err)
	}
}
