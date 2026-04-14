package security

import "context"

// ProxiedLLMAccess is the default LLMAccessStrategy for every Zone 2
// (third-party) sidecar, per 23-安全模型.md §2.3 clause 1 and §5.1. The
// strategy never issues a credential to the sidecar: every llm.* call
// must instead be routed back through the Kernel as a reverse RPC so the
// Kernel can inject the provider key and own the usage accounting.
//
// Concretely, Credentials always returns an empty map and a nil error —
// callers interpret an empty credential bundle as "go through the Kernel".
type ProxiedLLMAccess struct{}

// NewProxiedLLMAccess constructs the singleton proxied strategy.
// See 23-安全模型.md §5.1.
func NewProxiedLLMAccess() *ProxiedLLMAccess { return &ProxiedLLMAccess{} }

// Mode returns the literal "proxied" per 23 §5.1.
func (p *ProxiedLLMAccess) Mode() string { return "proxied" }

// Credentials returns an empty credential map per 23 §5.2: proxied
// sidecars are not allowed to hold provider keys.
func (p *ProxiedLLMAccess) Credentials(ctx context.Context, provider string) (map[string]string, error) {
	return map[string]string{}, nil
}
