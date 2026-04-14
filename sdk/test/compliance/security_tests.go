package compliance

import (
	"context"
	"fmt"

	"github.com/leef-l/brain/sdk/agent"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/security"
	braintesting "github.com/leef-l/brain/sdk/testing"
)

func registerSecurityTests(r *braintesting.MemComplianceRunner) {
	// C-S-01: MemVault Put/Get round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-01", Description: "Vault Put/Get round-trip", Category: "security",
	}, func(ctx context.Context) error {
		vault := security.NewMemVault()
		if err := vault.Put(ctx, "test-key", "test-value"); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-01: Put: %v", err)))
		}
		val, err := vault.Get(ctx, "test-key")
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-01: Get: %v", err)))
		}
		if val != "test-value" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-01: value mismatch"))
		}
		return nil
	})

	// C-S-02: Vault Get non-existent key returns error.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-02", Description: "Vault Get non-existent key → error", Category: "security",
	}, func(ctx context.Context) error {
		vault := security.NewMemVault()
		_, err := vault.Get(ctx, "no-such-key")
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-02: expected error for missing key"))
		}
		return nil
	})

	// C-S-03: Vault Delete removes key.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-03", Description: "Vault Delete removes key", Category: "security",
	}, func(ctx context.Context) error {
		vault := security.NewMemVault()
		vault.Put(ctx, "del-key", "val")
		if err := vault.Delete(ctx, "del-key"); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-03: Delete: %v", err)))
		}
		_, err := vault.Get(ctx, "del-key")
		if err == nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-03: key should be gone"))
		}
		return nil
	})

	// C-S-04: AuditLogger emits events with chain integrity.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-04", Description: "AuditLogger chain integrity", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		ev := &security.AuditEvent{
			Actor:    "test",
			Action:   "test.action",
			Resource: "test-resource",
		}
		if err := logger.Emit(ctx, ev); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-04: Emit: %v", err)))
		}
		if ev.SelfHash == "" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-04: SelfHash empty"))
		}
		if err := logger.Verify(); err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-04: Verify: %v", err)))
		}
		return nil
	})

	// C-S-05: AuditLogger chain — two events link.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-05", Description: "AuditLogger chain linkage", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		ev1 := &security.AuditEvent{Actor: "a", Action: "first", Resource: "r"}
		ev2 := &security.AuditEvent{Actor: "b", Action: "second", Resource: "r"}
		logger.Emit(ctx, ev1)
		logger.Emit(ctx, ev2)
		if ev2.PrevHash != ev1.SelfHash {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-05: PrevHash should equal previous SelfHash"))
		}
		return nil
	})

	// C-S-06: Vault with AuditLogger generates audit events.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-06", Description: "Vault+AuditLogger generates events", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		vault := security.NewMemVault(security.WithMemVaultAuditor(logger))
		vault.Put(ctx, "k1", "v1")
		vault.Get(ctx, "k1")
		events := logger.Snapshot()
		if len(events) < 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-06: expected ≥2 audit events, got %d", len(events))))
		}
		return nil
	})

	// C-S-07: ProxiedLLMAccess mode is "proxied".
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-07", Description: "ProxiedLLMAccess mode", Category: "security",
	}, func(ctx context.Context) error {
		p := security.NewProxiedLLMAccess()
		if p.Mode() != "proxied" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-07: mode not proxied"))
		}
		return nil
	})

	// C-S-08: ProxiedLLMAccess returns empty credentials.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-08", Description: "ProxiedLLMAccess returns empty credentials", Category: "security",
	}, func(ctx context.Context) error {
		p := security.NewProxiedLLMAccess()
		creds, err := p.Credentials(ctx, "anthropic")
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-08: Credentials: %v", err)))
		}
		if len(creds) != 0 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-08: proxied should return empty credentials"))
		}
		return nil
	})

	// C-S-09: LLMAccessMode constants defined.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-09", Description: "LLMAccessMode constants", Category: "security",
	}, func(ctx context.Context) error {
		if agent.LLMAccessProxied != "proxied" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-09: LLMAccessProxied wrong"))
		}
		if agent.LLMAccessDirect != "direct" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-09: LLMAccessDirect wrong"))
		}
		if agent.LLMAccessHybrid != "hybrid" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-09: LLMAccessHybrid wrong"))
		}
		return nil
	})

	// C-S-10: Sandbox interface — FSPolicy struct.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-10", Description: "Sandbox FSPolicy fields", Category: "security",
	}, func(ctx context.Context) error {
		fs := security.FSPolicy{
			ReadAllowed:  []string{"/tmp"},
			WriteAllowed: []string{"/tmp"},
			Denied:       []string{"/etc/shadow"},
		}
		if len(fs.Denied) != 1 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-10: FSPolicy.Denied wrong"))
		}
		return nil
	})

	// C-S-11: NetPolicy struct.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-11", Description: "NetPolicy fields", Category: "security",
	}, func(ctx context.Context) error {
		net := security.NetPolicy{
			AllowedHosts: []string{"api.anthropic.com"},
			DeniedHosts:  []string{"evil.com"},
			AllowedPorts: []int{443},
		}
		if len(net.AllowedPorts) != 1 || net.AllowedPorts[0] != 443 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-11: NetPolicy wrong"))
		}
		return nil
	})

	// C-S-12: ProcPolicy struct.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-12", Description: "ProcPolicy fields", Category: "security",
	}, func(ctx context.Context) error {
		proc := security.ProcPolicy{
			MaxProcs:   10,
			AllowedExe: []string{"node", "python3"},
		}
		if proc.MaxProcs != 10 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-12: ProcPolicy wrong"))
		}
		return nil
	})

	// C-S-13: SysPolicy struct.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-13", Description: "SysPolicy fields", Category: "security",
	}, func(ctx context.Context) error {
		sys := security.SysPolicy{
			MaxMemoryMB: 512,
		}
		if sys.MaxMemoryMB != 512 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-13: SysPolicy wrong"))
		}
		return nil
	})

	// C-S-14: SandboxChecker rejects nil sandbox.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-14", Description: "SandboxChecker rejects nil sandbox", Category: "security",
	}, func(ctx context.Context) error {
		// NewSandboxChecker(nil) panics — verify it panics.
		didPanic := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					didPanic = true
				}
			}()
			security.NewSandboxChecker(nil)
		}()
		if !didPanic {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-14: should panic on nil sandbox"))
		}
		return nil
	})

	// C-S-15: AuditLogger Snapshot returns copies.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-15", Description: "AuditLogger Snapshot returns copies", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		logger.Emit(ctx, &security.AuditEvent{Actor: "a", Action: "act", Resource: "r"})
		snap1 := logger.Snapshot()
		snap2 := logger.Snapshot()
		if len(snap1) != len(snap2) {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-15: snapshots differ"))
		}
		return nil
	})

	// C-S-16: AuditLogger Tail returns last hash.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-16", Description: "AuditLogger Tail", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		ev := &security.AuditEvent{Actor: "a", Action: "act", Resource: "r"}
		logger.Emit(ctx, ev)
		tail := logger.Tail()
		if tail != ev.SelfHash {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-16: Tail != last SelfHash"))
		}
		return nil
	})

	// C-S-17: AuditEvent SelfHash is non-empty after Emit.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-17", Description: "AuditEvent SelfHash non-empty", Category: "security",
	}, func(ctx context.Context) error {
		logger := security.NewHashChainAuditLogger()
		ev := &security.AuditEvent{Actor: "a", Action: "act", Resource: "r"}
		logger.Emit(ctx, ev)
		if ev.SelfHash == "" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-17: SelfHash empty"))
		}
		return nil
	})

	// C-S-18: Agent Kind constants.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-18", Description: "Agent Kind constants", Category: "security",
	}, func(ctx context.Context) error {
		if agent.KindCentral != "central" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-18: KindCentral wrong"))
		}
		if agent.KindCode != "code" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-18: KindCode wrong"))
		}
		if agent.KindVerifier != "verifier" {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-18: KindVerifier wrong"))
		}
		return nil
	})

	// C-S-19: Agent Descriptor struct fields.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-19", Description: "Agent Descriptor struct", Category: "security",
	}, func(ctx context.Context) error {
		desc := agent.Descriptor{
			Kind:           agent.KindCode,
			Version:        "1.0.0",
			LLMAccess:      agent.LLMAccessProxied,
			SupportedTools: []string{"code.read", "code.write"},
			Capabilities:   map[string]bool{"streaming": true},
		}
		if desc.Kind != agent.KindCode {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("C-S-19: Kind wrong"))
		}
		return nil
	})

	// C-S-20: Vault Len counts entries.
	r.Register(braintesting.ComplianceTest{
		ID: "C-S-20", Description: "Vault Len counts entries", Category: "security",
	}, func(ctx context.Context) error {
		vault := security.NewMemVault()
		vault.Put(ctx, "a", "1")
		vault.Put(ctx, "b", "2")
		if vault.Len() != 2 {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage(fmt.Sprintf("C-S-20: Len=%d, want 2", vault.Len())))
		}
		return nil
	})
}
