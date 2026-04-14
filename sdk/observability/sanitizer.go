// Package observability — sanitizer.go defines the log sanitization layer
// specified in 24-可观测性.md §6.4.
//
// The sanitizer redacts sensitive patterns (API keys, tokens, passwords,
// credentials) from log messages and attribute values before they leave
// the process boundary. This prevents secrets from leaking into external
// log backends (OTLP Collector, Elasticsearch, stdout).
package observability

import (
	"regexp"
	"strings"
)

// LogSanitizer redacts sensitive data from log messages and attributes
// before export. Implementations MUST be safe for concurrent use.
// See 24-可观测性.md §6.4.
type LogSanitizer interface {
	// SanitizeMessage redacts sensitive patterns in a log message string.
	SanitizeMessage(msg string) string

	// SanitizeAttrs returns a new Labels map with sensitive values redacted.
	// The original map MUST NOT be mutated.
	SanitizeAttrs(attrs Labels) Labels
}

// redactedPlaceholder is the replacement string for redacted values.
const redactedPlaceholder = "[REDACTED]"

// ── Pattern-based sanitizer ─────────────────────────────────────────────

// sensitiveKeyPatterns matches attribute keys that should always be
// redacted regardless of value content.
var defaultSensitiveKeys = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey",
	"api-key", "authorization", "auth", "credential", "credentials",
	"private_key", "private-key", "access_key", "access-key",
	"session_id", "session-id", "cookie",
}

// sensitiveValuePatterns matches values that look like secrets.
var defaultValuePatterns = []*regexp.Regexp{
	// Bearer tokens
	regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9\-_.~+/]+=*`),
	// API keys (sk-*, pk-*, key-*)
	regexp.MustCompile(`(?i)(?:sk|pk|key)-[a-zA-Z0-9]{20,}`),
	// Anthropic API keys
	regexp.MustCompile(`sk-ant-[a-zA-Z0-9\-]{20,}`),
	// OpenAI API keys
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),
	// Generic long hex tokens (32+ chars)
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),
	// Base64-encoded blobs that look like credentials (40+ chars)
	regexp.MustCompile(`(?i)(?:password|secret|token|key)\s*[=:]\s*[a-zA-Z0-9+/]{40,}={0,2}`),
}

// PatternSanitizer implements LogSanitizer using configurable key patterns
// and value regex patterns. It is the default sanitizer for production use.
type PatternSanitizer struct {
	sensitiveKeys  map[string]struct{}
	valuePatterns  []*regexp.Regexp
	extraRedactors []func(string) string
}

// PatternSanitizerOption configures a PatternSanitizer.
type PatternSanitizerOption func(*PatternSanitizer)

// WithExtraSensitiveKeys adds additional attribute keys to the redaction
// list beyond the built-in defaults.
func WithExtraSensitiveKeys(keys ...string) PatternSanitizerOption {
	return func(s *PatternSanitizer) {
		for _, k := range keys {
			s.sensitiveKeys[strings.ToLower(k)] = struct{}{}
		}
	}
}

// WithExtraValuePatterns adds additional regex patterns for value redaction.
func WithExtraValuePatterns(patterns ...*regexp.Regexp) PatternSanitizerOption {
	return func(s *PatternSanitizer) {
		s.valuePatterns = append(s.valuePatterns, patterns...)
	}
}

// WithExtraRedactors adds custom redaction functions applied to all string
// values. Each function should return the redacted string.
func WithExtraRedactors(fns ...func(string) string) PatternSanitizerOption {
	return func(s *PatternSanitizer) {
		s.extraRedactors = append(s.extraRedactors, fns...)
	}
}

// NewPatternSanitizer creates a sanitizer with the default sensitive key
// and value patterns. Use options to extend.
func NewPatternSanitizer(opts ...PatternSanitizerOption) *PatternSanitizer {
	s := &PatternSanitizer{
		sensitiveKeys: make(map[string]struct{}, len(defaultSensitiveKeys)),
		valuePatterns: make([]*regexp.Regexp, len(defaultValuePatterns)),
	}
	for _, k := range defaultSensitiveKeys {
		s.sensitiveKeys[k] = struct{}{}
	}
	copy(s.valuePatterns, defaultValuePatterns)

	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SanitizeMessage redacts sensitive patterns found in the message string.
func (s *PatternSanitizer) SanitizeMessage(msg string) string {
	result := msg
	for _, pat := range s.valuePatterns {
		result = pat.ReplaceAllString(result, redactedPlaceholder)
	}
	for _, fn := range s.extraRedactors {
		result = fn(result)
	}
	return result
}

// SanitizeAttrs returns a new Labels map with sensitive keys fully redacted
// and sensitive value patterns replaced.
func (s *PatternSanitizer) SanitizeAttrs(attrs Labels) Labels {
	if attrs == nil {
		return nil
	}
	result := make(Labels, len(attrs))
	for k, v := range attrs {
		if s.isSensitiveKey(k) {
			result[k] = redactedPlaceholder
			continue
		}
		result[k] = s.sanitizeValue(v)
	}
	return result
}

func (s *PatternSanitizer) isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if _, ok := s.sensitiveKeys[lower]; ok {
		return true
	}
	// Check if any sensitive key is a substring (e.g. "x_api_key" contains "api_key").
	for sk := range s.sensitiveKeys {
		if strings.Contains(lower, sk) {
			return true
		}
	}
	return false
}

func (s *PatternSanitizer) sanitizeValue(v string) string {
	result := v
	for _, pat := range s.valuePatterns {
		result = pat.ReplaceAllString(result, redactedPlaceholder)
	}
	for _, fn := range s.extraRedactors {
		result = fn(result)
	}
	return result
}

// ── Interface assertion ─────────────────────────────────────────────────

var _ LogSanitizer = (*PatternSanitizer)(nil)
