package security

import (
	"context"
	"fmt"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// DirectLLMAccess is the LLMAccessStrategy for Zone 1 (built-in) brains
// that are authorized to hold short-lived provider credentials. The sidecar
// connects to the LLM provider directly using a credential fetched from the
// Vault at initialization time.
//
// Only Zone 1 brains may use this strategy. Zone 2 (third-party) brains
// MUST be rejected — see 23-安全模型.md §5.1.
type DirectLLMAccess struct {
	vault   Vault
	auditor AuditLogger
}

// NewDirectLLMAccess constructs a direct access strategy backed by the
// given Vault. The auditor is optional but recommended for production.
func NewDirectLLMAccess(vault Vault, auditor AuditLogger) *DirectLLMAccess {
	return &DirectLLMAccess{vault: vault, auditor: auditor}
}

// Mode returns "direct" per 23-安全模型.md §5.1.
func (d *DirectLLMAccess) Mode() string { return "direct" }

// Credentials fetches the provider API key from the Vault. The key naming
// convention is "llm/<provider>/api_key" (e.g. "llm/anthropic/api_key").
// See 23-安全模型.md §5.2.
func (d *DirectLLMAccess) Credentials(ctx context.Context, provider string) (map[string]string, error) {
	if d.vault == nil {
		return nil, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("direct LLM access: vault not configured"))
	}

	key := "llm/" + provider + "/api_key"
	secret, err := d.vault.Get(ctx, key)
	if err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("direct LLM access: credential for %q not found in vault", provider)))
	}

	if d.auditor != nil {
		_ = d.auditor.Emit(ctx, &AuditEvent{
			Actor:    "system",
			Action:   "llm_credential_issued",
			Resource: "llm/" + provider,
			Payload: map[string]interface{}{
				"mode":            "direct",
				"provider":        provider,
				"key_fingerprint": keyFingerprint(key),
			},
		})
	}

	return map[string]string{
		"api_key": secret,
	}, nil
}

// ── Interface assertion ─────────────────────────────────────────────────

var _ LLMAccessStrategy = (*DirectLLMAccess)(nil)
