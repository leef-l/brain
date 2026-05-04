// Package intent provides multi-format LLM intent parsing for the Agent Loop.
//
// # Why this package exists
//
// The Agent Loop spec (22-Agent-Loop规格.md §6) assumes the LLM emits
// `tool_use` content blocks in the provider-specific native format
// (Anthropic content blocks / OpenAI tool_calls). This works on the
// flagship native-tool-calling models (Claude, GPT-4) where training and
// `tool_choice=required` make tool_use reliable.
//
// In production we also support DeepSeek-V4, Mimo, Qwen, and other
// open-/Chinese-vendor providers where:
//
//   - `tool_choice=required` is silently ignored or returns HTTP 400, so
//     the runner cannot coerce the model into emitting native tool_use.
//   - Reasoner-class models (deepseek-r, mimo, qwen-r) frequently spend
//     the first turn in pure thinking / plain text, even when the
//     prompt explicitly requests tool_use.
//   - Some models prefer Markdown / XML / function-call-syntax wrappers
//     over native tool_use blocks even when both are technically supported.
//
// Without this package the runner is forced into a brittle nudge loop:
// "your last reply had no tool_use, please try again", which blows the
// wallclock budget and irritates the model. With this package, the
// runner can extract tool intent from *any* of the formats LLMs naturally
// produce and synthesize a `tool_use` ContentBlock, keeping the rest of
// the loop blind to the original wire format.
//
// # Design
//
// IntentParser is a composable Chain of single-format Parser implementations.
// Each Parser inspects the LLM response (`[]llm.ContentBlock`), the running
// run state, and the available tool registry, and emits zero or more
// candidate Intents. The Chain ranks all candidates by Confidence (0–1),
// dedupes within the same source, and returns the best non-overlapping set.
//
// All five default parsers (NativeToolUse, JSONCodeBlock, XMLTool,
// FunctionSyntax, MarkdownHeuristic) are stdlib-only and stateless — safe
// to share across runs and goroutines.
//
// # Confidence calibration
//
// Confidence is a directional signal, not a probability. Parsers SHOULD
// follow these rough bands so the Chain can compare them:
//
//   1.00  Native provider tool_use block (already structured; trusted).
//   0.95  ```tool:<name>\n{json}``` fenced code block — explicit author
//         intent + parseable args.
//   0.90  ```json {"tool":...,"args":...}``` fenced JSON, validated against
//         the available tool schema.
//   0.85  <tool name="..."><input>...</input></tool> XML form.
//   0.80  Bare `name({json})` function-syntax with valid JSON args.
//   0.70  Markdown heuristic — text declares "I'll write game.html" and
//         the next code block matches the file content.
//   <0.6  Speculative / ambiguous — runner SHOULD escalate to clarification
//         instead of synthesizing a tool_use directly.
//
// # Wire contract
//
// Once a parser produces an Intent with Confidence ≥ Chain.Threshold, the
// runner converts it into an `llm.ContentBlock{Type:"tool_use",...}` via
// Intent.ToContentBlock and feeds it through the existing dispatch path.
// Nothing else in the loop changes — sanitizer, loop detector,
// task_complete handling, checkpoints all operate on the synthesized
// block exactly as if the LLM had emitted it natively.
package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/llm"
)

// Source identifies which parser produced an Intent. Used for telemetry,
// debugging, and Chain dedup logic.
type Source string

const (
	// SourceNative — the LLM emitted a real ContentBlock{Type:"tool_use"}.
	// Highest confidence; effectively a passthrough.
	SourceNative Source = "native"

	// SourceJSONCodeBlock — the LLM wrote ```json ... ``` containing
	// a {"tool":...,"args":...} envelope. High confidence.
	SourceJSONCodeBlock Source = "json_code_block"

	// SourceTaggedCodeBlock — the LLM wrote ```tool:<name>\n<json>```
	// or similar tagged fence with explicit tool name. High confidence.
	SourceTaggedCodeBlock Source = "tagged_code_block"

	// SourceXMLTool — the LLM wrapped the call in <tool>/<function_call>
	// XML-style tags. Common with Qwen / Claude post-tuned models.
	SourceXMLTool Source = "xml_tool"

	// SourceFunctionSyntax — the LLM wrote a bare `name({json})` or
	// `name(arg1=v, arg2=v)` invocation. Medium confidence.
	SourceFunctionSyntax Source = "function_syntax"

	// SourceMarkdownHeuristic — text declared an action ("I'll write X")
	// and a following code block contained the payload. Lower confidence;
	// requires runner to be cautious.
	SourceMarkdownHeuristic Source = "markdown_heuristic"
)

