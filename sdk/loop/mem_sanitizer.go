package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/tool"
)

// MemSanitizer is the in-process ToolResultSanitizer from
// 22-Agent-Loop规格.md §10. It implements the six-stage pipeline
// defined in §10.2 on top of raw tool.Result payloads:
//
//  1. Length clipping — oversize text is truncated to MaxOutputBytes
//     and the original length is recorded in the returned block.
//  2. Binary detection — payloads that contain NUL bytes or whose
//     non-printable ratio exceeds 30 % are rejected with a
//     tool_sanitize_failed BrainError (the Runner is expected to
//     fall back to an artifact reference instead).
//  3. Control-character stripping — ANSI escape sequences are removed
//     and any other C0/C1 control byte (except \t \n \r) is replaced
//     with the Unicode replacement character U+FFFD.
//  4. Unicode hygiene — BIDI override / RTL tricks (U+202A–U+202E,
//     U+2066–U+2069) and zero-width clusters are stripped. A full
//     NFC normalization would require golang.org/x/text which is
//     banned by §4.6 of brain骨架实施计划.md; the zero-width / BIDI
//     guards cover the prompt-injection vectors §10.2 cares about.
//  5. Prompt injection heuristics — occurrences of the phrases in
//     PromptInjectionPhrases are annotated (not removed) by prefixing
//     the output with a warning; the raw text stays intact so the
//     LLM can still reason about it.
//  6. PII masking — API-key / email / phone regex matches are
//     replaced with stable placeholders.
//
// The sanitized text is wrapped in the <tool_output> envelope from
// §10.3 before being returned as an llm.ContentBlock of type "text".
// The tool_use_id correlation is fed in by the Runner through the
// SanitizeMeta.ToolName + RunID fields; the envelope attribute stays
// informational because the loop package does not know the real
// tool_use_id yet (that is a Runner concern).
//
// Stdlib-only per brain骨架实施计划.md §4.6.
type MemSanitizer struct {
	// MaxOutputBytes is the upper bound on the text length fed back
	// to the LLM after stage 1. Defaults to 128 KiB which is roughly
	// the §10.2 "32k tokens" rule of thumb at 4 bytes/token.
	MaxOutputBytes int

	// BinaryRejectRatio is the non-printable byte ratio above which
	// stage 2 rejects the payload. Defaults to 0.30 per §10.2.
	BinaryRejectRatio float64

	// PromptInjectionPhrases is the case-insensitive phrase list the
	// stage-5 heuristic scans for. Callers MAY extend this slice at
	// construction time to add project-specific tells.
	PromptInjectionPhrases []string

	// MaskAPIKeys toggles the stage-6 API-key regex. Defaults to true.
	MaskAPIKeys bool

	// MaskEmails toggles the stage-6 email regex. Defaults to true.
	MaskEmails bool

	// MaskPhones toggles the stage-6 phone regex. Defaults to true.
	MaskPhones bool
}

// NewMemSanitizer builds a MemSanitizer with the §10.2 defaults. The
// returned sanitizer is safe for concurrent use across Runs because
// all state is read-only after construction.
func NewMemSanitizer() *MemSanitizer {
	return &MemSanitizer{
		MaxOutputBytes:    128 * 1024,
		BinaryRejectRatio: 0.30,
		PromptInjectionPhrases: []string{
			"ignore all previous instructions",
			"ignore the above",
			"disregard the above",
			"you are now",
			"system prompt",
			"print your instructions",
		},
		MaskAPIKeys: true,
		MaskEmails:  true,
		MaskPhones:  true,
	}
}

