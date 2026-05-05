package intent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/llm"
)

// helper — wrap one or more text blocks into a ParseContext.
func ctxText(text string, tools ...string) ParseContext {
	var schemas []llm.ToolSchema
	for _, t := range tools {
		schemas = append(schemas, llm.ToolSchema{Name: t, InputSchema: json.RawMessage(`{}`)})
	}
	return ParseContext{
		Content: []llm.ContentBlock{{Type: "text", Text: text}},
		AvailableTools: schemas,
	}
}

func TestNativeToolUseParser(t *testing.T) {
	pc := ParseContext{
		Content: []llm.ContentBlock{
			{Type: "text", Text: "I'll write the file."},
			{Type: "tool_use", ToolUseID: "tu_1", ToolName: "code.write_file", Input: json.RawMessage(`{"path":"a.go","content":"x"}`)},
		},
	}
	got, err := NativeToolUseParser{}.Parse(pc)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 intent, got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("ToolName=%q, want code.write_file", got[0].ToolName)
	}
	if got[0].Confidence != 1.0 {
		t.Errorf("Confidence=%v, want 1.0", got[0].Confidence)
	}
	if got[0].Source != SourceNative {
		t.Errorf("Source=%q, want native", got[0].Source)
	}
}

func TestNativeToolUseParser_EmptyInput(t *testing.T) {
	pc := ParseContext{
		Content: []llm.ContentBlock{
			{Type: "tool_use", ToolUseID: "tu_x", ToolName: "code.task_complete"},
		},
	}
	got, _ := NativeToolUseParser{}.Parse(pc)
	if string(got[0].Args) != "{}" {
		t.Errorf("Args=%s, want {}", string(got[0].Args))
	}
}

// ─── Tagged code block ───────────────────────────────────────────────

func TestTaggedCodeBlockParser_Happy(t *testing.T) {
	text := "Sure, here goes:\n\n```tool:code.write_file\n{\"path\":\"game.html\",\"content\":\"x\"}\n```\n"
	got, _ := TaggedCodeBlockParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("expected 1 intent, got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("ToolName=%q", got[0].ToolName)
	}
	if got[0].Confidence != 0.95 {
		t.Errorf("Confidence=%v", got[0].Confidence)
	}
	if got[0].Source != SourceTaggedCodeBlock {
		t.Errorf("Source=%q", got[0].Source)
	}
}

func TestTaggedCodeBlockParser_InvalidJSON(t *testing.T) {
	text := "```tool:code.write_file\nthis is not json\n```"
	got, _ := TaggedCodeBlockParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("expected 1 intent (with low conf), got %d", len(got))
	}
	if got[0].Confidence >= 0.6 {
		t.Errorf("Invalid JSON should drop confidence below threshold, got %v", got[0].Confidence)
	}
}

func TestTaggedCodeBlockParser_NoTag(t *testing.T) {
	// ```python is NOT tool: prefix
	text := "```python\nprint('hi')\n```"
	got, _ := TaggedCodeBlockParser{}.Parse(ctxText(text))
	if len(got) != 0 {
		t.Errorf("expected 0 intent, got %d", len(got))
	}
}

// ─── JSON code block ─────────────────────────────────────────────────

func TestJSONCodeBlockParser_ToolEnvelope(t *testing.T) {
	text := "```json\n{\"tool\":\"code.write_file\",\"args\":{\"path\":\"a\"}}\n```"
	got, _ := JSONCodeBlockParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("name=%q", got[0].ToolName)
	}
	if string(got[0].Args) != `{"path":"a"}` {
		t.Errorf("args=%s", string(got[0].Args))
	}
}

func TestJSONCodeBlockParser_NameArgumentsEnvelope(t *testing.T) {
	text := "```json\n{\"name\":\"code.write_file\",\"arguments\":{\"path\":\"a\"}}\n```"
	got, _ := JSONCodeBlockParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("name=%q", got[0].ToolName)
	}
}

func TestJSONCodeBlockParser_BareJSONNoTool(t *testing.T) {
	text := "```json\n{\"foo\":\"bar\"}\n```"
	got, _ := JSONCodeBlockParser{}.Parse(ctxText(text))
	if len(got) != 0 {
		t.Errorf("expected 0 (no tool key), got %d", len(got))
	}
}

// ─── XML / function_call / invoke ────────────────────────────────────

