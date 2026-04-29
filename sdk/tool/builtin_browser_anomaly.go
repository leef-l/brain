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

// browser.check_anomaly — detect page-level anomalies that the Agent should
// react to: modal dialogs, error banners, session expiration, CAPTCHA, rate
// limiting, blank/crashed pages.
//
// See sdk/docs/42-Browser-Brain异常感知层设计.md.
//
// Two modes:
//   - passive (default): read the in-memory detector state that's been
//     continuously updated by MutationObserver + netbuf. O(1).
//   - active: re-scan the page DOM right now with a JS injection. Costlier
//     but useful when the Agent suspects something is wrong.
//
// The tool returns a structured AnomalyReport that the loop hook can inject
// into the next turn's tool_result without requiring the Agent to ask.

// AnomalyType is the classified kind of anomaly detected.
type AnomalyType string

const (
	AnomalyModal       AnomalyType = "modal_blocking"
	AnomalyError       AnomalyType = "error_message"
	AnomalySession     AnomalyType = "session_expired"
	AnomalyCaptcha     AnomalyType = "captcha"
	AnomalyRateLimit   AnomalyType = "rate_limited"
	AnomalyBlank       AnomalyType = "blank_page"
	AnomalyJSError     AnomalyType = "javascript_error"
	AnomalyUIInjection AnomalyType = "ui_injection"
)

// AnomalySeverity is the priority level. Used by the main loop to decide
// whether to force-inject the report into the next turn.
type AnomalySeverity string

const (
	SeverityInfo    AnomalySeverity = "info"
	SeverityLow     AnomalySeverity = "low"
	SeverityMedium  AnomalySeverity = "medium"
	SeverityHigh    AnomalySeverity = "high"
	SeverityBlocker AnomalySeverity = "blocker"
)

// Anomaly is one detected anomaly event.
type Anomaly struct {
	Type           AnomalyType     `json:"type"`
	Severity       AnomalySeverity `json:"severity"`
	Subtype        string          `json:"subtype,omitempty"`        // e.g. "cloudflare_turnstile"
	Description    string          `json:"description"`
	ElementID      int             `json:"element_id,omitempty"`     // data-brain-id if applicable
	Text           string          `json:"text,omitempty"`           // extracted alert/error text
	URL            string          `json:"url,omitempty"`            // for network-derived anomalies
	HTTPStatus     int             `json:"http_status,omitempty"`
	DetectedAt     int64           `json:"detected_at"`              // unix millis
	Suggested      []string        `json:"suggested_actions,omitempty"`
	AutoResolvable *bool           `json:"auto_resolvable,omitempty"` // nil = unknown, false = needs human
}

// AnomalyReport is the output of check_anomaly.
type AnomalyReport struct {
	Anomalies   []Anomaly `json:"anomalies"`
	PageHealth  string    `json:"page_health"`   // healthy | degraded | blocked
	CheckedAt   int64     `json:"checked_at"`
	Mode        string    `json:"mode"`
	NextCheckMs int       `json:"next_check_hint_ms,omitempty"`
}

