package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

// B-6 / B-7 — Multimodal fallback trigger and joint reasoning.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §6.

const (
	multimodalConfidenceThreshold = 0.6
	complexDOMNodeThreshold       = 5000
	consecutiveFailureThreshold   = 3
)

// ShouldTriggerMultimodal determines whether to fallback to screenshot-based
// understanding according to the B-6 spec.
func ShouldTriggerMultimodal(ctx context.Context, holder *browserSessionHolder, textConfidence float64) bool {
	if textConfidence < multimodalConfidenceThreshold {
		return true
	}
	sess, err := holder.get(ctx)
	if err == nil && sess != nil {
		nodeCount, _ := countDOMNodes(ctx, sess)
		if nodeCount > complexDOMNodeThreshold {
			return true
		}
	}
	holder.mu.Lock()
	failCount := holder.understandFailCount
	holder.mu.Unlock()
	return failCount >= consecutiveFailureThreshold
}

// countDOMNodes returns the total number of DOM nodes via querySelectorAll.
func countDOMNodes(ctx context.Context, sess *cdp.BrowserSession) (int, error) {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    "document.querySelectorAll('*').length",
		"returnByValue": true,
	}, &result); err != nil {
		return 0, err
	}
	var count int
	if err := json.Unmarshal(result.Result.Value, &count); err != nil {
		return 0, err
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// B-7: UnderstandWithScreenshot — joint DOM + screenshot reasoning
// ---------------------------------------------------------------------------

// UnderstandWithScreenshot first attempts a pure-text understand, and if the
// confidence is low or quality uncertain, captures a screenshot and performs
// joint reasoning with the LLM.
func (t *browserUnderstandTool) UnderstandWithScreenshot(ctx context.Context, input understandInput) (*understandResult, error) {
	// 1. Pure-text understanding.
	textRes, err := t.understandPage(ctx, input)
	if err == nil && textRes != nil && textRes.SemanticQuality != "structural_only" && textRes.SemanticQuality != "low_confidence" {
		minConf := 1.0
		for _, el := range textRes.Elements {
			if el.Confidence > 0 && el.Confidence < minConf {
				minConf = el.Confidence
			}
		}
		if minConf >= multimodalConfidenceThreshold {
			return textRes, nil
		}
	}

	// 2. Screenshot capture.
	sess, err := t.holder.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("no browser session: %w", err)
	}

	pageURL, _ := readPageMeta(ctx, sess)
	var shot struct {
		Data string `json:"data"`
	}
	if err := sess.Exec(ctx, "Page.captureScreenshot", map[string]interface{}{
		"format": "png",
	}, &shot); err != nil {
		return nil, fmt.Errorf("screenshot capture failed: %w", err)
	}

	// 3. DOM snapshot.
	elements, _ := collectInteractive(ctx, sess)
	if input.MaxElements <= 0 {
		input.MaxElements = 60
	}
	if len(elements) > input.MaxElements {
		elements = elements[:input.MaxElements]
	}

	// 4. Joint LLM prompt.
	urlPat := urlPattern(pageURL)
	dhash := domHash(elements)

	system := `You are a web UI analyst with visual reasoning. A screenshot of the page is provided as base64 PNG data.
For each interactive element listed below, output JSON with:
- action_intent: one short sentence
- reversibility: one of {reversible, semi_reversible, irreversible, conditional}
- risk_level: one of {safe, safe_caution, destructive, external_effect}
- flow_role: one of {primary, secondary, escape, navigation, cross_page_nav, utility}
- confidence: 0.0-1.0

Output ONLY a JSON array in the same order as input elements. No prose, no code fences.`

	user := buildUnderstandUserPrompt(elements, pageURL)
	truncatedShot := shot.Data
	if len(truncatedShot) > 200 {
		truncatedShot = truncatedShot[:200]
	}
	user += fmt.Sprintf("\n\nScreenshot (base64 PNG, truncated): %s... (total %d chars)\n", truncatedShot, len(shot.Data))

	// 5. LLM call.
	if t.llm == nil {
		return nil, fmt.Errorf("llm backend not configured for multimodal reasoning")
	}
	raw, err := t.llm.Complete(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("llm joint reasoning failed: %w", err)
	}

	// 6. Parse response (same logic as batchAnnotateViaLLM).
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
		return nil, fmt.Errorf("parse joint reasoning response: %w", err)
	}

	out := make([]understoodElement, 0, len(parsed))
	for i, p := range parsed {
		if i >= len(elements) {
			break
		}
		el := elements[i]
		out = append(out, understoodElement{
			ID:            el.ID,
			Tag:           el.Tag,
			Role:          el.Role,
			Name:          el.Name,
			ActionIntent:  p.ActionIntent,
			Reversibility: p.Reversibility,
			RiskLevel:     p.RiskLevel,
			FlowRole:      p.FlowRole,
			Source:        "llm_multimodal",
			Confidence:    p.Confidence,
		})
	}

	quality := "full"
	if len(out) == 0 {
		quality = "structural_only"
	}

	sourceStats := map[string]int{"cache": 0, "rules": 0, "llm": 0, "fallback": 0, "multimodal": len(out)}
	if len(out) > 0 {
		sourceStats["llm"] = len(out)
	}

	return &understandResult{
		URLPattern:      urlPat,
		DOMHash:         dhash,
		SemanticQuality: quality,
		SourceStats:     sourceStats,
		Elements:        out,
	}, nil
}
