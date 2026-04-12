package security

import "context"

// Vault is the single egress point for every secret inside the Kernel,
// defined in 23-安全模型.md §4.3.
//
// All business code — Kernel core, runners, sidecars, tools — MUST fetch
// secrets via Vault.Get rather than reading os.Getenv, config files, or
// database columns directly (23 §4.2 and §4.3). Implementations MUST:
//
//   - record an audit event for every Get call (success or failure);
//   - return only non-sensitive Fingerprint data for logging, never the
//     raw Value (23 §4.3);
//   - zero the returned secret buffer as soon as it leaves the calling
//     stack frame (23 §4.2);
//   - enforce scope-based authorization so only whitelisted brains can
//     retrieve a given key.
//
// The wave-1 skeleton defines only the minimal Get / Put / Delete surface
// consumed by the rest of the Kernel. The richer rotation / listing API
// from 23 §4.3 will be layered on top in a later wave.
type Vault interface {
	// Get retrieves the secret bound to key. Implementations MUST emit
	// an audit event (see 23 §4.3) and MUST NOT log the raw value.
	Get(ctx context.Context, key string) (string, error)

	// Put stores value under key. Implementations MUST write only to the
	// secure backing store (column-encrypted DB, KMS, etc.) defined in
	// 23 §4.2 and MUST NOT persist the plaintext anywhere else.
	Put(ctx context.Context, key, value string) error

	// Delete removes the secret bound to key. Implementations MUST record
	// the deletion as an audit event and MUST zero any in-memory copies
	// still referenced by the caller.
	Delete(ctx context.Context, key string) error
}