// anomalyJS detects 6 anomaly types in one injected pass.
// Designed for sync DOM-walk (fast, no MutationObserver on this pass).
const anomalyJS = `
(function(){
  var out = [];
  var now = Date.now();

  // 1. Modal / Dialog blocking
  document.querySelectorAll('[role="dialog"],[role="alertdialog"]').forEach(function(el){
    var r = el.getBoundingClientRect();
    if(r.width > 0 && r.height > 0){
      var txt = (el.innerText || '').trim().slice(0, 400);
      var subtype = '';
      var tlo = txt.toLowerCase();
      if(/session|expired|log.?in.*again|unauthorized/i.test(txt)) subtype = 'session_expired';
      else if(/cookie|gdpr|consent/i.test(txt)) subtype = 'cookie_consent';
      else if(/confirm|are you sure|delete\?/i.test(txt)) subtype = 'confirmation';
      out.push({type:'modal_blocking', subtype:subtype, text:txt, bbox:[r.x,r.y,r.width,r.height]});
    }
  });

  // 2. Error banners / toasts
  var alertSel = '[role="alert"],[aria-live="assertive"],.alert-danger,.alert-error,.toast-error,.notification-error,.MuiAlert-filledError,.ant-message-error';
  document.querySelectorAll(alertSel).forEach(function(el){
    var r = el.getBoundingClientRect();
    if(r.width > 0 && r.height > 0){
      var txt = (el.innerText || '').trim().slice(0, 300);
      if(txt){ out.push({type:'error_message', text:txt, bbox:[r.x,r.y,r.width,r.height]}); }
    }
  });

  // Keyword-based error detection (fallback when role=alert missing)
  var bodyText = (document.body ? (document.body.innerText || '') : '').slice(0, 20000);
  var errorKeywords = /\b(error|failed|invalid|incorrect|denied|refused|rejected|unauthorized|forbidden|not found|错误|失败|无效|拒绝)\b/i;
  if(errorKeywords.test(bodyText)){
    // Already captured via [role=alert]? If not, note as soft signal.
    if(out.filter(function(x){return x.type==='error_message'}).length === 0){
      var m = bodyText.match(/([^.!?\n]*(?:error|failed|invalid|incorrect|denied|refused|rejected|unauthorized|forbidden|not found|错误|失败|无效|拒绝)[^.!?\n]*)/i);
      if(m){ out.push({type:'error_message', subtype:'keyword', text: m[1].trim().slice(0, 200)}); }
    }
  }

  // 3. CAPTCHA detection
  var captchaChecks = [
    {sel:'iframe[src*="recaptcha"]',     subtype:'recaptcha'},
    {sel:'iframe[src*="hcaptcha"]',       subtype:'hcaptcha'},
    {sel:'iframe[src*="challenges.cloudflare"]', subtype:'cloudflare_turnstile'},
    {sel:'iframe[src*="geetest"]',        subtype:'geetest'},
    {sel:'iframe[src*="datadome"]',       subtype:'datadome'},
    {sel:'.g-recaptcha',                  subtype:'recaptcha'},
    {sel:'.h-captcha',                    subtype:'hcaptcha'},
    {sel:'.cf-turnstile',                 subtype:'cloudflare_turnstile'}
  ];
  captchaChecks.forEach(function(c){
    if(document.querySelector(c.sel)){
      out.push({type:'captcha', subtype:c.subtype});
    }
  });
  // Cloudflare "Just a moment..." interstitial
  if(/Just a moment|Checking your browser|请稍候|Please wait/i.test(document.title)){
    out.push({type:'captcha', subtype:'cloudflare_interstitial', text:document.title});
  }

  // 4. Blank page / JS error
  var bodyChildren = document.body ? document.body.childElementCount : 0;
  var readyState = document.readyState;
  var hasMainContent = document.querySelector('main,article,section,h1,h2,#content,#main,#root,#app');
  if(bodyChildren < 2 && !hasMainContent && document.body && (document.body.innerText||'').trim().length < 50){
    out.push({type:'blank_page', text:'body has only '+bodyChildren+' children, no visible content'});
  }

  // 5. Session-expired signals in URL / forms
  if(/\/(login|signin|auth|account\/login)/i.test(location.pathname)){
    var hasLoginForm = document.querySelector('input[type="password"]') !== null;
    if(hasLoginForm){
      // Only flag if we landed here unexpectedly (heuristic: no prior login button pressed)
      out.push({type:'session_expired', subtype:'redirect_to_login', text:'URL is on login page with password field'});
    }
  }

  return JSON.stringify({
    anomalies: out,
    body_children: bodyChildren,
    ready_state: readyState,
    title: document.title,
    url: location.href,
    ts: now
  });
})();
`

