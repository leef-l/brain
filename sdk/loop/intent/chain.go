package intent

import (
	"sort"
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// Chain composes multiple Parser implementations into a single front-end
// that the runner calls when an LLM response carries no native tool_use
// block. The Chain runs every parser, ranks the resulting candidates by
// Confidence, and emits a deduplicated, non-overlapping intent list.
//
// The Chain is the only place where parser results interact — individual
// Parsers stay simple and don't need to know about each other.
//
// # Threshold
//
// Intents whose Confidence < Threshold are dropped on the floor and the
// runner is told "no intent found" so it can escalate to ClarificationLoop
// (Phase 5). The default threshold is 0.6, calibrated from the bands in
// the package doc — anything below 0.6 is speculative enough that
// directly synthesizing a tool_use is more dangerous than asking the
// model to clarify.
//
// # Tool-name normalization
//
// LLMs frequently emit short tool names ("write_file") even though the
// registry only contains namespaced ones ("code.write_file"). Chain.resolve
// performs the lookup and rewrites the Intent in place; an unresolved name
// keeps its original form and the dispatch will surface a "tool not found"
// error like any hallucinated call.
type Chain struct {
	// Parsers is the ordered list of Parser implementations to consult.
	// Order does not affect ranking (Chain ranks by Confidence) but it
	// does affect tie-breaking when two parsers report the same Intent
	// at the same confidence — in that case the earlier parser wins.
	Parsers []Parser

	// Threshold is the minimum Confidence for an Intent to escape the
	// Chain. Defaults to 0.6 when zero.
	Threshold float64

	// MaxParallelTools caps the number of distinct tool calls returned
	// from one LLM response. Defaults to capability.MaxParallelTools or 4.
	MaxParallelTools int
}

// NewDefaultChain constructs a Chain with the five built-in parsers in
// their canonical order. Callers are free to build their own Chain with
// a different parser slice for tests or specialized brains.
//
// The order matches confidence calibration in the package doc:
//
//   1. NativeToolUseParser    (1.00)
//   2. TaggedCodeBlockParser  (0.95)
//   3. JSONCodeBlockParser    (0.90)
//   4. XMLToolParser          (0.85)
//   5. FunctionSyntaxParser   (0.80)
//   6. MarkdownHeuristicParser(0.70)
func NewDefaultChain() *Chain {
	return &Chain{
		Parsers: []Parser{
			&NativeToolUseParser{},
			&TaggedCodeBlockParser{},
			&JSONCodeBlockParser{},
			&XMLToolParser{},
			&FunctionSyntaxParser{},
			&MarkdownHeuristicParser{},
		},
		Threshold: 0.6,
	}
}

// Parse runs every configured Parser, ranks candidates by Confidence,
// dedupes overlapping spans, and returns the surviving Intents.
//
// Behavior contract:
//
//   - Empty Parser list → ([] , nil), no error.
//   - Any parser returning an error is logged via the supplied callback
//     (or skipped silently when nil) and its candidates are still
//     considered if non-empty.
//   - Returned slice is sorted by descending Confidence so callers can
//     truncate the head if they only care about the top intent.
func (c *Chain) Parse(pc ParseContext) []Intent {
	if c == nil || len(c.Parsers) == 0 {
		return nil
	}
	threshold := c.Threshold
	if threshold <= 0 {
		threshold = 0.6
	}
	maxTools := c.MaxParallelTools
	if maxTools <= 0 {
		maxTools = pc.Capability.MaxParallelTools
		if maxTools <= 0 {
			maxTools = 4
		}
	}

	var candidates []Intent
	for _, p := range c.Parsers {
		intents, _ := p.Parse(pc) // errors deliberately ignored; parsers stay defensive
		for i := range intents {
			c.resolveToolName(&intents[i], pc.AvailableTools)
			c.adjustForCapability(&intents[i], pc.Capability)
		}
		candidates = append(candidates, intents...)
	}

	// Filter below threshold first to shrink the dedup workload.
	filtered := candidates[:0]
	for _, ci := range candidates {
		if ci.Confidence < threshold {
			continue
		}
		filtered = append(filtered, ci)
	}

	// Sort by Confidence DESC, ties broken by Source priority then by
	// SpanStart so deterministic output across runs.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Confidence != filtered[j].Confidence {
			return filtered[i].Confidence > filtered[j].Confidence
		}
		if sourceRank(filtered[i].Source) != sourceRank(filtered[j].Source) {
			return sourceRank(filtered[i].Source) < sourceRank(filtered[j].Source)
		}
		return filtered[i].SpanStart < filtered[j].SpanStart
	})

	// Dedup overlapping intents:
	//   - Same (ToolName, Args) → keep highest confidence.
	//   - Overlapping span (one parser's range fully contains another's)
	//     → keep the higher-confidence one to avoid double dispatch.
	survivors := make([]Intent, 0, len(filtered))
	keyTaken := map[string]bool{}
	for _, ci := range filtered {
		key := dedupKey(ci)
		if keyTaken[key] {
			continue
		}
		if overlapsAccepted(ci, survivors) {
			continue
		}
		keyTaken[key] = true
		survivors = append(survivors, ci)
		if len(survivors) >= maxTools {
			break
		}
	}
	return survivors
}

