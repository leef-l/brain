// Package security implements the security model defined in 23-安全模型.md.
//
// This package carries the frozen v1 contracts for:
//
//   - the five trust zones crossed by a Run (23 §2.1);
//   - the four-dimensional Sandbox (FS / Net / Proc / Sys, 23 §3);
//   - the Vault abstraction that is the single egress point for every secret
//     (23 §4.3);
//   - the LLMAccess strategy that governs whether a sidecar may talk to an
//     LLM provider directly (23 §5);
//   - the append-only, hash-chained AuditEvent shape (23 §8.4).
//
// Like the other wave-1 subpackages, security has zero dependencies on any
// other brain subpackage and only imports the standard library. Higher-level
// packages (kernel / runner / persistence / loop) import from here and MUST
// funnel every secret access, sandbox policy decision, and auditable side
// effect through these types so that the security properties promised by
// 23-安全模型.md are enforceable by a single audit surface.
package security

import (
	"context"
	"time"
)

// AuditEvent is the append-only audit record defined in 23-安全模型.md §8.4.
//
// Every security-relevant action on the Kernel (credential issuance, safety
// refusal, Zone 2 brain load, human override, Vault access failure, sidecar
// quarantine, etc.) MUST materialize as one AuditEvent. Events form an
// append-only hash chain via PrevHash / SelfHash (23 §8.2): PrevHash points
// at the previous row's SelfHash, and SelfHash is the SHA-256 of the current
// row with the SelfHash field itself elided. A chain mismatch MUST raise a
// P0 alert because it indicates possible tampering.
//
// The Payload map is the event-type specific body; for normative field
// layouts per event type see 23 附录 A. Implementations MUST NOT serialize
// raw secrets into Payload — only non-sensitive fingerprints (see
// 23 §4.3).
type AuditEvent struct {
	// EventID is the globally unique identifier of this event (snowflake).
	// See 23 §8.4.
	EventID string

	// Actor identifies who performed the action: a user id, a brain id, or
	// the literal "system" when the Kernel itself acts. See 23 §8.4.
	Actor string

	// Action is the event type string from 23 附录 A, e.g.
	// "safety_refused_blocked" or "vault_get_failed".
	Action string

	// Resource identifies the subject the action was performed against
	// (run id, brain id, tool call id, config key, ...). See 23 §8.4.
	Resource string

	// Timestamp is the wall-clock time the event was recorded, in UTC.
	// It MUST be monotonic-non-decreasing within a single hash chain.
	Timestamp time.Time

	// PrevHash is the SelfHash of the previous AuditEvent in the same chain,
	// or the zero string for the genesis event. See 23 §8.2.
	PrevHash string

	// SelfHash is the SHA-256 hex digest of this event computed over all
	// fields except SelfHash itself. See 23 §8.2 and §8.4.
	SelfHash string

	// Payload carries the event-type specific body described in 23 附录 A.
	// Secrets MUST NOT appear here; use fingerprints instead (23 §4.3).
	Payload map[string]interface{}
}

// AuditLogger is the sink that accepts AuditEvents and persists them to the
// oplog-backed hash chain described in 23-安全模型.md §8.2.
//
// Implementations MUST be append-only, MUST verify PrevHash continuity
// against the current chain tip, and MUST surface any verification failure
// as an error rather than silently accepting the event. The Kernel wires
// exactly one AuditLogger per process so that all subsystems share a single
// chain.
type AuditLogger interface {
	// Emit appends ev to the audit chain. Implementations MUST populate
	// ev.PrevHash and ev.SelfHash if the caller left them blank, MUST reject
	// the event on chain mismatch, and MUST NOT mutate ev.Payload. See
	// 23 §8.2 and §8.4.
	Emit(ctx context.Context, ev *AuditEvent) error
}
