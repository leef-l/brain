package intent

import (
	"encoding/json"
	"strings"
)

// codeFence is a parsed fenced code block from the LLM output.
//
// We deliberately do not use a Markdown library — the LLM output we
// care about almost always uses three backticks, occasionally tildes,
// and we want zero non-stdlib dependencies (per brain骨架实施计划.md §4.6).
type codeFence struct {
	// Language is the fence info string: ` ```json ` → "json",
	// ` ```tool:write_file ` → "tool:write_file".
	Language string
	// Body is the raw content between the fences (no leading/trailing
	// newlines stripped — JSON parsers don't care about the whitespace).
	Body string
	// SpanStart, SpanEnd are byte offsets into the source text including
	// the fence delimiters themselves, used by the Chain to dedup
	// overlapping intents.
	SpanStart int
	SpanEnd   int
}

// extractCodeFences scans `text` for ` ``` ... ``` ` and ` ~~~ ... ~~~ ` blocks
// and returns them in source order. Unbalanced fences (missing closer)
// are dropped so a half-emitted stream doesn't produce phantom intents.
//
// Closer 必须出现在**行首**(前面是 \n 或位于 text 开头),这是 P2 修复:
// 之前的实现只 strings.Index 找 marker 出现位置,如果 LLM 在 args 里写了
// 三连反引号字面量(例如 `"content":"... ``` ..."`),会误判 body 提前
// 截断,导致 JSON 解析失败 + 整段 fence 丢失。
//
// CommonMark 标准也要求 fence closer 在行首(可前置最多 3 个空格),
// 我们用更严格的"行首"以最大限度避免误命中。
func extractCodeFences(text string) []codeFence {
	if text == "" {
		return nil
	}
	var fences []codeFence
	for i := 0; i < len(text); {
		// Find the next opener.
		open := strings.Index(text[i:], "```")
		altOpen := strings.Index(text[i:], "~~~")
		var openOff int
		var marker string
		switch {
		case open == -1 && altOpen == -1:
			return fences
		case altOpen == -1 || (open != -1 && open < altOpen):
			openOff = i + open
			marker = "```"
		default:
			openOff = i + altOpen
			marker = "~~~"
		}
		// Read language up to newline.
		lineEnd := strings.IndexByte(text[openOff+len(marker):], '\n')
		if lineEnd == -1 {
			return fences
		}
		lang := strings.TrimSpace(text[openOff+len(marker) : openOff+len(marker)+lineEnd])
		bodyStart := openOff + len(marker) + lineEnd + 1
		// Find the matching closer at the start of a line. We loop
		// strings.Index from bodyStart and only accept positions where the
		// preceding byte is '\n' (or position is bodyStart itself). This
		// rejects markers that appear inline within the body (e.g. inside
		// a JSON string value), which would otherwise truncate body
		// prematurely and break parsing.
		closeOff := -1
		searchFrom := bodyStart
		for searchFrom < len(text) {
			rel := strings.Index(text[searchFrom:], marker)
			if rel == -1 {
				break
			}
			candidate := searchFrom + rel
			// Accept if at start of text region OR previous byte is newline.
			if candidate == bodyStart || (candidate > 0 && text[candidate-1] == '\n') {
				closeOff = candidate
				break
			}
			// Skip past this false hit and keep searching.
			searchFrom = candidate + len(marker)
		}
		if closeOff == -1 {
			return fences
		}
		body := text[bodyStart:closeOff]
		fences = append(fences, codeFence{
			Language:  lang,
			Body:      body,
			SpanStart: openOff,
			SpanEnd:   closeOff + len(marker),
		})
		i = closeOff + len(marker)
	}
	return fences
}

// TaggedCodeBlockParser handles fences whose language tag is `tool:<name>`.
// Example LLM output (real Mimo / Qwen behavior under restricted prompts):
//
//	```tool:code.write_file
//	{"path":"game.html","content":"<html>...</html>"}
//	```
//
// Confidence is 0.95 — author intent is explicit (the tag is "tool:") and
// the args are independently JSON-validated.
type TaggedCodeBlockParser struct{}