func TestXMLToolParser_ToolForm(t *testing.T) {
	text := `<tool name="code.write_file"><input>{"path":"a"}</input></tool>`
	got, _ := XMLToolParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("name=%q", got[0].ToolName)
	}
	if got[0].Confidence != 0.85 {
		t.Errorf("conf=%v", got[0].Confidence)
	}
}

func TestXMLToolParser_FunctionCallForm(t *testing.T) {
	text := `<function_call><name>code.write_file</name><arguments>{"path":"a"}</arguments></function_call>`
	got, _ := XMLToolParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("name=%q", got[0].ToolName)
	}
}

func TestXMLToolParser_InvokeForm(t *testing.T) {
	text := `<invoke name="code.write_file"><parameter name="path">a.html</parameter><parameter name="content">x</parameter></invoke>`
	got, _ := XMLToolParser{}.Parse(ctxText(text))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	var args map[string]string
	if err := json.Unmarshal(got[0].Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	if args["path"] != "a.html" || args["content"] != "x" {
		t.Errorf("args mismatch: %v", args)
	}
}

// ─── Function syntax ─────────────────────────────────────────────────

func TestFunctionSyntaxParser_JSONForm(t *testing.T) {
	text := `code.write_file({"path":"a","content":"hi"})`
	got, _ := FunctionSyntaxParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("name=%q", got[0].ToolName)
	}
	if got[0].Confidence != 0.80 {
		t.Errorf("conf=%v", got[0].Confidence)
	}
}

func TestFunctionSyntaxParser_KwargForm(t *testing.T) {
	text := `code.write_file(path="game.html", content="hello world")`
	got, _ := FunctionSyntaxParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Confidence != 0.65 {
		t.Errorf("conf=%v", got[0].Confidence)
	}
	var args map[string]string
	_ = json.Unmarshal(got[0].Args, &args)
	if args["path"] != "game.html" {
		t.Errorf("path=%q", args["path"])
	}
}

func TestFunctionSyntaxParser_RejectsUnknownTool(t *testing.T) {
	// "this(works)" is parens around English — must not become a call
	text := `It works (sort of) but maybe not always.`
	got, _ := FunctionSyntaxParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func TestFunctionSyntaxParser_ShortName(t *testing.T) {
	text := `write_file({"path":"a","content":"x"})`
	got, _ := FunctionSyntaxParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("expected resolved to code.write_file, got %q", got[0].ToolName)
	}
}

// ─── Markdown heuristic ──────────────────────────────────────────────

func TestMarkdownHeuristicParser_AnnouncementPlusFence(t *testing.T) {
	text := "I'll create game.html with the snake game:\n\n```html\n<!DOCTYPE html><html></html>\n```\n"
	got, _ := MarkdownHeuristicParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Confidence != 0.70 {
		t.Errorf("conf=%v", got[0].Confidence)
	}
	var args map[string]string
	_ = json.Unmarshal(got[0].Args, &args)
	if args["path"] != "game.html" {
		t.Errorf("path=%q", args["path"])
	}
	if args["content"] == "" {
		t.Error("content empty")
	}
}

func TestMarkdownHeuristicParser_FilenameAsLanguage(t *testing.T) {
	text := "```game.html\n<!DOCTYPE html>\n```"
	got, _ := MarkdownHeuristicParser{}.Parse(ctxText(text, "code.write_file"))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	var args map[string]string
	_ = json.Unmarshal(got[0].Args, &args)
	if args["path"] != "game.html" {
		t.Errorf("path=%q", args["path"])
	}
}