// browserCheckAnomalyTool implements browser.check_anomaly.
type browserCheckAnomalyTool struct{ holder *browserSessionHolder }

func (t *browserCheckAnomalyTool) Name() string { return "browser.check_anomaly" }
func (t *browserCheckAnomalyTool) Risk() Risk   { return RiskSafe }

func (t *browserCheckAnomalyTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Detect page-level anomalies the Agent should react to:
  - modal_blocking:    [role=dialog] / alertdialog blocking interaction
  - error_message:     [role=alert] / toast-error / keyword-based error detection
  - session_expired:   redirect to /login, 401/403 response, session timeout modal
  - captcha:           reCAPTCHA / hCaptcha / Cloudflare Turnstile / DataDome / GeeTest
  - rate_limited:      HTTP 429 or rate-limit text (via netbuf)
  - blank_page:        body nearly empty + no main content
  - javascript_error:  console.error events captured by JS hook

modes:
  - passive (default):  read existing detector state + fast DOM scan
  - active:             deeper scan including network inspection

Output: structured AnomalyReport. The main loop MAY auto-inject this into
the next tool_result for high/blocker severity without the Agent asking.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "mode":           { "type": "string", "enum": ["passive","active"], "description": "default: passive" },
    "since_ts":       { "type": "integer", "description": "Only report anomalies newer than this unix-millis" },
    "filter_types":   { "type": "array", "items": { "type": "string" }, "description": "Limit to specified anomaly types" },
    "min_severity":   { "type": "string", "enum": ["info","low","medium","high","blocker"] }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "anomalies":   { "type": "array" },
    "page_health": { "type": "string" },
    "checked_at":  { "type": "integer" },
    "mode":        { "type": "string" }
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

type anomalyInput struct {
	Mode        string   `json:"mode"`
	SinceTS     int64    `json:"since_ts"`
	FilterTypes []string `json:"filter_types"`
	MinSeverity string   `json:"min_severity"`
}

func (t *browserCheckAnomalyTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input anomalyInput
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Mode == "" {
		input.Mode = "passive"
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	report, cerr := CheckAnomalies(ctx, sess, t.holder.netbuf, t.holder.history, input)
	if cerr != nil {
		return errResult("check failed: %v", cerr), nil
	}

	data, _ := json.Marshal(report)
	return &Result{Output: data}, nil
}

// CheckAnomalies is exported so the main loop can call it directly
// (bypass the Agent-visible tool registry) for auto-injection.
// hist may be nil — in that case no javascript_error entries are attached.
func CheckAnomalies(ctx context.Context, sess *cdp.BrowserSession, buf *netBuf, hist *anomalyHistory, input anomalyInput) (*AnomalyReport, error) {
	report := &AnomalyReport{
		Anomalies:  []Anomaly{},
		PageHealth: "healthy",
		CheckedAt:  time.Now().UnixMilli(),
		Mode:       input.Mode,
	}

	// Step 1: DOM scan
	domAnomalies, err := scanDOMAnomalies(ctx, sess)
	if err != nil {
		return nil, fmt.Errorf("DOM scan: %w", err)
	}
	report.Anomalies = append(report.Anomalies, domAnomalies...)

	// Step 2: Network-derived anomalies (401/403/429)
	if buf != nil {
		netAnomalies := scanNetworkAnomalies(buf, input.SinceTS)
		report.Anomalies = append(report.Anomalies, netAnomalies...)
	}

	// Step 3: JS errors captured by the Runtime.consoleAPICalled /
	// Runtime.exceptionThrown subscribers (attached in browserSessionHolder.get).
	if hist != nil {
		for _, ev := range hist.drainJSErrors(input.SinceTS) {
			report.Anomalies = append(report.Anomalies, Anomaly{
				Type:        AnomalyJSError,
				Severity:    SeverityLow,
				Description: "JS error: " + clip(ev.Text, 200),
				Text:        ev.Text,
				URL:         ev.URL,
				DetectedAt:  ev.Timestamp.UnixMilli(),
			})
		}
	}

	// Step 4: B-8 extended anomaly detection.
	report.Anomalies = append(report.Anomalies, checkPageLoadErrorsExtended(buf, input.SinceTS)...)
	report.Anomalies = append(report.Anomalies, checkMissingCriticalElements(ctx, sess)...)
	report.Anomalies = append(report.Anomalies, checkDOMMutation(ctx, sess, hist)...)
	report.Anomalies = append(report.Anomalies, checkFormFieldAnomalies(ctx, sess, hist)...)

	// Filter by since_ts
	if input.SinceTS > 0 {
		filtered := report.Anomalies[:0]
		for _, a := range report.Anomalies {
			if a.DetectedAt >= input.SinceTS {
				filtered = append(filtered, a)
			}
		}
		report.Anomalies = filtered
	}

	// Filter by type
	if len(input.FilterTypes) > 0 {
		allowed := make(map[string]bool, len(input.FilterTypes))
		for _, t := range input.FilterTypes {
			allowed[t] = true
		}
		filtered := report.Anomalies[:0]
		for _, a := range report.Anomalies {
			if allowed[string(a.Type)] {
				filtered = append(filtered, a)
			}
		}
		report.Anomalies = filtered
	}

	// Filter by min_severity
	if input.MinSeverity != "" {
		minRank := severityRank(AnomalySeverity(input.MinSeverity))
		filtered := report.Anomalies[:0]
		for _, a := range report.Anomalies {
			if severityRank(a.Severity) >= minRank {
				filtered = append(filtered, a)
			}
		}
		report.Anomalies = filtered
	}

	// Overall page_health
	report.PageHealth = deriveHealth(report.Anomalies)
	return report, nil
}

func severityRank(s AnomalySeverity) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityBlocker:
		return 4
	}
	return 0
}

