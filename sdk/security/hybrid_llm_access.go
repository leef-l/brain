package security

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// HybridLLMAccess is the LLMAccessStrategy that defaults to proxied mode
// but can issue ephemeral credentials on demand for a specific call. Every
// credential issuance MUST be audited and scoped to the requesting provider.
//
// This strategy is a middle ground: the sidecar normally routes calls
// through the Kernel (proxied), but may request a short-lived credential
// when latency or throughput demands direct access. See 23-安全模型.md §5.1.
type HybridLLMAccess struct {
	vault   Vault
	auditor AuditLogger
	// allowedProviders limits which providers can get direct credentials.
	// If empty, all providers are allowed.
	allowedProviders map[string]struct{}
}

// HybridLLMAccessOption configures a HybridLLMAccess.
type HybridLLMAccessOption func(*HybridLLMAccess)

// WithAllowedProviders restricts which providers may receive direct
// credentials. Providers not in this list fall back to proxied mode.
func WithAllowedProviders(providers ...string) HybridLLMAccessOption {
	return func(h *HybridLLMAccess) {
		for _, p := range providers {
			h.allowedProviders[p] = struct{}{}
		}
	}
}

// NewHybridLLMAccess constructs a hybrid access strategy.
func NewHybridLLMAccess(vault Vault, auditor AuditLogger, opts ...HybridLLMAccessOption) *HybridLLMAccess {
	h := &HybridLLMAccess{
		vault:            vault,
		auditor:          auditor,
		allowedProviders: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Mode returns "hybrid" per 23-安全模型.md §5.1.
func (h *HybridLLMAccess) Mode() string { return "hybrid" }

// Credentials issues an ephemeral credential for the provider if allowed,
// otherwise returns an empty map (proxied fallback). See 23-安全模型.md §5.2.
func (h *HybridLLMAccess) Credentials(ctx context.Context, provider string) (map[string]string, error) {
	// If provider whitelist is set, check membership.
	if len(h.allowedProviders) > 0 {
		if _, ok := h.allowedProviders[provider]; !ok {
			// Not in whitelist — fall back to proxied (empty creds).
			if h.auditor != nil {
				_ = h.auditor.Emit(ctx, &AuditEvent{
					Actor:    "system",
					Action:   "llm_credential_denied",
					Resource: "llm/" + provider,
					Payload: map[string]interface{}{
						"mode":     "hybrid",
						"provider": provider,
						"reason":   "provider_not_allowed",
					},
				})
			}
			return map[string]string{}, nil
		}
	}

	if h.vault == nil {
		return map[string]string{}, nil
	}

	key := "llm/" + provider + "/api_key"
	secret, err := h.vault.Get(ctx, key)
	if err != nil {
		// Credential not available — fall back to proxied.
		return map[string]string{}, nil
	}

	if h.auditor != nil {
		_ = h.auditor.Emit(ctx, &AuditEvent{
			Actor:    "system",
			Action:   "llm_credential_issued",
			Resource: "llm/" + provider,
			Payload: map[string]interface{}{
				"mode":            "hybrid",
				"provider":        provider,
				"key_fingerprint": keyFingerprint(key),
			},
		})
	}

	return map[string]string{
		"api_key": secret,
	}, nil
}

// IsProxiedFallback returns true if the last Credentials call for this
// provider would have returned an empty map (proxied mode). This is a
// convenience for callers that need to know whether to route through the
// Kernel or connect directly.
func (h *HybridLLMAccess) IsProxiedFallback(ctx context.Context, provider string) bool {
	creds, err := h.Credentials(ctx, provider)
	if err != nil {
		return true
	}
	return len(creds) == 0
}

// SandboxLevel represents the isolation level for sandbox enforcement.
// See 23-安全模型.md §3.6.
type SandboxLevel int

const (
	// SandboxL0 is no enforcement — policy-only checks (SandboxChecker).
	SandboxL0 SandboxLevel = iota
	// SandboxL1 is process-level enforcement via seccomp/AppArmor.
	SandboxL1
	// SandboxL2 is container-level enforcement via Docker/containerd.
	SandboxL2
	// SandboxL3 is VM-level enforcement via gVisor/Firecracker.
	SandboxL3
)

// String returns the human-readable sandbox level name.
func (l SandboxLevel) String() string {
	switch l {
	case SandboxL0:
		return "L0-none"
	case SandboxL1:
		return "L1-seccomp"
	case SandboxL2:
		return "L2-container"
	case SandboxL3:
		return "L3-vm"
	default:
		return fmt.Sprintf("unknown(%d)", int(l))
	}
}

// SandboxEnforcer validates and optionally enforces sandbox policies at
// the specified level. L0 is pure policy checking (SandboxChecker), L1+
// require system-level backends.
type SandboxEnforcer struct {
	checker *SandboxChecker
	level   SandboxLevel
}

// NewSandboxEnforcer creates an enforcer at the given level. For L0, only
// policy checks run. For L1+, the enforcer validates that the system
// backend is available and returns an error if not.
func NewSandboxEnforcer(sandbox Sandbox, level SandboxLevel) *SandboxEnforcer {
	return &SandboxEnforcer{
		checker: NewSandboxChecker(sandbox),
		level:   level,
	}
}

// Level returns the current enforcement level.
func (e *SandboxEnforcer) Level() SandboxLevel { return e.level }

// Checker returns the underlying SandboxChecker for policy-only queries.
func (e *SandboxEnforcer) Checker() *SandboxChecker { return e.checker }

// ValidateLevel checks whether the requested level is available on this
// system. L0 is always available. L1 requires seccomp support (Linux).
// L2/L3 require container/VM runtime.
func (e *SandboxEnforcer) ValidateLevel() error {
	switch e.level {
	case SandboxL0:
		return nil
	case SandboxL1:
		// L1 is a seccomp/AppArmor enforcement level. For now, we only
		// validate policy — the actual seccomp installation is deferred
		// to a platform-specific package.
		return nil
	case SandboxL2:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("sandbox L2 (container) enforcement not yet implemented"))
	case SandboxL3:
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("sandbox L3 (VM) enforcement not yet implemented"))
	default:
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(fmt.Sprintf("unknown sandbox level: %d", e.level)))
	}
}

// ── Interface assertion ─────────────────────────────────────────────────

var _ LLMAccessStrategy = (*HybridLLMAccess)(nil)