// Name implements Parser.
func (TaggedCodeBlockParser) Name() string { return "tagged_code_block" }

// Parse extracts every fence whose language tag starts with `tool:` and
// emits an Intent. Invalid JSON bodies fall back to empty `{}` with a
// reduced 0.5 confidence so they get dropped by the default threshold.
func (TaggedCodeBlockParser) Parse(pc ParseContext) ([]Intent, error) {
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		for _, f := range extractCodeFences(b.Text) {
			if !strings.HasPrefix(strings.ToLower(f.Language), "tool:") {
				continue
			}
			toolName := strings.TrimSpace(f.Language[len("tool:"):])
			if toolName == "" {
				continue
			}
			args, ok := normalizeJSONBody(f.Body)
			confidence := 0.95
			if !ok {
				args = json.RawMessage("{}")
				confidence = 0.5
			}
			out = append(out, Intent{
				ToolName:   toolName,
				Args:       args,
				Confidence: confidence,
				Source:     SourceTaggedCodeBlock,
				SpanStart:  f.SpanStart,
				SpanEnd:    f.SpanEnd,
				SourceText: shortPreview(f.Body, 80),
			})
		}
	}
	return out, nil
}

// JSONCodeBlockParser handles plain ```json fences whose body parses as
// `{"tool":"name", "args":{...}}` or the alternative `{"name":..., "arguments":{...}}`
// envelope (the latter mirrors OpenAI's tool_calls.function shape).
//
// Both envelope shapes are common because LLMs that don't emit native
// tool_use often fall back to these conventions when the prompt asks
// for "structured output".
//
// Confidence is 0.90 — the envelope is explicit and JSON-validated, but
// slightly less direct than a `tool:` tag.
type JSONCodeBlockParser struct{}

// Name implements Parser.
func (JSONCodeBlockParser) Name() string { return "json_code_block" }

// jsonEnvelope is one of the recognized JSON intent envelopes.
type jsonEnvelope struct {
	Tool      string          `json:"tool,omitempty"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// Parse iterates over text content blocks, extracts ```json fences, and
// recognizes the supported envelope shapes.
func (JSONCodeBlockParser) Parse(pc ParseContext) ([]Intent, error) {
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		for _, f := range extractCodeFences(b.Text) {
			lang := strings.ToLower(f.Language)
			if lang != "json" && lang != "" {
				continue
			}
			body, ok := normalizeJSONBody(f.Body)
			if !ok {
				continue
			}
			var env jsonEnvelope
			if json.Unmarshal(body, &env) != nil {
				continue
			}
			tool := strings.TrimSpace(env.Tool)
			if tool == "" {
				tool = strings.TrimSpace(env.Name)
			}
			if tool == "" {
				continue
			}
			args := firstNonEmpty(env.Args, env.Arguments, env.Input)
			if len(args) == 0 || !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			out = append(out, Intent{
				ToolName:   tool,
				Args:       args,
				Confidence: 0.90,
				Source:     SourceJSONCodeBlock,
				SpanStart:  f.SpanStart,
				SpanEnd:    f.SpanEnd,
				SourceText: shortPreview(f.Body, 80),
			})
		}
	}
	return out, nil
}

// normalizeJSONBody trims fence-internal whitespace and validates that
// the result is a JSON object/array. Returns (cleaned, true) on success
// or (nil, false) on failure.
func normalizeJSONBody(body string) (json.RawMessage, bool) {
	s := strings.TrimSpace(body)
	if s == "" {
		return nil, false
	}
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	return json.RawMessage(s), true
}

// firstNonEmpty returns the first non-empty json.RawMessage from candidates.
func firstNonEmpty(candidates ...json.RawMessage) json.RawMessage {
	for _, c := range candidates {
		if len(c) > 0 {
			return c
		}
	}
	return nil
}

// shortPreview clips s to maxLen runes for human-readable diagnostics.
func shortPreview(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
