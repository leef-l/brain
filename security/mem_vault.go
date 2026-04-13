package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// MemVault is an in-memory implementation of Vault suitable for unit tests,
// the `brain doctor` smoke run, and the v0.1.0 reference Kernel. It honors
// the audit and secret-hygiene rules from 23-安全模型.md §4.3: every Get /
// Put / Delete call is offered to the configured AuditLogger with a
// non-sensitive key fingerprint, and no raw value is ever emitted through
// logs or audit events.
//
// The backing map is guarded by a sync.RWMutex so concurrent sidecars can
// resolve secrets without serializing on writes. TTLs are optional and
// enforced lazily on Get (expired entries are evicted in-place).
//
// MemVault implements the full Vault interface including Rotate and List.
type MemVault struct {
	mu      sync.RWMutex
	entries map[string]memVaultEntry
	auditor AuditLogger
	now     func() time.Time // injectable for tests
}

type memVaultEntry struct {
	Value     string
	ExpiresAt time.Time // zero means no expiry
}

// MemVaultOption configures a MemVault at construction time.
// See 23-安全模型.md §4.3 for the required behavior.
type MemVaultOption func(*MemVault)

// WithMemVaultAuditor wires an AuditLogger into the Vault so every Get /
// Put / Delete call produces an audit event. See 23-安全模型.md §4.3.
func WithMemVaultAuditor(a AuditLogger) MemVaultOption {
	return func(v *MemVault) {
		v.auditor = a
	}
}

// WithMemVaultClock overrides the wall-clock source used for TTL eviction.
// Test-only; production callers should leave the default time.Now.
func WithMemVaultClock(now func() time.Time) MemVaultOption {
	return func(v *MemVault) {
		if now != nil {
			v.now = now
		}
	}
}

// NewMemVault constructs an empty MemVault. Pass WithMemVaultAuditor to
// have credential access emit audit events per 23 §4.3.
func NewMemVault(opts ...MemVaultOption) *MemVault {
	v := &MemVault{
		entries: make(map[string]memVaultEntry),
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// Get retrieves a secret bound to key. A missing or expired key returns a
// CodeRecordNotFound BrainError. See 23-安全模型.md §4.3.
func (v *MemVault) Get(ctx context.Context, key string) (string, error) {
	if err := validateVaultKey(key); err != nil {
		v.audit(ctx, "vault_get_rejected", key, map[string]interface{}{"reason": "invalid_key"})
		return "", err
	}

	v.mu.RLock()
	ent, ok := v.entries[key]
	v.mu.RUnlock()

	if ok && !ent.ExpiresAt.IsZero() && !v.now().Before(ent.ExpiresAt) {
		// TTL expired — drop it and treat as missing.
		v.mu.Lock()
		// Re-check under write lock to avoid racing with concurrent Put.
		if cur, stillThere := v.entries[key]; stillThere && cur == ent {
			delete(v.entries, key)
		}
		v.mu.Unlock()
		ok = false
	}

	if !ok {
		v.audit(ctx, "vault_get_not_found", key, nil)
		return "", brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("vault: key not found"))
	}
	v.audit(ctx, "vault_get_success", key, nil)
	return ent.Value, nil
}

// Put stores value under key with no TTL. See 23-安全模型.md §4.3.
func (v *MemVault) Put(ctx context.Context, key, value string) error {
	return v.PutWithTTL(ctx, key, value, 0)
}

// PutWithTTL stores value under key with the given TTL. A zero or negative
// TTL means no expiry. See 23-安全模型.md §4.3.
func (v *MemVault) PutWithTTL(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := validateVaultKey(key); err != nil {
		v.audit(ctx, "vault_put_rejected", key, map[string]interface{}{"reason": "invalid_key"})
		return err
	}
	var expires time.Time
	if ttl > 0 {
		expires = v.now().Add(ttl)
	}
	v.mu.Lock()
	v.entries[key] = memVaultEntry{Value: value, ExpiresAt: expires}
	v.mu.Unlock()
	v.audit(ctx, "vault_put", key, nil)
	return nil
}

// Delete removes the secret bound to key. Deleting a missing key is not an
// error (idempotent), matching the Vault v1 contract in 23 §4.3.
func (v *MemVault) Delete(ctx context.Context, key string) error {
	if err := validateVaultKey(key); err != nil {
		v.audit(ctx, "vault_delete_rejected", key, map[string]interface{}{"reason": "invalid_key"})
		return err
	}
	v.mu.Lock()
	delete(v.entries, key)
	v.mu.Unlock()
	v.audit(ctx, "vault_delete", key, nil)
	return nil
}

// Rotate atomically replaces the value for key. If key does not exist,
// returns CodeRecordNotFound. See 23-安全模型.md §4.3.
func (v *MemVault) Rotate(ctx context.Context, key, newValue string) error {
	if err := validateVaultKey(key); err != nil {
		v.audit(ctx, "vault_rotate_rejected", key, map[string]interface{}{"reason": "invalid_key"})
		return err
	}

	v.mu.Lock()
	ent, ok := v.entries[key]
	if !ok {
		v.mu.Unlock()
		v.audit(ctx, "vault_rotate_not_found", key, nil)
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("vault: key not found for rotation"))
	}
	// Check TTL expiry under lock.
	if !ent.ExpiresAt.IsZero() && !v.now().Before(ent.ExpiresAt) {
		delete(v.entries, key)
		v.mu.Unlock()
		v.audit(ctx, "vault_rotate_not_found", key, map[string]interface{}{"reason": "expired"})
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("vault: key expired before rotation"))
	}
	// Preserve the TTL of the existing entry.
	v.entries[key] = memVaultEntry{Value: newValue, ExpiresAt: ent.ExpiresAt}
	v.mu.Unlock()

	v.audit(ctx, "vault_rotate", key, nil)
	return nil
}