// resolveToolName looks up Intent.ToolName in the available tool list,
// rewriting short names to their fully-qualified form when there is
// exactly one match. Ambiguous matches (multiple brains expose the same
// short name) leave the Intent's name untouched — the dispatch will
// surface "tool not found" for the original typo, which is closer to
// the LLM's actual output and easier to debug.
func (c *Chain) resolveToolName(in *Intent, tools []llm.ToolSchema) {
	if in == nil || in.ToolName == "" {
		return
	}
	// Already fully-qualified.
	for _, t := range tools {
		if t.Name == in.ToolName {
			return
		}
	}
	// Find candidates whose suffix matches.
	var matches []string
	for _, t := range tools {
		if strings.HasSuffix(t.Name, "."+in.ToolName) {
			matches = append(matches, t.Name)
		}
	}
	if len(matches) == 1 {
		in.ToolName = matches[0]
	}
}

// adjustForCapability biases confidence based on the provider profile.
// The current rules are conservative — we only nudge, never rewrite a
// confidence band:
//
//   - Reasoner providers tend to put intent in `text` blocks rather than
//     `thinking`. Intents extracted from thinking blocks of a reasoner
//     get a small confidence penalty so text-block intents win on tie.
//
// (At the moment we don't have enough span/block tracking to apply this
// reliably; the field is reserved for future Phase-6 reasoner work.)
func (c *Chain) adjustForCapability(in *Intent, _ llm.Capabilities) {
	if in == nil {
		return
	}
	// Placeholder for future per-capability tuning. Keep the call site
	// in the chain so tests can verify the intent passes through.
}

// dedupKey produces a stable string for "same intent" comparison.
func dedupKey(i Intent) string {
	var b strings.Builder
	b.WriteString(i.ToolName)
	b.WriteByte('|')
	b.Write(i.Args)
	return b.String()
}

// overlapsAccepted reports whether `cand` shares a textual span with any
// already-accepted intent. Equal spans count as overlap; pure adjacency
// does not. Two intents with both spans = (0,0) (unknown) are NOT treated
// as overlapping — those came from non-text sources where overlap is
// meaningless.
func overlapsAccepted(cand Intent, accepted []Intent) bool {
	if cand.SpanStart == 0 && cand.SpanEnd == 0 {
		return false
	}
	for _, a := range accepted {
		if a.SpanStart == 0 && a.SpanEnd == 0 {
			continue
		}
		if cand.SpanStart < a.SpanEnd && a.SpanStart < cand.SpanEnd {
			return true
		}
	}
	return false
}

// sourceRank gives each Source a tie-breaking priority. Lower wins.
// Aligned with confidence band order from the package doc.
func sourceRank(s Source) int {
	switch s {
	case SourceNative:
		return 0
	case SourceTaggedCodeBlock:
		return 1
	case SourceJSONCodeBlock:
		return 2
	case SourceXMLTool:
		return 3
	case SourceFunctionSyntax:
		return 4
	case SourceMarkdownHeuristic:
		return 5
	default:
		return 99
	}
}
