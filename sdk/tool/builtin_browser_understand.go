package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// browser.understand — L4-L7 semantic annotation of snapshot elements.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.2 (Phase 1).
//
// For each interactive element:
//   - action_intent  (L4)
//   - reversibility  (L6, from Phase 0 experiment)
//   - risk_level     (L7)
//   - flow_role      (L5, loose)
//
// Source precedence (cheapest first):
//   1. SQLite cache lookup by (url_pattern, dom_hash, element_key)
//   2. Static DOM rules (semantic_rules.go) — covers ~40% based on Phase 0 analysis
//   3. LLM batch annotation for remaining elements — ~60%
//
// Phase 0 data informed this design: cheap models hit 82-85% on snapshot-only
// input, so batch + cache is economical. Confidence field is NOT used as a
// quality gate (Phase 0 showed calibration is unreliable) — we rely on
// `quality` = {full, structural_only, low_confidence} set by the source layer.

// LLMBackend is the minimal interface browser.understand needs to call an LLM.
// It is an interface (not a concrete type) so tests can inject stubs without
// pulling in the Anthropic SDK, and so different providers (Anthropic/OpenAI
// compatible) can back it.
type LLMBackend interface {
	// Complete sends a system + user prompt and returns the raw text response.
	// Callers are responsible for parsing JSON from the response.
	Complete(ctx context.Context, system, user string) (string, error)
}

// NoLLMBackend returns structural-only annotations. Used when no LLM is
// available (tests, offline mode, or the user explicitly opts out).
type NoLLMBackend struct{}

func (NoLLMBackend) Complete(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("llm backend not configured")
}

// browserUnderstandTool implements browser.understand.
type browserUnderstandTool struct {
	holder  *browserSessionHolder
	cacheMu sync.Mutex
	cache   *SemanticCache
	llm     LLMBackend
}

func (t *browserUnderstandTool) Name() string { return "browser.understand" }
func (t *browserUnderstandTool) Risk() Risk   { return RiskSafe }