// List returns all key names matching the given prefix. An empty prefix
// returns all keys. Values are never returned. See 23-安全模型.md §4.3.
func (v *MemVault) List(ctx context.Context, prefix string) ([]string, error) {
	v.mu.RLock()
	now := v.now()
	var keys []string
	for k, ent := range v.entries {
		// Skip expired entries.
		if !ent.ExpiresAt.IsZero() && !now.Before(ent.ExpiresAt) {
			continue
		}
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	v.mu.RUnlock()

	v.audit(ctx, "vault_list", prefix, map[string]interface{}{"count": len(keys)})
	return keys, nil
}

// Len returns the current number of entries (post lazy-eviction style: this
// does NOT pro-actively sweep TTLs). Test helper.
func (v *MemVault) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.entries)
}

// audit emits an AuditEvent if an auditor is wired. Key fingerprint is
// sha256 hex truncated to 12 chars so log consumers can correlate accesses
// without seeing the raw key. Raw values are NEVER forwarded.
// See 23-安全模型.md §4.3.
func (v *MemVault) audit(ctx context.Context, action, key string, extra map[string]interface{}) {
	if v.auditor == nil {
		return
	}
	payload := map[string]interface{}{
		"key_fingerprint": keyFingerprint(key),
	}
	for k, val := range extra {
		payload[k] = val
	}
	ev := &AuditEvent{
		Actor:     "system",
		Action:    action,
		Resource:  "vault/" + keyFingerprint(key),
		Timestamp: v.now().UTC(),
		Payload:   payload,
	}
	_ = v.auditor.Emit(ctx, ev)
}

// validateVaultKey enforces the key hygiene rules from 23 §4.3: non-empty,
// no embedded newlines (to keep audit lines single-line), no leading /
// trailing whitespace.
func validateVaultKey(key string) error {
	if key == "" {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("vault: key is empty"))
	}
	if strings.TrimSpace(key) != key {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("vault: key contains leading or trailing whitespace"))
	}
	if strings.ContainsAny(key, "\n\r") {
		return brainerrors.New(brainerrors.CodeToolInputInvalid,
			brainerrors.WithMessage("vault: key contains control characters"))
	}
	return nil
}

// keyFingerprint returns sha256(key) truncated to 12 hex chars. Suitable
// for audit correlation without leaking the raw key. See 23 §4.3.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}
