package tool

import (
	"context"
	"fmt"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

// MatchedPattern is the result of a successful pattern match against the
// current page. It includes pre/post condition validation flags.
type MatchedPattern struct {
	Pattern          *UIPattern    `json:"pattern"`
	Score            float64       `json:"score"`
	MatchedVia       string        `json:"matched_via"`
	PreConditionsOK  bool          `json:"pre_conditions_ok"`
	PostConditionsOK bool          `json:"post_conditions_ok,omitempty"`
	PostOutcomes     []PostOutcome `json:"post_outcomes,omitempty"`
}

// PatternMatcher wraps the existing UIPattern library with explicit
// PreConditions and PostConditions validation APIs.
type PatternMatcher struct {
	lib *PatternLibrary
}

// NewPatternMatcher creates a matcher backed by the shared pattern library.
func NewPatternMatcher() (*PatternMatcher, error) {
	lib, err := sharedPatternLib()
	if err != nil {
		return nil, fmt.Errorf("pattern library: %w", err)
	}
	return &PatternMatcher{lib: lib}, nil
}

// NewPatternMatcherWithLib creates a matcher with an explicit library (useful
// for tests).
func NewPatternMatcherWithLib(lib *PatternLibrary) *PatternMatcher {
	return &PatternMatcher{lib: lib}
}

// Match scores patterns against the current page and returns the best
// candidate. PreConditions (AppliesWhen) are evaluated during scoring;
// PostConditions are not executed here because they require the action
// sequence to run first. Use ValidatePostConditions after execution.
func (m *PatternMatcher) Match(ctx context.Context, sess *cdp.BrowserSession, category string) (*MatchedPattern, error) {
	if m.lib == nil {
		return nil, fmt.Errorf("pattern library not available")
	}
	if sess == nil {
		return nil, fmt.Errorf("browser session required")
	}

	candidates, err := MatchPatterns(ctx, sess, m.lib, category)
	if err != nil {
		return nil, fmt.Errorf("match patterns: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	top := candidates[0]
	return &MatchedPattern{
		Pattern:         top.Pattern,
		Score:           top.Score,
		MatchedVia:      top.MatchedVia,
		PreConditionsOK: true, // MatchPatterns already validated AppliesWhen
	}, nil
}

// ValidatePreConditions checks whether a specific pattern's AppliesWhen holds
// on the current page.
func (m *PatternMatcher) ValidatePreConditions(ctx context.Context, sess *cdp.BrowserSession, p *UIPattern) (bool, string, error) {
	if sess == nil {
		return false, "", fmt.Errorf("browser session required")
	}
	pageURL, pageTitle := readPageMeta(ctx, sess)
	pageText := readBodyText(ctx, sess, 20_000)
	ok, reason := evaluateMatch(ctx, sess, &p.AppliesWhen, pageURL, pageTitle, pageText)
	return ok, reason, nil
}

// ValidatePostConditions checks the pattern's PostConditions against the
// current page state. urlBefore should be the URL before the action sequence
// ran.
func (m *PatternMatcher) ValidatePostConditions(ctx context.Context, sess *cdp.BrowserSession, p *UIPattern, urlBefore string) (bool, []PostOutcome, error) {
	if sess == nil {
		return false, nil, fmt.Errorf("browser session required")
	}
	if len(p.PostConditions) == 0 {
		return true, nil, nil
	}

	outcomes := make([]PostOutcome, 0, len(p.PostConditions))
	allOK := true
	for _, pc := range p.PostConditions {
		ok, reason := CheckPostCondition(ctx, sess, &pc, urlBefore)
		outcomes = append(outcomes, PostOutcome{Type: pc.Type, OK: ok, Reason: reason})
		if !ok {
			allOK = false
		}
	}
	return allOK, outcomes, nil
}