func TestMarkdownHeuristicParser_NoWriteFileTool(t *testing.T) {
	text := "I'll create game.html:\n\n```html\n<x/>\n```"
	// no write_file tool registered → parser must return nothing
	got, _ := MarkdownHeuristicParser{}.Parse(ctxText(text, "verifier.read_file"))
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

// ─── Chain ────────────────────────────────────────────────────────────

func TestChain_PrefersHigherConfidence(t *testing.T) {
	// Native + heuristic both fire, native wins.
	pc := ParseContext{
		Content: []llm.ContentBlock{
			{Type: "text", Text: "I'll write game.html:\n\n```html\n<x/>\n```\n"},
			{Type: "tool_use", ToolUseID: "tu_1", ToolName: "code.write_file", Input: json.RawMessage(`{"path":"game.html","content":"native"}`)},
		},
		AvailableTools: []llm.ToolSchema{{Name: "code.write_file"}},
	}
	got := NewDefaultChain().Parse(pc)
	if len(got) != 1 {
		t.Fatalf("expected 1 (deduped), got %d", len(got))
	}
	if got[0].Source != SourceNative {
		t.Errorf("Source=%q, want native (highest conf)", got[0].Source)
	}
}

func TestChain_DropsBelowThreshold(t *testing.T) {
	// Tagged code block with invalid JSON falls below 0.6 threshold.
	pc := ctxText("```tool:code.write_file\nNOT JSON\n```", "code.write_file")
	got := NewDefaultChain().Parse(pc)
	if len(got) != 0 {
		t.Errorf("expected 0 (below threshold), got %d", len(got))
	}
}

func TestChain_ResolvesShortName(t *testing.T) {
	pc := ParseContext{
		Content: []llm.ContentBlock{
			{Type: "text", Text: "```tool:write_file\n{\"path\":\"a\",\"content\":\"x\"}\n```"},
		},
		AvailableTools: []llm.ToolSchema{{Name: "code.write_file"}},
	}
	got := NewDefaultChain().Parse(pc)
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ToolName != "code.write_file" {
		t.Errorf("expected resolved to code.write_file, got %q", got[0].ToolName)
	}
}

func TestChain_RespectsMaxParallelTools(t *testing.T) {
	// Three tagged code blocks, but cap is 2.
	text := "```tool:code.write_file\n{\"path\":\"a\",\"content\":\"1\"}\n```\n```tool:code.write_file\n{\"path\":\"b\",\"content\":\"2\"}\n```\n```tool:code.write_file\n{\"path\":\"c\",\"content\":\"3\"}\n```"
	pc := ctxText(text, "code.write_file")
	c := NewDefaultChain()
	c.MaxParallelTools = 2
	got := c.Parse(pc)
	if len(got) != 2 {
		t.Errorf("expected 2 (capped), got %d", len(got))
	}
}

func TestIntent_ToContentBlock(t *testing.T) {
	in := Intent{
		ToolName: "code.write_file",
		Args:     json.RawMessage(`{"path":"a"}`),
		Source:   SourceTaggedCodeBlock,
	}
	cb := in.ToContentBlock()
	if cb.Type != "tool_use" {
		t.Errorf("Type=%q", cb.Type)
	}
	if cb.ToolName != "code.write_file" {
		t.Errorf("ToolName=%q", cb.ToolName)
	}
	if string(cb.Input) != `{"path":"a"}` {
		t.Errorf("Input=%s", string(cb.Input))
	}
	if cb.ToolUseID == "" {
		t.Error("ToolUseID empty")
	}
}

// TestExtractCodeFences_BacktickInArgsBody is the regression for the P2
// fix where extractCodeFences treated any "```" in the body as a closer,
// truncating fences whose JSON args contain a backtick string literal.
// After the fix, the closer must be at the start of a line.
func TestExtractCodeFences_BacktickInArgsBody(t *testing.T) {
	// Realistic LLM output: args has a string value containing the fence
	// marker as text. Old behavior: body truncated at the inline ```,
	// JSON parse fails, intent dropped. New behavior: fence closer must
	// be at line start, so the inline ``` is ignored and the body is
	// captured intact through the real closer.
	text := "Here you go:\n\n" +
		"```json\n" +
		`{"tool":"code.write_file","args":{"path":"x.md","content":"Wrap with ` + "```" + ` for code in markdown"}}` + "\n" +
		"```\n"
	fences := extractCodeFences(text)
	if len(fences) != 1 {
		t.Fatalf("expected 1 fence, got %d", len(fences))
	}
	body := fences[0].Body
	// Body must contain the closing brace of args — proves we didn't
	// truncate at the inline ```.
	if !strings.Contains(body, `"path":"x.md"`) {
		t.Errorf("body lost path field: %q", body)
	}
	if !strings.Contains(body, "code in markdown") {
		t.Errorf("body truncated before end of content value: %q", body)
	}
}

func TestIntent_SyntheticID_Stable(t *testing.T) {
	in1 := Intent{ToolName: "x", Args: []byte(`{"a":1}`), Source: SourceTaggedCodeBlock}
	in2 := Intent{ToolName: "x", Args: []byte(`{"a":1}`), Source: SourceTaggedCodeBlock}
	if in1.SyntheticID() != in2.SyntheticID() {
		t.Error("SyntheticID should be stable for same inputs")
	}
	in3 := Intent{ToolName: "x", Args: []byte(`{"a":2}`), Source: SourceTaggedCodeBlock}
	if in1.SyntheticID() == in3.SyntheticID() {
		t.Error("SyntheticID should differ for different args")
	}
}
