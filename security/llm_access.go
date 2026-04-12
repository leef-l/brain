package security

import "context"

// LLMAccessStrategy decides how a brain sidecar is allowed to talk to an
// LLM provider, as defined in 23-安全模型.md §5 (and the 02 §12.5.7
// handshake it refines).
//
// Three modes exist:
//
//   - "proxied" — default and only option for Zone 2 sidecars. The sidecar
//     never holds credentials; every llm.complete call is issued as a
//     reverse RPC through the Kernel, which injects the provider key. All
//     usage accounting happens centrally. See 23 §5.1 and §5.2.
//   - "direct" — the sidecar connects to the provider itself using a
//     short-lived credential handed out by Vault during initialize. The
//     sidecar MUST report usage back via trace.emit. Only allowed for
//     Zone 1 built-in brains.
//   - "hybrid" — the sidecar may request an ephemeral credential on demand
//     for a specific call budget. Every issuance MUST be audited and
//     scoped; the default is still proxied.
//
// Implementations MUST return credential maps that contain only the
// non-sensitive fingerprints described in 23 §4.3 when logging, and MUST
// zero the raw values as soon as they leave the caller's stack. Zone 2
// sidecars MUST be rejected by concrete strategies whenever Mode() would
// return anything other than "proxied" (23 §2.3 clause 1).
type LLMAccessStrategy interface {
	// Mode returns the literal access mode string ("proxied", "direct",
	// or "hybrid"). See 23 §5.1.
	Mode() string

	// Credentials issues the credential bundle the sidecar should use to
	// reach the given provider ("anthropic", "openai", "gemini", ...).
	// For "proxied" mode the map MUST be empty; for "direct" or "hybrid"
	// mode the map carries short-lived secrets that MUST be audited via
	// the AuditLogger. See 23 §5.2.
	Credentials(ctx context.Context, provider string) (map[string]string, error)
}