func (t *browserUnderstandTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Annotate each interactive element with semantic L4-L7 labels:
  - action_intent:   what the user wants to achieve by interacting
  - reversibility:   reversible / semi_reversible / irreversible / conditional
  - risk_level:      safe / safe_caution / destructive / external_effect
  - flow_role:       primary / secondary / escape / navigation / cross_page_nav / utility

This is the Phase 1 implementation: static DOM rules cover well-known patterns,
LLM batch annotates the rest, all results cached in ~/.brain/browser_semantics.db.

When to use:
  - After browser.snapshot on a new page to make informed action decisions
  - Before destructive actions (helps identify which buttons are 'destructive')

When NOT to use:
  - Every snapshot — results cache per page template, so call once per new page type
  - Pure read tasks — if you're just extracting text, snapshot is enough`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "element_ids":    { "type": "array", "items": { "type": "integer" }, "description": "Specific data-brain-id values to annotate. If omitted, annotate all visible interactive elements from a fresh snapshot." },
    "force_refresh":  { "type": "boolean", "description": "Bypass cache and re-annotate (default: false)" },
    "skip_llm":       { "type": "boolean", "description": "Use static rules only, skip LLM for uncovered elements" },
    "max_elements":   { "type": "integer", "description": "Upper bound when element_ids is empty (default 60)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url_pattern":      { "type": "string" },
    "dom_hash":         { "type": "string" },
    "semantic_quality": { "type": "string", "description": "full | structural_only | low_confidence" },
    "source_stats":     { "type": "object" },
    "elements":         { "type": "array" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

type understandInput struct {
	ElementIDs   []int `json:"element_ids"`
	ForceRefresh bool  `json:"force_refresh"`
	SkipLLM      bool  `json:"skip_llm"`
	MaxElements  int   `json:"max_elements"`
}

// understoodElement is what the tool returns per element.
type understoodElement struct {
	ID            int     `json:"id"`
	Tag           string  `json:"tag"`
	Role          string  `json:"role"`
	Name          string  `json:"name"`
	ActionIntent  string  `json:"action_intent"`
	Reversibility string  `json:"reversibility"`
	RiskLevel     string  `json:"risk_level"`
	FlowRole      string  `json:"flow_role"`
	Source        string  `json:"source"`
	Confidence    float64 `json:"confidence,omitempty"`
}

// understandResult is the structured output of understandPage.
type understandResult struct {
	URLPattern      string              `json:"url_pattern"`
	DOMHash         string              `json:"dom_hash"`
	SemanticQuality string              `json:"semantic_quality"`
	SourceStats     map[string]int      `json:"source_stats"`
	Elements        []understoodElement `json:"elements"`
}

func (t *browserUnderstandTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input understandInput
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.MaxElements <= 0 {
		input.MaxElements = 60
	}

	res, err := t.understandPage(ctx, input)
	if err != nil {
		return errResult("%v", err), nil
	}

	return okResult(map[string]interface{}{
		"url_pattern":      res.URLPattern,
		"dom_hash":         res.DOMHash,
		"semantic_quality": res.SemanticQuality,
		"source_stats":     res.SourceStats,
		"elements":         res.Elements,
	}), nil
}

// understandPage analyses the current page snapshot and returns semantic
// annotations for each interactive element. It implements the three-tier
// source precedence: cache → static DOM rules → LLM batch.
func (t *browserUnderstandTool) understandPage(ctx context.Context, input understandInput) (*understandResult, error) {
	sess, err := t.holder.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("no browser session: %w", err)
	}

	// 1. Fetch interactive snapshot from the page.
	allElements, err := collectInteractive(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	pageURL, _ := readPageMeta(ctx, sess)

	// 2. Filter to requested IDs (or all).
	var selected []brainElement
	if len(input.ElementIDs) > 0 {
		want := make(map[int]bool, len(input.ElementIDs))
		for _, id := range input.ElementIDs {
			want[id] = true
		}
		for _, el := range allElements {
			if want[el.ID] {
				selected = append(selected, el)
			}
		}
	} else {
		selected = allElements
		if len(selected) > input.MaxElements {
			selected = selected[:input.MaxElements]
		}
	}
	if len(selected) == 0 {
		return &understandResult{
			URLPattern:      urlPattern(pageURL),
			DOMHash:         domHash(allElements),
			SemanticQuality: "full",
			Elements:        []understoodElement{},
			SourceStats:     map[string]int{"cache": 0, "rules": 0, "llm": 0, "fallback": 0},
		}, nil
	}

	// 3. Cache lookup bulk.
	urlPat := urlPattern(pageURL)
	dhash := domHash(allElements)
	cache := t.ensureCache()

	keyToEl := make(map[string]brainElement, len(selected))
	keys := make([]string, 0, len(selected))
	for _, el := range selected {
		k := elementKey(el)
		keyToEl[k] = el
		keys = append(keys, k)
	}

	stats := map[string]int{"cache": 0, "rules": 0, "llm": 0, "fallback": 0}
	results := make(map[string]*SemanticEntry, len(selected))

	if !input.ForceRefresh && cache != nil {
		hits, _ := cache.Lookup(ctx, urlPat, dhash, keys)
		for k, e := range hits {
			results[k] = e
			stats["cache"]++
		}
	}

	// 4. Static rules for cache misses.
	needLLM := make([]brainElement, 0)
	for _, k := range keys {
		if _, ok := results[k]; ok {
			continue
		}
		el := keyToEl[k]
		if entry := applyStaticRules(el, pageURL); entry != nil {
			entry.URLPattern = urlPat
			entry.DOMHash = dhash
			entry.ElementKey = k
			results[k] = entry
			stats["rules"]++
			continue
		}
		needLLM = append(needLLM, el)
	}

	// 5. LLM batch (if allowed and available).
	newFromLLM := []*SemanticEntry{}
	if len(needLLM) > 0 && !input.SkipLLM && t.llm != nil {
		llmEntries, llmErr := t.batchAnnotateViaLLM(ctx, needLLM, pageURL)
		if llmErr == nil {
			for i, el := range needLLM {
				if i >= len(llmEntries) || llmEntries[i] == nil {
					continue
				}
				k := elementKey(el)
				llmEntries[i].URLPattern = urlPat
				llmEntries[i].DOMHash = dhash
				llmEntries[i].ElementKey = k
				llmEntries[i].Source = "llm"
				llmEntries[i].Quality = "full"
				results[k] = llmEntries[i]
				newFromLLM = append(newFromLLM, llmEntries[i])
				stats["llm"]++
			}
		} else {
			// LLM unavailable or failed — fall back to structural stubs so the
			// caller still gets *something* per element.
			fmt.Fprintf(os.Stderr, "  [understand] LLM batch failed: %v\n", llmErr)
		}
	}

	// 6. Fallback structural_only for still-missing (LLM skipped or failed).
	for _, k := range keys {
		if _, ok := results[k]; ok {
			continue
		}
		el := keyToEl[k]
		results[k] = &SemanticEntry{
			URLPattern:    urlPat,
			DOMHash:       dhash,
			ElementKey:    k,
			ActionIntent:  fmt.Sprintf("Interact with %s element", firstNonEmpty(el.Name, el.Tag)),
			Reversibility: "reversible",
			RiskLevel:     "safe",
			FlowRole:      "secondary",
			Source:        "fallback",
			Quality:       "structural_only",
			Confidence:    0.3,
		}
		stats["fallback"]++
	}

	// 7. Persist new rules+LLM entries to cache.
	if cache != nil {
		toSave := []*SemanticEntry{}
		for _, k := range keys {
			e := results[k]
			if e != nil && (e.Source == "rules" || e.Source == "llm") {
				toSave = append(toSave, e)
			}
		}
		_ = cache.BatchUpsert(ctx, toSave)
	}

	// 8. Shape output.
	out := make([]understoodElement, 0, len(selected))
	for _, el := range selected {
		e := results[elementKey(el)]
		if e == nil {
			continue
		}
		out = append(out, understoodElement{
			ID:            el.ID,
			Tag:           el.Tag,
			Role:          el.Role,
			Name:          el.Name,
			ActionIntent:  e.ActionIntent,
			Reversibility: e.Reversibility,
			RiskLevel:     e.RiskLevel,
			FlowRole:      e.FlowRole,
			Source:        e.Source,
			Confidence:    e.Confidence,
		})
	}

	quality := "full"
	if stats["fallback"] > 0 {
		if stats["fallback"] >= len(selected)/2 {
			quality = "structural_only"
		} else {
			quality = "low_confidence"
		}
	}

	// B-6: track consecutive text understanding failures.
	t.holder.mu.Lock()
	if quality == "structural_only" || quality == "low_confidence" {
		t.holder.understandFailCount++
	} else {
		t.holder.understandFailCount = 0
	}
	t.holder.mu.Unlock()

	return &understandResult{
		URLPattern:      urlPat,
		DOMHash:         dhash,
		SemanticQuality: quality,
		SourceStats:     stats,
		Elements:        out,
	}, nil
}

// ensureCache lazily opens the SQLite cache.
func (t *browserUnderstandTool) ensureCache() *SemanticCache {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if t.cache != nil {
		return t.cache
	}
	c, err := NewSemanticCache("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  [understand] cache unavailable: %v\n", err)
		return nil
	}
	t.cache = c
	return t.cache
}

// batchAnnotateViaLLM asks the configured backend to annotate elements.
// See Phase 0 system prompt template for exact format.
func (t *browserUnderstandTool) batchAnnotateViaLLM(ctx context.Context, elements []brainElement, pageURL string) ([]*SemanticEntry, error) {
	system := `You are a web UI analyst. For each interactive element, output JSON with:
- action_intent: one short sentence, what the user wants to achieve by interacting
- reversibility: one of {reversible, semi_reversible, irreversible, conditional}
- risk_level: one of {safe, safe_caution, destructive, external_effect}
- flow_role: one of {primary, secondary, escape, navigation, cross_page_nav, utility}

Definitions:
- reversibility: reversible=no persistent side-effect; semi_reversible=recoverable (add-to-cart);
  irreversible=persistent (place order, delete); conditional=depends on next step
- risk_level: safe=local/none; safe_caution=submit/login; destructive=delete/pay/send-unrecallable;
  external_effect=emails/SMS/3rd-party
- flow_role: primary=main action; secondary=alternative; escape=cancel/forgot; navigation=in-page tab;
  cross_page_nav=leaves page; utility=helper

Output ONLY a JSON array in the same order as input. No prose, no code fences.`

	user := buildUnderstandUserPrompt(elements, pageURL)

	raw, err := t.llm.Complete(ctx, system, user)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("no JSON array in LLM response: %s", clip(raw, 200))
	}
	var parsed []struct {
		ActionIntent  string  `json:"action_intent"`
		Reversibility string  `json:"reversibility"`
		RiskLevel     string  `json:"risk_level"`
		FlowRole      string  `json:"flow_role"`
		Confidence    float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := make([]*SemanticEntry, len(parsed))
	for i, p := range parsed {
		out[i] = &SemanticEntry{
			ActionIntent:  p.ActionIntent,
			Reversibility: p.Reversibility,
			RiskLevel:     p.RiskLevel,
			FlowRole:      p.FlowRole,
			Confidence:    p.Confidence,
		}
	}
	return out, nil
}

func buildUnderstandUserPrompt(elements []brainElement, pageURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Page URL: %s\nElements (same order in output array):\n\n", pageURL)
	for i, el := range elements {
		fmt.Fprintf(&sb, "[%d] tag=%s role=%s type=%s name=%q href=%s\n",
			i, el.Tag, el.Role, el.Type, el.Name, el.Href)
	}
	return sb.String()
}