// Intent describes a single tool invocation extracted from an LLM response.
type Intent struct {
	// ToolName matches a Tool.Schema().Name value. The Chain MAY normalize
	// short / aliased names (e.g. "write_file" → "code.write_file") via
	// the supplied tool registry; parsers themselves SHOULD emit the name
	// the LLM actually wrote and let the Chain do the resolution.
	ToolName string

	// Args is the JSON-encoded argument payload. MUST be valid JSON when
	// Confidence ≥ 0.6 — parsers that cannot reach a valid JSON payload
	// SHOULD lower the confidence below 0.6 and let the runner escalate
	// to clarification.
	Args json.RawMessage

	// Confidence is the parser's belief in [0, 1] that the user-visible
	// response actually contains this tool call. See package doc for the
	// recommended calibration bands.
	Confidence float64

	// Source identifies which parser produced this intent. Used by Chain
	// for dedup and by the runner for telemetry / clarification messages.
	Source Source

	// SpanStart, SpanEnd are byte offsets into the originating text
	// content block, used by Chain to detect overlapping intents that
	// would double-dispatch the same tool call. Zero values mean
	// "unknown span" (parsers without textual provenance).
	SpanStart int
	SpanEnd   int

	// SourceText is a short excerpt of the original text the parser
	// matched. Used in clarification messages and debug logs. Optional.
	SourceText string
}

// ToContentBlock synthesizes a Brain-internal `tool_use` ContentBlock that
// the runner can feed into the dispatch loop. The synthetic ToolUseID is
// a stable hash of (source, tool, args) so loop detection still works.
func (i *Intent) ToContentBlock() llm.ContentBlock {
	args := i.Args
	if len(args) == 0 || !json.Valid(args) {
		args = json.RawMessage("{}")
	}
	id := i.SyntheticID()
	return llm.ContentBlock{
		Type:      "tool_use",
		ToolUseID: id,
		ToolName:  i.ToolName,
		Input:     args,
	}
}

// SyntheticID returns a stable, content-derived ID for a synthesized
// tool_use block. Collisions are statistically negligible and the runner's
// loop detector keys on (toolname + args) anyway.
func (i *Intent) SyntheticID() string {
	h := sha256.New()
	h.Write([]byte(i.Source))
	h.Write([]byte("|"))
	h.Write([]byte(i.ToolName))
	h.Write([]byte("|"))
	h.Write(i.Args)
	sum := h.Sum(nil)
	return fmt.Sprintf("intent_%s", hex.EncodeToString(sum[:8]))
}

// ParseContext bundles the inputs every Parser needs. We pass it as a
// single struct so adding new fields (e.g. provider capabilities) does
// not break existing parser signatures.
type ParseContext struct {
	// Content is the LLM response's content blocks, in original order.
	Content []llm.ContentBlock

	// AvailableTools is the list of tools currently registered for this
	// turn (from RunOptions.Tools). Parsers MAY use it to:
	//   - Resolve short names ("write_file" → "code.write_file")
	//   - Validate the parsed Args against InputSchema (boost confidence)
	//   - Reject tools that are not in the registry (avoid hallucinations)
	AvailableTools []llm.ToolSchema

	// Capability is the provider's reported capability profile. Parsers
	// can use it to bias confidence — e.g. for Reasoner providers we
	// trust thinking-block intents less than text-block intents.
	Capability llm.Capabilities
}

// Parser extracts zero or more candidate Intents from an LLM response.
// Implementations MUST be:
//
//   - Stateless / safe for concurrent use across runs (no internal
//     state survives a Parse call).
//   - Stdlib-only (per brain骨架实施计划.md §4.6).
//   - Defensive — never panic on malformed LLM output.
//
// A Parser that finds no intent SHOULD return (nil, nil), not an error.
// Errors are reserved for genuinely broken inputs (e.g. JSON containing
// an unsupported escape) where the caller MAY want to log or retry.
type Parser interface {
	// Name returns a stable identifier for telemetry / debug logs.
	Name() string

	// Parse inspects pc.Content and returns any candidate Intents found.
	// Implementations MUST set Source on each emitted Intent.
	Parse(pc ParseContext) ([]Intent, error)
}
