package errors

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Fingerprint computes the 16-hex-char aggregation key defined in
// 21-错误模型.md §6.1.
//
// The canonical input is:
//
//	error_code | brain_id | cause.error_code | normalized(message)
//
// joined with the literal "|" byte (no surrounding spaces). The join
// preserves empty segments — an error with no BrainID still has three "|"
// separators so that "code||cause|msg" and "code|brain|cause|msg" never
// collide. The canonical string is hashed with SHA-256 and the first 8 bytes
// are returned as lowercase hex (16 chars). See 21 §6.1 for the normative
// algorithm and 21 §6.3 for the excluded volatile fields (occurred_at,
// trace_id, span_id, sidecar_pid, attempt, stack, request ids) that MUST NOT
// leak into the fingerprint.
//
// Fingerprint is deterministic: calling it twice on a BrainError built from
// the same inputs MUST yield the same string, and two errors that only
// differ in volatile fields MUST collapse to the same fingerprint. This is
// what lets the alerting layer count "the same error" across replicas.
//
// Passing nil returns the empty string rather than panicking so that
// Fingerprint can be called unconditionally from the constructors.
func Fingerprint(err *BrainError) string {
	if err == nil {
		return ""
	}
	var causeCode string
	if err.Cause != nil {
		causeCode = err.Cause.ErrorCode
	}
	canonical := strings.Join([]string{
		err.ErrorCode,
		err.BrainID,
		causeCode,
		NormalizeMessage(err.Message),
	}, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:8])
}

// Message normalization regexes implementing the 21-错误模型.md §6.2 rule
// set. The order matters: more specific patterns (UUID, ISO-8601, paths)
// MUST run before the generic numeric / hex patterns so that the concrete
// replacements happen first and the fallback rules only see leftovers.
//
// Every regex is compiled once at package init time so that NormalizeMessage
// has zero per-call allocation for the regex machinery. The regexes are
// intentionally anchored by class characters rather than word boundaries —
// Go's regexp engine does not support lookbehind, so we lean on the literal
// delimiter classes to avoid false positives (e.g. the UUID rule requires
// hyphens in the expected positions).
var (
	// reUUID — canonical 8-4-4-4-12 RFC 4122 layout, case-insensitive.
	reUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

	// reISO8601 — YYYY-MM-DDTHH:MM:SS with optional fractional seconds and
	// optional timezone. Must run before rePath (the T separator looks like
	// a path otherwise) and before reDigits.
	reISO8601 = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?\b`)

	// reQuotedString — double-quoted or single-quoted substrings. Non-greedy
	// so adjacent quoted segments stay distinct. Intentionally does NOT
	// handle escaped quotes — messages rarely contain them and the extra
	// backtracking would slow every call.
	reQuotedString = regexp.MustCompile(`"[^"]*"|'[^']*'`)

	// rePath — Unix-style absolute path with at least one segment. Matches
	// "/foo/bar", "/etc/passwd", "/var/log/app.log". The trailing char class
	// is the character set allowed inside path segments.
	rePath = regexp.MustCompile(`(?:/[A-Za-z0-9_.\-]+)+/?`)

	// reHexBlob — hex run of 8+ chars that is NOT already matched by reUUID
	// (UUID runs first and wipes those hyphen-layout strings). Common source:
	// SHA hashes, memory addresses without 0x prefix.
	reHexBlob = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)

	// reDigits — any remaining run of digits. Runs last so the structural
	// replacements above have already consumed the digits embedded in
	// UUIDs / ISO-8601 / hex blobs / paths with numeric segments.
	reDigits = regexp.MustCompile(`\d+`)
)

// NormalizeMessage applies the 21-错误模型.md §6.2 normalizer so two errors
// that differ only in volatile substrings fingerprint the same. The rule
// set is:
//
//  1. Quoted strings            "..."/'...' → <STR>
//  2. ISO-8601 timestamps       2026-04-11T14:00:00Z → <TIME>
//  3. UUIDs (RFC 4122 layout)   xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx → <ID>
//  4. Unix paths                /foo/bar.log → <PATH>
//  5. Hex blobs (len ≥ 8)       deadbeefcafe → <HEX>
//  6. Remaining digit runs      42 → <N>
//
// Leading/trailing whitespace is trimmed and internal runs of whitespace are
// collapsed to a single space, matching the example in 21 §6.2. Callers
// SHOULD pass the raw BrainError.Message — the function is idempotent, so
// normalizing an already-normalized string is a no-op.
//
// Exported so tests and the compliance runner can assert exact fingerprint
// stability against the appendix vectors.
func NormalizeMessage(msg string) string {
	if msg == "" {
		return ""
	}
	// Order matters — structural replacements first, generic ones last.
	msg = reQuotedString.ReplaceAllString(msg, "<STR>")
	msg = reISO8601.ReplaceAllString(msg, "<TIME>")
	msg = reUUID.ReplaceAllString(msg, "<ID>")
	msg = rePath.ReplaceAllString(msg, "<PATH>")
	msg = reHexBlob.ReplaceAllString(msg, "<HEX>")
	msg = reDigits.ReplaceAllString(msg, "<N>")

	// Collapse whitespace so "foo  bar" and "foo bar" share a fingerprint.
	msg = strings.TrimSpace(msg)
	if strings.ContainsAny(msg, " \t\n\r") {
		fields := strings.Fields(msg)
		msg = strings.Join(fields, " ")
	}
	return msg
}
