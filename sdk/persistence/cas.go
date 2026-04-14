package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// casAlgoSha256 is the v1 CAS algorithm prefix defined in
// 26-持久化与恢复.md §6.2. New algorithms MUST pick a new prefix.
const casAlgoSha256 = "sha256"

// Sha256Hex returns the lowercase hexadecimal SHA-256 digest of data.
//
// This is the single authoritative primitive that every CAS key
// computation in the brain package funnels through so that the Go host
// and any sidecar SDKs produce byte-identical Refs — the parity
// requirement in 26-持久化与恢复.md §3 and §6.2.
//
// This function is intentionally implemented (not a panic stub) per the
// decision logged in brain骨架实施计划.md §8 (2026-04-11 · cas.go 允许真实实现):
// it is a pure function over stdlib primitives with no cross-package
// coupling, so landing it now lets every downstream caller depend on a
// stable algorithm from day one.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ComputeKey returns the CAS Ref for data using the v1 sha256 algorithm.
//
// The returned Ref has the frozen wire format "sha256/<64 lowercase hex>"
// defined in 26-持久化与恢复.md §6.2. Like Sha256Hex, ComputeKey is a pure
// helper that is implemented (not a panic stub) so every ArtifactStore
// backend can rely on a single authoritative key builder from day one.
// See the same §8 decision referenced above.
func ComputeKey(data []byte) Ref {
	return Ref(casAlgoSha256 + "/" + Sha256Hex(data))
}

// ParseRef splits a CAS Ref into its (algorithm, hex) components and
// validates the v1 wire format from 26-持久化与恢复.md §6.2.
//
// The only v1 algorithm is "sha256"; §6.2 requires every new algorithm
// to claim a new prefix rather than reuse "sha256/", so ParseRef rejects
// anything else with CodeInvalidParams. Malformed shape, non-hex
// characters, or a digest that is not exactly 64 lowercase hex chars all
// return ClassUserFault errors so the caller can safely surface them to
// an end user.
func ParseRef(s string) (algo string, hexDigest string, err error) {
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || slash == len(s)-1 {
		return "", "", brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(fmt.Sprintf("cas ref %q missing algorithm prefix", s)),
		)
	}
	algo = s[:slash]
	hexDigest = s[slash+1:]

	if algo != casAlgoSha256 {
		return "", "", brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(fmt.Sprintf("cas ref %q uses unsupported algorithm %q (v1 only accepts %q)", s, algo, casAlgoSha256)),
		)
	}
	if len(hexDigest) != 64 {
		return "", "", brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(fmt.Sprintf("cas ref %q digest length = %d, want 64", s, len(hexDigest))),
		)
	}
	for i := 0; i < len(hexDigest); i++ {
		c := hexDigest[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", "", brainerrors.New(brainerrors.CodeInvalidParams,
				brainerrors.WithMessage(fmt.Sprintf("cas ref %q digest contains non-lowercase-hex byte at offset %d", s, i)),
			)
		}
	}
	return algo, hexDigest, nil
}