func deriveHealth(anomalies []Anomaly) string {
	high, blocker := 0, 0
	for _, a := range anomalies {
		if a.Severity == SeverityHigh {
			high++
		} else if a.Severity == SeverityBlocker {
			blocker++
		}
	}
	if blocker > 0 {
		return "blocked"
	}
	if high > 0 {
		return "degraded"
	}
	return "healthy"
}

// scanDOMAnomalies runs anomalyJS and converts raw hits to typed Anomaly records.
func scanDOMAnomalies(ctx context.Context, sess *cdp.BrowserSession) ([]Anomaly, error) {
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    anomalyJS,
		"returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	var raw string
	if err := json.Unmarshal(out.Result.Value, &raw); err != nil {
		return nil, fmt.Errorf("scan result not a string: %s", string(out.Result.Value))
	}
	var parsed struct {
		Anomalies []struct {
			Type    string    `json:"type"`
			Subtype string    `json:"subtype"`
			Text    string    `json:"text"`
			BBox    []float64 `json:"bbox"`
		} `json:"anomalies"`
		TS int64 `json:"ts"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse anomalies: %w", err)
	}

	result := make([]Anomaly, 0, len(parsed.Anomalies))
	for _, a := range parsed.Anomalies {
		item := Anomaly{
			Type:       AnomalyType(a.Type),
			Subtype:    a.Subtype,
			Text:       a.Text,
			DetectedAt: parsed.TS,
		}
		item.Severity, item.Description, item.Suggested = classifyAnomaly(item)
		result = append(result, item)
	}
	return result, nil
}

// scanNetworkAnomalies walks the netbuf for HTTP-derived signals.
func scanNetworkAnomalies(buf *netBuf, sinceTS int64) []Anomaly {
	var out []Anomaly
	for _, e := range buf.snapshot() {
		if sinceTS > 0 && e.StartedAt < sinceTS {
			continue
		}
		switch e.Status {
		case 401, 403:
			out = append(out, Anomaly{
				Type:        AnomalySession,
				Severity:    SeverityHigh,
				Subtype:     "unauthorized",
				Description: fmt.Sprintf("HTTP %d on %s", e.Status, e.URL),
				URL:         e.URL,
				HTTPStatus:  e.Status,
				DetectedAt:  e.StartedAt,
				Suggested:   []string{"re-authenticate", "check stored credentials"},
			})
		case 429:
			out = append(out, Anomaly{
				Type:        AnomalyRateLimit,
				Severity:    SeverityHigh,
				Subtype:     "http_429",
				Description: fmt.Sprintf("HTTP 429 rate limit on %s", e.URL),
				URL:         e.URL,
				HTTPStatus:  429,
				DetectedAt:  e.StartedAt,
				Suggested:   []string{"back off exponentially", "reduce concurrency"},
			})
		}
	}
	return out
}

// classifyAnomaly assigns severity + description based on type/subtype/content.
func classifyAnomaly(a Anomaly) (AnomalySeverity, string, []string) {
	switch a.Type {
	case AnomalyModal:
		sev := SeverityMedium
		desc := fmt.Sprintf("Modal dialog appeared: %q", clip(a.Text, 120))
		sug := []string{"inspect with browser.snapshot to find close/confirm buttons", "decide based on task intent"}
		switch a.Subtype {
		case "session_expired":
			sev = SeverityHigh
			desc = "Session-expired modal: " + clip(a.Text, 120)
			sug = []string{"re-authenticate", "restore session from browser.storage if available"}
		case "cookie_consent":
			sev = SeverityLow
			desc = "Cookie consent banner"
			sug = []string{"accept or reject per task constraints"}
		case "confirmation":
			sev = SeverityHigh
			desc = "Confirmation dialog: " + clip(a.Text, 120)
			sug = []string{"read dialog text carefully", "confirm only if action aligns with task"}
		}
		return sev, desc, sug

	case AnomalyError:
		sev := SeverityMedium
		desc := "Error message: " + clip(a.Text, 200)
		if containsAny(a.Text, []string{"password", "credential", "unauthorized"}) {
			sev = SeverityHigh
			return sev, desc + " (auth-related, do not retry blindly)", []string{"stop retrying", "report to user"}
		}
		return sev, desc, []string{"read error text", "adjust approach"}

	case AnomalyCaptcha:
		switch a.Subtype {
		case "cloudflare_interstitial":
			return SeverityMedium, "Cloudflare 'Just a moment' interstitial — usually passes in 5s", []string{"wait up to 10s", "retry navigation"}
		case "hcaptcha", "geetest", "datadome":
			return SeverityBlocker, fmt.Sprintf("CAPTCHA (%s) requires human intervention", a.Subtype), []string{"request human help"}
		}
		return SeverityHigh, fmt.Sprintf("CAPTCHA detected (%s)", a.Subtype), []string{"request human help if auto-pass fails"}

	case AnomalyBlank:
		return SeverityHigh, "Page is blank or crashed: " + a.Text, []string{"reload page", "give up if reload fails"}

	case AnomalySession:
		return SeverityHigh, "Session appears expired: " + a.Text, []string{"re-authenticate"}

	case AnomalyRateLimit:
		return SeverityHigh, "Rate limited — back off", []string{"wait 10-60s", "reduce request rate"}

	case AnomalyJSError:
		return SeverityLow, "JavaScript error: " + a.Text, nil
	}
	return SeverityInfo, a.Text, nil
}

func containsAny(s string, needles []string) bool {
	ls := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(ls, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// keep regexp import alive for future work.
var _ = regexp.MustCompile

// ---------------------------------------------------------------------------
// anomalyInjectingTool — decorator that appends post-action anomaly reports
// to a wrapped browser tool's output. Inspired by Playwright MCP's a11y
// snapshot injection and Skyvern's Validator sub-agent.
// ---------------------------------------------------------------------------

// anomalyInjectingTool wraps any browser tool. After the tool runs (with any
// outcome), it re-scans for high/blocker anomalies and merges them into the
// returned JSON as a `_anomalies` field. The Agent sees them in the next turn
// without having to ask.
//
// Only wraps side-effect tools (click/type/navigate/open/etc.) — not the
// read-only tools like snapshot/network/check_anomaly (avoids recursion and
// redundant overhead).
type anomalyInjectingTool struct {
	inner  Tool
	holder *browserSessionHolder
}

var autoInjectTools = map[string]bool{
	"browser.open":         true,
	"browser.navigate":     true,
	"browser.click":        true,
	"browser.double_click": true,
	"browser.right_click":  true,
	"browser.type":         true,
	"browser.press_key":    true,
	"browser.scroll":       true,
	"browser.hover":        true,
	"browser.drag":         true,
	"browser.select":       true,
	"browser.upload_file":  true,
	"browser.wait":         true,
	// Intentionally excluded: snapshot, network, check_anomaly, screenshot, eval
}

func (a *anomalyInjectingTool) Name() string   { return a.inner.Name() }
func (a *anomalyInjectingTool) Risk() Risk     { return a.inner.Risk() }
func (a *anomalyInjectingTool) Schema() Schema { return a.inner.Schema() }

func (a *anomalyInjectingTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	res, err := a.inner.Execute(ctx, args)
	if err != nil || res == nil {
		return res, err
	}

	// Task #13: record this action into the per-run sequence log if a recorder
	// is bound to ctx. Fails silently — recording is best-effort.
	recordInteractionForLearning(ctx, a.inner.Name(), args, res)

	// M6 学习闭环:把本次工具执行结果喂给 AdaptiveToolPolicy,让成功率低的
	// 工具按既有策略自动降权。sink 由 cmd/brain 启动时注入;未注入则 no-op。
	if sink := currentOutcomeSink(); sink != nil {
		success := !res.IsError
		sink.RecordOutcome(a.inner.Name(), deriveTaskTypeFromCtx(ctx), success)
	}

	// Skip injection if session not yet up (e.g. error from this very call).
	a.holder.mu.Lock()
	sess := a.holder.session
	a.holder.mu.Unlock()
	if sess == nil {
		return res, nil
	}

	// Use v2 so injected anomalies benefit from severity escalation,
	// subtype-aware suggestions, and JS error capture.
	report, cerr := CheckAnomaliesV2(ctx, sess, a.holder.netbuf, a.holder.history, anomalyInput{
		Mode:        "passive",
		MinSeverity: "high",
	})
	if cerr != nil || report == nil || len(report.Anomalies) == 0 {
		return res, nil
	}

	// Merge _anomalies into the existing JSON output. If output is not a
	// JSON object, wrap it.
	merged := mergeAnomalyIntoOutput(res.Output, report)
	res.Output = merged
	return res, nil
}

// mergeAnomalyIntoOutput appends `_anomalies` and `_page_health` to the
// tool's output JSON. Handles objects, strings, arrays, and invalid JSON.
func mergeAnomalyIntoOutput(orig json.RawMessage, report *AnomalyReport) json.RawMessage {
	// Compact anomaly payload — avoid flooding tool_result with every field.
	summary := struct {
		Health    string    `json:"page_health"`
		Anomalies []Anomaly `json:"anomalies"`
	}{
		Health:    report.PageHealth,
		Anomalies: report.Anomalies,
	}

	// Try to parse orig as an object and inject.
	var asObj map[string]json.RawMessage
	if json.Valid(orig) && json.Unmarshal(orig, &asObj) == nil && asObj != nil {
		payload, _ := json.Marshal(summary)
		asObj["_anomalies"] = payload
		out, err := json.Marshal(asObj)
		if err == nil {
			return out
		}
	}

	// Fallback: wrap non-object payloads in an envelope.
	wrapped := map[string]interface{}{
		"_original":  json.RawMessage(orig),
		"_anomalies": summary,
	}
	out, _ := json.Marshal(wrapped)
	return out
}