// Pre-compiled masking regexes — compiled once at package load so the
// hot path stays allocation-free. Stdlib-only (regexp).
var (
	// ANSI CSI / OSC escape sequences. Covers most terminal colorizers.
	ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`)

	// sk-live / Bearer / generic 40+ char hex API keys.
	apiKeyRe = regexp.MustCompile(`(?i)\b(sk-[A-Za-z0-9_\-]{16,}|bearer\s+[A-Za-z0-9._\-]{16,}|[A-Fa-f0-9]{40,})\b`)

	// RFC5322-lite email. Intentionally permissive — we just need to
	// mask the obvious shape, not validate addresses.
	emailRe = regexp.MustCompile(`\b[\w.+\-]+@[\w\-]+(?:\.[\w\-]+)+\b`)

	// E.164 and CN mobile numbers. Covers +8613812345678 and
	// 13812345678. Not exhaustive; the goal is obvious-shape masking.
	phoneRe = regexp.MustCompile(`\+?\d{1,3}?[\s\-]?1[3-9]\d{9}|\b\+?\d{10,14}\b`)
)

// Sanitize runs the six-stage pipeline from §10.2 over raw and returns
// a single llm.ContentBlock of type "text" whose payload is wrapped in
// the §10.3 <tool_output> envelope. On an unrecoverable violation the
// method returns a CodeToolSanitizeFailed BrainError; the Runner MUST
// then escalate per §10.2.
func (s *MemSanitizer) Sanitize(ctx context.Context, raw *tool.Result, meta SanitizeMeta) (*llm.ContentBlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapLoopCtxErr(err)
	}
	if raw == nil {
		return nil, brainerrors.New(brainerrors.CodeToolSanitizeFailed,
			brainerrors.WithMessage("MemSanitizer.Sanitize: raw result is nil"),
		)
	}
	// The sanitizer operates on the textual projection of the raw
	// Output JSON. json.RawMessage preserves whatever bytes the tool
	// produced; we read them verbatim to give stage 2 a fair shot at
	// catching binary leaks.
	text := string(raw.Output)

	// Stage 1 — length clipping.
	limit := s.MaxOutputBytes
	if limit <= 0 {
		limit = 128 * 1024
	}
	truncated := false
	if len(text) > limit {
		text = text[:limit]
		truncated = true
	}

	// Stage 2 — binary detection. The §10.2 rule has two triggers:
	// any NUL byte OR non-printable ratio > BinaryRejectRatio.
	if containsNulByte(text) || nonPrintableRatio(text) > s.binaryRejectRatio() {
		return nil, brainerrors.New(brainerrors.CodeToolSanitizeFailed,
			brainerrors.WithMessage(fmt.Sprintf(
				"MemSanitizer.Sanitize: binary content rejected (tool=%s risk=%s run=%s)",
				meta.ToolName, meta.Risk, meta.RunID,
			)),
		)
	}

	// Stage 3 — control-character stripping.
	text = ansiEscapeRe.ReplaceAllString(text, "")
	text = stripControlChars(text)

	// Stage 4 — Unicode hygiene. We reject BIDI overrides outright so
	// the model never sees a right-to-left flipped payload, and strip
	// zero-width characters that tend to be used for prompt-injection
	// homoglyph tricks.
	text = stripBIDIAndZeroWidth(text)

	// Stage 5 — prompt injection heuristic. We annotate rather than
	// delete so the LLM can still analyze legitimate discussions of
	// "ignore previous instructions" (e.g. a blog post fragment).
	injectionHit := detectPromptInjection(text, s.PromptInjectionPhrases)

	// Stage 6 — PII masking.
	text = s.maskPII(text)

	// §10.3 envelope. Runner level will replace the tool_use_id with
	// the real one; we expose the informational metadata so downstream
	// observers can correlate the sanitized block to its source.
	envelope := buildToolOutputEnvelope(meta.ToolName, meta.RunID, text, truncated, injectionHit)

	// Serialize the envelope into the text field of a ContentBlock.
	// We also mirror the original Output into the block's Output
	// slot as a compact JSON so the Runner can recover the raw payload
	// for persistence without re-running the sanitizer.
	compact, err := compactJSONBytes(raw.Output)
	if err != nil {
		// A non-JSON output is legal — tools may return plain text —
		// so failing compact is not fatal. We fall back to the raw
		// bytes unchanged.
		compact = raw.Output
	}
	return &llm.ContentBlock{
		Type:    "tool_result",
		Text:    envelope,
		Output:  compact,
		IsError: raw.IsError,
	}, nil
}

func (s *MemSanitizer) binaryRejectRatio() float64 {
	if s.BinaryRejectRatio <= 0 {
		return 0.30
	}
	return s.BinaryRejectRatio
}

// containsNulByte reports whether s contains a NUL byte anywhere.
// NUL bytes are a strong signal that the payload is binary and would
// be truncated by any downstream C string consumer.
func containsNulByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0x00 {
			return true
		}
	}
	return false
}

// nonPrintableRatio returns the fraction of bytes in s that are not
// printable ASCII or valid UTF-8 printable runes. Used by §10.2 stage
// 2 to reject mostly-binary payloads.
func nonPrintableRatio(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	nonPrintable := 0
	total := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		total++
		i += size
		if r == utf8.RuneError && size == 1 {
			nonPrintable++
			continue
		}
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if !unicode.IsPrint(r) {
			nonPrintable++
		}
	}
	return float64(nonPrintable) / float64(total)
}

// stripControlChars replaces C0/C1 control bytes (other than \t \n \r)
// with the Unicode replacement character U+FFFD per §10.2 stage 3.
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			b.WriteRune('\ufffd')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripBIDIAndZeroWidth removes bidirectional override and zero-width
// code points commonly used for prompt injection and homoglyph tricks
// per §10.2 stage 4.
func stripBIDIAndZeroWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		// BIDI controls — U+202A..U+202E, U+2066..U+2069.
		case 0x202A, 0x202B, 0x202C, 0x202D, 0x202E,
			0x2066, 0x2067, 0x2068, 0x2069,
			// Zero-width joiners / non-joiners / spaces.
			0x200B, 0x200C, 0x200D, 0xFEFF:
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// detectPromptInjection reports whether any of phrases appears in text
// (case-insensitive). The check is intentionally O(n·m) — the phrase
// list is tiny and this path runs once per Turn, so compiling an Aho-
// Corasick machine would be overkill.
func detectPromptInjection(text string, phrases []string) bool {
	lower := strings.ToLower(text)
	for _, p := range phrases {
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// maskPII runs the stage-6 regexes against text and replaces matches
// with stable placeholders. Callers can disable individual families
// via the MaskAPIKeys / MaskEmails / MaskPhones toggles.
func (s *MemSanitizer) maskPII(text string) string {
	if s.MaskAPIKeys {
		text = apiKeyRe.ReplaceAllString(text, "[REDACTED_APIKEY]")
	}
	if s.MaskEmails {
		text = emailRe.ReplaceAllString(text, "[REDACTED_EMAIL]")
	}
	if s.MaskPhones {
		text = phoneRe.ReplaceAllString(text, "[REDACTED_PHONE]")
	}
	return text
}

// buildToolOutputEnvelope wraps body in the §10.3 <tool_output> XML
// envelope. The envelope is intentionally terse: toolName, runID, a
// truncated attribute, and an injection flag. It is NOT a full XML
// payload — only a human-readable hint for the LLM, consistent with
// the spec example.
func buildToolOutputEnvelope(toolName, runID, body string, truncated, injectionHit bool) string {
	var header strings.Builder
	header.WriteString("<tool_output tool=\"")
	header.WriteString(escapeAttr(toolName))
	header.WriteString("\" run_id=\"")
	header.WriteString(escapeAttr(runID))
	header.WriteString("\" trust=\"untrusted\"")
	if truncated {
		header.WriteString(" truncated=\"1\"")
	}
	if injectionHit {
		header.WriteString(" prompt_injection_suspected=\"1\"")
	}
	header.WriteString(">\n")
	header.WriteString(body)
	header.WriteString("\n</tool_output>")
	return header.String()
}

// escapeAttr minimally escapes double-quote and less-than so the
// tool_output envelope stays parseable. A full XML escape is not
// needed — the envelope is advisory, not machine-parsed.
func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, `"`, `&quot;`)
	s = strings.ReplaceAll(s, `<`, `&lt;`)
	return s
}

// compactJSONBytes returns the compact JSON form of raw. If raw is not
// valid JSON the error is surfaced and the caller decides whether to
// keep the raw bytes as-is.
func compactJSONBytes(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}
