package intent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// MarkdownHeuristicParser is the lowest-confidence fallback. It catches
// the most common "human-style" reply where the model says it will write
// a file and immediately puts the file's content in the next code fence:
//
//	I'll create game.html with the snake game:
//
//	```html
//	<!DOCTYPE html>
//	<html>...
//	```
//
// We synthesize a `code.write_file` (or `write_file` short name) intent
// with `path` and `content` filled from the heuristic. Confidence sits
// at 0.70 — strong enough to autodispatch in bypass-permissions mode,
// weak enough that the runner's confidence threshold can be raised in
// stricter modes to force clarification.
//
// The parser only runs when the available tool list contains a write_file
// tool — without one, even a perfect match would have no destination.
type MarkdownHeuristicParser struct{}

// Name implements Parser.
func (MarkdownHeuristicParser) Name() string { return "markdown_heuristic" }

var (
	// Captures phrases like "I'll write game.html", "let me create snake.js",
	// "writing a new index.html for you" — the file name MUST contain a
	// dot to disqualify random English words.
	heuristicFileRe = regexp.MustCompile(`(?i)(?:i['']?ll|i will|let me|i'm going to|going to|here'?s the|writing|creating|create|update|writing a|i need to (?:write|create))\s+(?:a\s+|the\s+|new\s+)?([\w][\w\-]*\.[\w][\w]+)`)

	// Filename inside a fence's language tag — Mimo / GLM sometimes
	// write ```game.html ... ``` with the filename as the language.
	filenameLangRe = regexp.MustCompile(`^[\w][\w\-]*\.[\w][\w]+$`)
)

// Parse looks for "I'll write <file>" + a following code fence.
func (MarkdownHeuristicParser) Parse(pc ParseContext) ([]Intent, error) {
	writeFileTool := findWriteFileTool(pc.AvailableTools)
	if writeFileTool == "" {
		return nil, nil
	}
	var out []Intent
	for _, b := range pc.Content {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		out = append(out, scanHeuristic(b.Text, writeFileTool)...)
	}
	return out, nil
}

// scanHeuristic walks one text block: for every announcement match it
// looks for the very next code fence and pairs them. Multiple
// announcements with shared fences are deduped so we don't double-write
// the same file.
func scanHeuristic(text, writeFileTool string) []Intent {
	annMatches := heuristicFileRe.FindAllStringSubmatchIndex(text, -1)
	if len(annMatches) == 0 {
		// Even without an announcement, a fence whose language is a
		// filename is a very common Mimo / GLM pattern. Try that.
		return scanHeuristicByFenceLang(text, writeFileTool)
	}
	fences := extractCodeFences(text)
	var out []Intent
	used := map[int]bool{}
	for _, m := range annMatches {
		// m: [outerStart, outerEnd, fileStart, fileEnd]
		filename := text[m[2]:m[3]]
		annEnd := m[1]
		// Find the first fence starting at or after annEnd.
		idx := -1
		for i, f := range fences {
			if used[i] {
				continue
			}
			if f.SpanStart >= annEnd {
				idx = i
				break
			}
		}
		if idx == -1 {
			continue
		}
		used[idx] = true
		body := fences[idx].Body
		args := encodeWriteFileArgs(filename, body)
		out = append(out, Intent{
			ToolName:   writeFileTool,
			Args:       args,
			Confidence: 0.70,
			Source:     SourceMarkdownHeuristic,
			SpanStart:  m[0],
			SpanEnd:    fences[idx].SpanEnd,
			SourceText: shortPreview(filename+": "+body, 80),
		})
	}
	// Also pick up unused fences whose lang is itself a filename.
	for i, f := range fences {
		if used[i] {
			continue
		}
		if !filenameLangRe.MatchString(f.Language) {
			continue
		}
		args := encodeWriteFileArgs(f.Language, f.Body)
		out = append(out, Intent{
			ToolName:   writeFileTool,
			Args:       args,
			Confidence: 0.70,
			Source:     SourceMarkdownHeuristic,
			SpanStart:  f.SpanStart,
			SpanEnd:    f.SpanEnd,
			SourceText: shortPreview(f.Language+": "+f.Body, 80),
		})
	}
	return out
}

// scanHeuristicByFenceLang catches the GLM / Mimo style:
//
//	```game.html
//	<!DOCTYPE ...>
//	```
//
// Without an explicit "I'll write ..." announcement.
func scanHeuristicByFenceLang(text, writeFileTool string) []Intent {
	var out []Intent
	for _, f := range extractCodeFences(text) {
		if !filenameLangRe.MatchString(f.Language) {
			continue
		}
		args := encodeWriteFileArgs(f.Language, f.Body)
		out = append(out, Intent{
			ToolName:   writeFileTool,
			Args:       args,
			Confidence: 0.70,
			Source:     SourceMarkdownHeuristic,
			SpanStart:  f.SpanStart,
			SpanEnd:    f.SpanEnd,
			SourceText: shortPreview(f.Language+": "+f.Body, 80),
		})
	}
	return out
}

// findWriteFileTool returns the fully-qualified tool name to dispatch
// for a "write file" heuristic. Picks the first match in priority order:
//
//  1. any tool with suffix ".write_file" — the per-brain namespaced form
//     (e.g. "code.write_file").
//  2. exact "write_file" — when the brain registers tools under bare names.
//
// Returns "" when none found, which makes Parse a no-op.
func findWriteFileTool(tools []llm.ToolSchema) string {
	for _, t := range tools {
		if strings.HasSuffix(t.Name, ".write_file") {
			return t.Name
		}
	}
	for _, t := range tools {
		if t.Name == "write_file" {
			return t.Name
		}
	}
	return ""
}

// encodeWriteFileArgs builds the JSON payload for a synthesized write_file
// call. We deliberately stick to {path, content} which is the canonical
// Brain write_file schema (sdk/tool/builtin_writefile.go).
func encodeWriteFileArgs(path, content string) json.RawMessage {
	args := struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{Path: path, Content: content}
	encoded, err := json.Marshal(args)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(encoded)
}
