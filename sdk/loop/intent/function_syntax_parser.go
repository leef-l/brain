package intent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// FunctionSyntaxParser handles bare function-call syntax produced by some
// LLMs (Mimo / Qwen / smaller fine-tunes especially) when they think they
// are writing pseudo-code instead of issuing a tool call:
//
//   code.write_file({"path": "game.html", "content": "..."})
//   write_file(path="game.html", content="...")
//
// We detect both the `name({json_object})` form (preferred — the args
// are guaranteed parseable as JSON) and the `name(arg=value, ...)` form
// (we synthesize JSON, but with reduced confidence).
//
// Confidence:
//
//   - JSON object form         → 0.80
//   - Keyword-argument form    → 0.65 (synthesis can drop strings, etc.)
type FunctionSyntaxParser struct{}

// Name implements Parser.
func (FunctionSyntaxParser) Name() string { return "function_syntax" }

var (
	// nameOpenRe matches a tool-name followed by an opening parenthesis.
	// The name must be alphanumeric with optional dots / underscores.
	// The match is non-anchored so we can scan a whole text block.
	nameOpenRe = regexp.MustCompile(`(?m)\b([a-zA-Z][a-zA-Z0-9_]*(?:\.[a-zA-Z][a-zA-Z0-9_]*)?)\s*\(`)
	// kwargRe matches a single key=value pair inside parens. Values can
	// be quoted strings (single or double), bare identifiers/numbers.
	kwargRe = regexp.MustCompile(`(?s)\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|([^,\s)]+))\s*,?`)
)

// Parse iterates each text content block, scans for `name(...)` patterns
// and emits an Intent per balanced occurrence.
func (FunctionSyntaxParser) Parse(pc ParseContext) ([]Intent, error) {
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		out = append(out, scanFunctionSyntax(b.Text, pc.AvailableTools)...)
	}
	return out, nil
}

// scanFunctionSyntax walks `text` looking for `name(...)` calls. Only
// returns intents where the name resolves to a known tool — otherwise
// every parenthesised English word ("This works (sort of)") would become
// a candidate.
func scanFunctionSyntax(text string, tools []llm.ToolSchema) []Intent {
	knownNames := map[string]string{}
	for _, t := range tools {
		knownNames[t.Name] = t.Name
		// Also accept the short name (after the last dot) for resolution.
		if idx := strings.LastIndexByte(t.Name, '.'); idx >= 0 {
			knownNames[t.Name[idx+1:]] = t.Name
		}
	}

	var out []Intent
	matches := nameOpenRe.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		// m: [outerStart, outerEnd, nameStart, nameEnd]
		name := text[m[2]:m[3]]
		// Filter to known tool names — avoids matching every English
		// "(parenthesis)" the model writes.
		full, ok := knownNames[name]
		if !ok {
			continue
		}
		// Find the matching closing paren by tracking depth + string state.
		bodyStart := m[1]
		bodyEnd, ok := findMatchingParen(text, bodyStart-1)
		if !ok {
			continue
		}
		body := text[bodyStart:bodyEnd]
		args, conf := buildFunctionArgs(body)
		if conf == 0 {
			continue
		}
		out = append(out, Intent{
			ToolName:   full,
			Args:       args,
			Confidence: conf,
			Source:     SourceFunctionSyntax,
			SpanStart:  m[2],
			SpanEnd:    bodyEnd + 1,
			SourceText: shortPreview(text[m[2]:bodyEnd+1], 80),
		})
	}
	return out
}

// findMatchingParen returns the index of the `)` matching the `(` at
// `openIdx`. Tracks single/double quotes and backslash escapes.
func findMatchingParen(s string, openIdx int) (int, bool) {
	if openIdx < 0 || openIdx >= len(s) || s[openIdx] != '(' {
		return 0, false
	}
	depth := 1
	inSingle := false
	inDouble := false
	escaped := false
	for i := openIdx + 1; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		switch {
		case c == '\\':
			escaped = true
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// buildFunctionArgs interprets `body` (the content between parens) as
// either:
//
//   - a single JSON object (preferred)         → confidence 0.80
//   - a kwarg list `key="v", key2=v`           → confidence 0.65
//
// Returns (json, confidence). Confidence == 0 means "couldn't parse,
// don't emit an intent".
func buildFunctionArgs(body string) (json.RawMessage, float64) {
	body = strings.TrimSpace(body)
	if body == "" {
		return json.RawMessage("{}"), 0.80
	}

	// JSON object form.
	if strings.HasPrefix(body, "{") {
		if json.Valid([]byte(body)) {
			return json.RawMessage(body), 0.80
		}
	}

	// Kwarg form.
	pairs := kwargRe.FindAllStringSubmatch(body, -1)
	if len(pairs) == 0 {
		return nil, 0
	}
	m := make(map[string]interface{}, len(pairs))
	for _, p := range pairs {
		// Submatches:
		// p[1] key
		// p[2] double-quoted value (without quotes)
		// p[3] single-quoted value
		// p[4] bare value
		key := strings.TrimSpace(p[1])
		var val interface{}
		switch {
		case p[2] != "":
			val = unescapeStringLit(p[2])
		case p[3] != "":
			val = unescapeStringLit(p[3])
		default:
			bare := strings.TrimSpace(p[4])
			val = parseBareLiteral(bare)
		}
		if key != "" {
			m[key] = val
		}
	}
	if len(m) == 0 {
		return nil, 0
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, 0
	}
	return json.RawMessage(encoded), 0.65
}

// unescapeStringLit converts common backslash escapes inside a string
// literal back to the real characters. We only handle the escapes that
// appear in real LLM output — \n, \t, \r, \\, \" — anything more
// elaborate (\x, \u) the LLM rarely generates and the cost of getting
// it wrong is just a slightly garbled arg, not data loss.
func unescapeStringLit(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			case '"':
				b.WriteByte('"')
				i++
				continue
			case '\'':
				b.WriteByte('\'')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseBareLiteral interprets a bare token from kwarg form as a JSON
// scalar. Supports true / false / null / numbers / fallback string.
func parseBareLiteral(s string) interface{} {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null", "None", "nil":
		return nil
	}
	// Try number.
	var asNum interface{}
	if err := json.Unmarshal([]byte(s), &asNum); err == nil {
		return asNum
	}
	return s
}
