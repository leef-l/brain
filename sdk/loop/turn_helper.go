package loop

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewTurn constructs a Turn for the given runID and 1-based index. The UUID
// field is populated with a cryptographically random 16-byte hex string
// (32 hex characters) generated via crypto/rand so it can serve as the
// idempotency key required by 22-Agent-Loop规格.md §6.1 and §12. StartedAt is
// set to now; EndedAt is nil (the Turn is in progress).
//
// See 22-Agent-Loop规格.md §6.1.
func NewTurn(runID string, index int, now time.Time) *Turn {
	return &Turn{
		Index:     index,
		RunID:     runID,
		UUID:      mustRandHex16(),
		StartedAt: now,
	}
}

// End marks the Turn as complete by stamping EndedAt with now. Calling End
// more than once is idempotent — subsequent calls update the timestamp to the
// latest value. See 22-Agent-Loop规格.md §6.1.
func (t *Turn) End(now time.Time) {
	t.EndedAt = &now
}

// mustRandHex16 generates a 16-byte random value and returns it as a 32-
// character lower-case hex string. It panics only if crypto/rand.Read fails,
// which the Go standard library documents as never happening on supported
// platforms.
func mustRandHex16() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read should never fail on any supported OS.
		// If it does, something is fundamentally wrong with the platform.
		panic("brain/loop: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
