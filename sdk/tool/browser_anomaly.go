package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

// B-8 — Enhanced anomaly detection for browser.check_anomaly.
//
// This file supplements the MVP anomaly detector (builtin_browser_anomaly.go)
// with:
//   - Extended HTTP error detection (all 4xx/5xx)
//   - Missing critical elements (reverse PostCondition lookup)
//   - DOM structure mutation detection
//   - Form field anomalies (required fields disappearing)

// ---------------------------------------------------------------------------
// Extended page-load errors
// ---------------------------------------------------------------------------

// checkPageLoadErrorsExtended scans the netBuf for HTTP 4xx/5xx status codes
// beyond the 401/403/429 already handled by the MVP scanNetworkAnomalies.
func checkPageLoadErrorsExtended(buf *netBuf, sinceTS int64) []Anomaly {
	var out []Anomaly
	if buf == nil {
		return out
	}
	for _, e := range buf.snapshot() {
		if sinceTS > 0 && e.StartedAt < sinceTS {
			continue
		}
		if e.Status < 400 {
			continue
		}
		// Skip codes already covered by MVP detector.
		if e.Status == 401 || e.Status == 403 || e.Status == 429 {
			continue
		}
		sev := SeverityMedium
		switch {
		case e.Status >= 500:
			sev = SeverityBlocker
		case e.Status == 404 || e.Status == 410:
			sev = SeverityLow
		case e.Status >= 400:
			sev = SeverityMedium
		}
		desc := fmt.Sprintf("HTTP %d on %s", e.Status, e.URL)
		if e.StatusText != "" {
			desc = fmt.Sprintf("HTTP %d %s on %s", e.Status, e.StatusText, e.URL)
		}
		out = append(out, Anomaly{
			Type:        AnomalyError,
			Severity:    sev,
			Subtype:     fmt.Sprintf("http_%d", e.Status),
			Description: desc,
			URL:         e.URL,
			HTTPStatus:  e.Status,
			DetectedAt:  e.StartedAt,
			Suggested:   []string{"inspect URL availability", "check request payload or authentication"},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Missing critical elements (reverse PostCondition lookup)
// ---------------------------------------------------------------------------

// checkMissingCriticalElements evaluates the current page against the
// PostConditions of matching patterns. For any pattern whose AppliesWhen
// matches the current URL, we verify that its dom_contains selectors are
// present. Missing selectors are reported as anomalies.
func checkMissingCriticalElements(ctx context.Context, sess *cdp.BrowserSession) []Anomaly {
	var out []Anomaly
	if sess == nil {
		return out
	}
	lib, err := sharedPatternLib()
	if err != nil || lib == nil {
		return out
	}
	pageURL, _ := readPageMeta(ctx, sess)
	patterns := lib.List("")
	checked := make(map[string]bool)
	for _, p := range patterns {
		if p.AppliesWhen.URLPattern != "" {
			matched, _ := urlMatchesPattern(pageURL, p.AppliesWhen.URLPattern)
			if !matched {
				continue
			}
		}
		for _, pc := range p.PostConditions {
			sels := extractSelectors(&pc)
			for _, sel := range sels {
				if checked[sel] {
					continue
				}
				checked[sel] = true
				exists, _ := domSelectorExists(ctx, sess, sel)
				if !exists {
					out = append(out, Anomaly{
						Type:        AnomalyError,
						Severity:    SeverityHigh,
						Subtype:     "missing_critical_element",
						Description: fmt.Sprintf("Pattern %q expects element %q which is missing", p.ID, sel),
						URL:         pageURL,
						DetectedAt:  time.Now().UnixMilli(),
						Suggested:   []string{"re-check page state", "pattern may be stale — consider re-learning"},
					})
				}
			}
		}
	}
	return out
}

// extractSelectors returns CSS selectors from a PostCondition tree.
func extractSelectors(pc *PostCondition) []string {
	var out []string
	if pc == nil {
		return out
	}
	switch pc.Type {
	case "dom_contains":
		if pc.Selector != "" {
			out = append(out, pc.Selector)
		}
	case "any_of":
		for i := range pc.Any {
			out = append(out, extractSelectors(&pc.Any[i])...)
		}
	}
	return out
}

// domSelectorExists checks whether a CSS selector matches any element.
func domSelectorExists(ctx context.Context, sess *cdp.BrowserSession, selector string) (bool, error) {
	js := fmt.Sprintf(`!!document.querySelector(%q)`, selector)
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return false, err
	}
	var exists bool
	_ = json.Unmarshal(result.Result.Value, &exists)
	return exists, nil
}

// urlMatchesPattern performs a simple regex match for pattern URLs.
func urlMatchesPattern(pageURL, pattern string) (bool, error) {
	// Best-effort: compile and match.
	re, err := compileRegexOnce(pattern)
	if err != nil {
		return strings.Contains(pageURL, pattern), nil
	}
	return re.MatchString(pageURL), nil
}

// compileRegexOnce is a tiny regex cache to avoid re-compiling the same patterns.
func compileRegexOnce(pattern string) (*regexp.Regexp, error) {
	// Use a package-level cache if this becomes hot; for anomaly scanning it's fine.
	return regexp.Compile(pattern)
}

// ---------------------------------------------------------------------------
// DOM structure mutation
// ---------------------------------------------------------------------------

// checkDOMMutation compares the current DOM hash against the cached previous
// snapshot and reports a structural mutation anomaly if they differ.
func checkDOMMutation(ctx context.Context, sess *cdp.BrowserSession, hist *anomalyHistory) []Anomaly {
	var out []Anomaly
	if sess == nil || hist == nil {
		return out
	}
	elements, err := collectInteractive(ctx, sess)
	if err != nil {
		return out
	}
	currentHash := domHash(elements)
	pageURL, _ := readPageMeta(ctx, sess)

	hist.mu.Lock()
	prevHash := hist.lastDOMHash
	hist.lastDOMHash = currentHash
	hist.mu.Unlock()

	if prevHash != "" && prevHash != currentHash {
		out = append(out, Anomaly{
			Type:        AnomalyError,
			Severity:    SeverityMedium,
			Subtype:     "dom_structure_mutation",
			Description: fmt.Sprintf("DOM structure changed significantly (hash %s → %s)", prevHash, currentHash),
			URL:         pageURL,
			DetectedAt:  time.Now().UnixMilli(),
			Suggested:   []string{"re-run browser.snapshot to see new layout", "verify expected state transition"},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Form field anomalies
// ---------------------------------------------------------------------------

// checkFormFieldAnomalies detects missing required form fields by comparing
// the current set of required inputs against the cached previous state.
func checkFormFieldAnomalies(ctx context.Context, sess *cdp.BrowserSession, hist *anomalyHistory) []Anomaly {
	var out []Anomaly
	if sess == nil || hist == nil {
		return out
	}
	currentFields, err := collectRequiredFields(ctx, sess)
	if err != nil {
		return out
	}
	pageURL, _ := readPageMeta(ctx, sess)

	hist.mu.Lock()
	prevFields := hist.lastFormFields
	hist.lastFormFields = currentFields
	hist.mu.Unlock()

	if len(prevFields) > 0 {
		missing := stringSliceDiff(prevFields, currentFields)
		for _, f := range missing {
			out = append(out, Anomaly{
				Type:        AnomalyError,
				Severity:    SeverityHigh,
				Subtype:     "required_field_missing",
				Description: fmt.Sprintf("Required form field %q disappeared", f),
				URL:         pageURL,
				DetectedAt:  time.Now().UnixMilli(),
				Suggested:   []string{"re-check form state", "page may have dynamically updated fields"},
			})
		}
	}
	return out
}

// collectRequiredFields returns CSS-like descriptors for currently visible
// required input fields.
func collectRequiredFields(ctx context.Context, sess *cdp.BrowserSession) ([]string, error) {
	js := `(function(){
		var out = [];
		document.querySelectorAll('input[required], textarea[required], select[required]').forEach(function(el){
			var sel = el.tagName.toLowerCase();
			if(el.id) sel += '#' + el.id;
			else if(el.name) sel += '[name="' + el.name + '"]';
			else if(el.className) sel += '.' + el.className.split(' ')[0];
			out.push(sel);
		});
		return JSON.stringify(out);
	})()`
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return nil, err
	}
	var raw string
	if err := json.Unmarshal(result.Result.Value, &raw); err != nil {
		return nil, err
	}
	var fields []string
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func stringSliceDiff(prev, curr []string) []string {
	currSet := make(map[string]bool, len(curr))
	for _, f := range curr {
		currSet[f] = true
	}
	var missing []string
	for _, f := range prev {
		if !currSet[f] {
			missing = append(missing, f)
		}
	}
	return missing
}
