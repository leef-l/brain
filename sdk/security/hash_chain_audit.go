package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// HashChainAuditLogger is an in-memory implementation of AuditLogger that
// materializes the append-only hash chain defined in 23-安全模型.md §8.2.
//
// Every Emit call:
//
//  1. fills ev.PrevHash from the current chain tail if the caller left it
//     blank, or rejects the event with CodeInvariantViolated if the caller
//     pinned a PrevHash that does not match the tail ("chain_mismatch");
//  2. computes ev.SelfHash = sha256(canonical(ev)) where canonical() omits
//     the SelfHash field itself, matching the normative recipe in 23 §8.4;
//  3. appends a deep copy of ev to the internal log so later callers that
//     mutate their own pointer cannot retroactively break the chain.
//
// Verify re-computes every SelfHash and checks that PrevHash on row N+1
// equals SelfHash on row N. It is the primary regression test surface for
// downstream sinks that persist the chain to disk / DB.
type HashChainAuditLogger struct {
	mu       sync.Mutex
	events   []AuditEvent
	tailHash string
}

// NewHashChainAuditLogger returns a fresh, empty logger. The genesis
// PrevHash is the zero string, matching 23 §8.2.
func NewHashChainAuditLogger() *HashChainAuditLogger {
	return &HashChainAuditLogger{}
}

// Emit appends ev to the chain. See 23-安全模型.md §8.2 and §8.4.
func (l *HashChainAuditLogger) Emit(ctx context.Context, ev *AuditEvent) error {
	if ev == nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("audit: nil event"))
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// Caller may leave PrevHash blank ("use tail") or pin it to the last
	// value they saw. A mismatch is a chain violation.
	if ev.PrevHash == "" {
		ev.PrevHash = l.tailHash
	} else if ev.PrevHash != l.tailHash {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf(
				"audit: chain_mismatch: got prev=%q want=%q",
				ev.PrevHash, l.tailHash,
			)))
	}

	h, err := canonicalHash(ev)
	if err != nil {
		return brainerrors.New(brainerrors.CodeAssertionFailed,
			brainerrors.WithMessage("audit: canonical hash failed: "+err.Error()))
	}
	ev.SelfHash = h

	// Deep-copy before persisting so callers cannot tamper with the chain
	// by retaining a pointer to ev.
	stored := copyAuditEvent(ev)
	l.events = append(l.events, stored)
	l.tailHash = h
	return nil
}

// Verify re-runs the hash chain from the genesis row and reports the first
// mismatch it finds. A successful Verify returns nil; tampering anywhere
// in the chain surfaces a CodeInvariantViolated BrainError.
func (l *HashChainAuditLogger) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := ""
	for i := range l.events {
		ev := l.events[i]
		if ev.PrevHash != prev {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage(fmt.Sprintf(
					"audit: prev_hash mismatch at index=%d got=%q want=%q",
					i, ev.PrevHash, prev,
				)))
		}
		expected, err := canonicalHash(&ev)
		if err != nil {
			return brainerrors.New(brainerrors.CodeAssertionFailed,
				brainerrors.WithMessage("audit: canonical hash failed during verify: "+err.Error()))
		}
		if expected != ev.SelfHash {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage(fmt.Sprintf(
					"audit: self_hash mismatch at index=%d got=%q want=%q",
					i, ev.SelfHash, expected,
				)))
		}
		prev = ev.SelfHash
	}
	return nil
}

// Snapshot returns a deep copy of the chain suitable for inspection by
// tests and debuggers. See 23-安全模型.md §8.4.
func (l *HashChainAuditLogger) Snapshot() []AuditEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]AuditEvent, len(l.events))
	for i := range l.events {
		out[i] = copyAuditEvent(&l.events[i])
	}
	return out
}

// Tail returns the current SelfHash of the chain, or "" if empty.
func (l *HashChainAuditLogger) Tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tailHash
}

// canonicalHash computes sha256 over a canonical representation of ev.
// The canonical form is a fixed field ordering, so two SDKs (Go / Python /
// TS) can reproduce identical hashes for replication. See 23 §8.4.
func canonicalHash(ev *AuditEvent) (string, error) {
	payloadJSON, err := canonicalPayload(ev.Payload)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	// Field order is load-bearing — do NOT reorder.
	h.Write([]byte(ev.EventID))
	h.Write([]byte{0x1f})
	h.Write([]byte(ev.Actor))
	h.Write([]byte{0x1f})
	h.Write([]byte(ev.Action))
	h.Write([]byte{0x1f})
	h.Write([]byte(ev.Resource))
	h.Write([]byte{0x1f})
	h.Write([]byte(ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z")))
	h.Write([]byte{0x1f})
	h.Write([]byte(ev.PrevHash))
	h.Write([]byte{0x1f})
	h.Write(payloadJSON)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// canonicalPayload marshals payload with sorted keys so the resulting
// byte string is deterministic across Go map iteration orders.
func canonicalPayload(payload map[string]interface{}) ([]byte, error) {
	if len(payload) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]struct {
		K string      `json:"k"`
		V interface{} `json:"v"`
	}, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, struct {
			K string      `json:"k"`
			V interface{} `json:"v"`
		}{K: k, V: payload[k]})
	}
	return json.Marshal(ordered)
}

func copyAuditEvent(ev *AuditEvent) AuditEvent {
	out := *ev
	if ev.Payload != nil {
		out.Payload = make(map[string]interface{}, len(ev.Payload))
		for k, v := range ev.Payload {
			out.Payload[k] = v
		}
	}
	return out
}
