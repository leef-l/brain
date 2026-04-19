package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

// P1 tool set — see sdk/docs/39-Browser-Brain感知与嗅探增强设计.md §4.
//
//   browser.iframe      — inspect/switch CDP frame targets
//   browser.downloads   — wait for/list downloaded files
//   browser.storage     — export/import cookies + local/sessionStorage
//   browser.fill_form   — batch-fill a form by label/name/placeholder
//   browser.changes     — DOM diff since last snapshot (MutationObserver)

// ---------------------------------------------------------------------------
// browser.iframe
// ---------------------------------------------------------------------------

type browserIframeTool struct{ holder *browserSessionHolder }

func (t *browserIframeTool) Name() string { return "browser.iframe" }
func (t *browserIframeTool) Risk() Risk   { return RiskSafe }

func (t *browserIframeTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Inspect iframes on the current page and optionally read their structure.

actions:
  - list: return every frame's url/name/id/size (default)
  - snapshot: run browser.snapshot inside a frame (select by url_pattern / name / index)
  - eval: run JS inside a specific frame

iframes drive payment widgets (Stripe), captcha challenges, rich editors,
and ads. The parent snapshot sees them only as opaque boxes; this tool
punches through.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":      { "type": "string", "enum": ["list","snapshot","eval"], "description": "default: list" },
    "url_pattern": { "type": "string", "description": "regex to locate the frame (snapshot/eval)" },
    "name":        { "type": "string", "description": "frame name attribute (snapshot/eval)" },
    "index":       { "type": "integer", "description": "0-based index (snapshot/eval)" },
    "expression":  { "type": "string", "description": "JS to evaluate (action=eval)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":  { "type": "string" },
    "count":   { "type": "integer" },
    "frames":  { "type": "array" },
    "elements":{ "type": "array" },
    "value":   {}
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

type iframeInput struct {
	Action     string `json:"action"`
	URLPattern string `json:"url_pattern"`
	Name       string `json:"name"`
	Index      int    `json:"index"`
	Expression string `json:"expression"`
}

type frameInfo struct {
	Index   int    `json:"index"`
	URL     string `json:"url"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	Width   int    `json:"w"`
	Height  int    `json:"h"`
	Visible bool   `json:"visible"`
}

func (t *browserIframeTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	input := iframeInput{Action: "list", Index: -1}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Action == "" {
		input.Action = "list"
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	frames, err := listFrames(ctx, sess)
	if err != nil {
		return errResult("list frames: %v", err), nil
	}

	if input.Action == "list" {
		return okResult(map[string]interface{}{
			"action": "list", "count": len(frames), "frames": frames,
		}), nil
	}

	// Locate target frame
	target := selectFrame(frames, input)
	if target == nil {
		return errResult("no frame matches (url_pattern=%q name=%q index=%d)", input.URLPattern, input.Name, input.Index), nil
	}

	switch input.Action {
	case "snapshot":
		elements, err := frameSnapshot(ctx, sess, target)
		if err != nil {
			return errResult("frame snapshot: %v", err), nil
		}
		return okResult(map[string]interface{}{
			"action": "snapshot", "frame": target, "elements": elements,
		}), nil

	case "eval":
		if input.Expression == "" {
			return errResult("expression required for action=eval"), nil
		}
		val, err := frameEval(ctx, sess, target, input.Expression)
		if err != nil {
			return errResult("frame eval: %v", err), nil
		}
		return okResult(map[string]interface{}{
			"action": "eval", "frame": target, "value": val,
		}), nil
	}
	return errResult("unknown action %q", input.Action), nil
}

// listFrames enumerates all iframes on the current page by querying the DOM.
// Uses plain getBoundingClientRect for visible-size computation instead of
// deep CDP frame tree traversal (which would require cross-target session
// management that's out of scope for Stage 1).
func listFrames(ctx context.Context, sess *cdp.BrowserSession) ([]frameInfo, error) {
	js := `JSON.stringify(Array.from(document.querySelectorAll('iframe')).map(function(el, i){
  var r = el.getBoundingClientRect();
  return {
    index: i,
    url: el.src || '',
    name: el.name || '',
    id: el.id || '',
    w: Math.round(r.width),
    h: Math.round(r.height),
    visible: r.width>0 && r.height>0 && getComputedStyle(el).visibility !== 'hidden'
  };
}))`
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	var raw string
	if err := json.Unmarshal(out.Result.Value, &raw); err != nil {
		return nil, err
	}
	var frames []frameInfo
	if err := json.Unmarshal([]byte(raw), &frames); err != nil {
		return nil, err
	}
	return frames, nil
}

func selectFrame(frames []frameInfo, input iframeInput) *frameInfo {
	if input.Index >= 0 && input.Index < len(frames) {
		return &frames[input.Index]
	}
	if input.Name != "" {
		for i := range frames {
			if frames[i].Name == input.Name {
				return &frames[i]
			}
		}
	}
	if input.URLPattern != "" {
		re, err := compileRegexSafe(input.URLPattern)
		if err == nil {
			for i := range frames {
				if re.MatchString(frames[i].URL) {
					return &frames[i]
				}
			}
		}
	}
	return nil
}

// frameSnapshot runs brainSnapshotJS inside an iframe via contentWindow.eval.
// Cross-origin frames will fail with a security error — we return an empty
// list rather than crashing.
func frameSnapshot(ctx context.Context, sess *cdp.BrowserSession, f *frameInfo) ([]brainElement, error) {
	// Inject our brain-snapshot function into the target frame's window,
	// then call it and read the result back.
	js := fmt.Sprintf(`(function(){
  try {
    var iframe = document.querySelectorAll('iframe')[%d];
    if(!iframe) return JSON.stringify({error:"frame not found"});
    var fwin = iframe.contentWindow;
    if(!fwin) return JSON.stringify({error:"no contentWindow"});
    var fdoc = fwin.document;
    if(!fdoc) return JSON.stringify({error:"no frame document (cross-origin)"});
    // Inline snapshot inside the frame (cross-window function exec isn't trivial).
    var out = [];
    var n = 0;
    var sel = 'a,button,input,select,textarea,[role=button],[role=link]';
    fdoc.querySelectorAll(sel).forEach(function(el){
      var r = el.getBoundingClientRect();
      if(r.width <= 0 || r.height <= 0) return;
      n++;
      el.setAttribute('data-brain-id', String(n));
      out.push({
        id: n, tag: el.tagName.toLowerCase(),
        role: el.getAttribute('role') || el.tagName.toLowerCase(),
        type: el.getAttribute('type') || '',
        name: ((el.getAttribute('aria-label')||el.innerText||el.placeholder||'').trim()).slice(0,120),
        value: el.value || '',
        href: el.tagName === 'A' ? (el.getAttribute('href')||'') : '',
        x: Math.round(r.x+r.width/2), y: Math.round(r.y+r.height/2),
        w: Math.round(r.width), h: Math.round(r.height),
        inViewport: r.top>=0 && r.bottom<=innerHeight
      });
    });
    return JSON.stringify({ok:true, elements: out});
  } catch(e) {
    return JSON.stringify({error: String(e)});
  }
})()`, f.Index)

	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	var raw string
	if err := json.Unmarshal(out.Result.Value, &raw); err != nil {
		return nil, err
	}
	var parsed struct {
		OK       bool           `json:"ok"`
		Elements []brainElement `json:"elements"`
		Error    string         `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("%s", parsed.Error)
	}
	return parsed.Elements, nil
}

func frameEval(ctx context.Context, sess *cdp.BrowserSession, f *frameInfo, expr string) (interface{}, error) {
	escaped, _ := json.Marshal(expr)
	js := fmt.Sprintf(`(function(){
  try {
    var iframe = document.querySelectorAll('iframe')[%d];
    if(!iframe) return JSON.stringify({error:"frame not found"});
    var fwin = iframe.contentWindow;
    if(!fwin) return JSON.stringify({error:"no contentWindow"});
    var val = fwin.eval(%s);
    return JSON.stringify({ok:true, value: val});
  } catch(e) {
    return JSON.stringify({error: String(e)});
  }
})()`, f.Index, string(escaped))
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	var raw string
	if err := json.Unmarshal(out.Result.Value, &raw); err != nil {
		return nil, err
	}
	var parsed struct {
		OK    bool        `json:"ok"`
		Value interface{} `json:"value"`
		Error string      `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("%s", parsed.Error)
	}
	return parsed.Value, nil
}

// ---------------------------------------------------------------------------
// browser.downloads
// ---------------------------------------------------------------------------

// downloadRegistry lives on the holder, populated via Browser.setDownloadBehavior.
// Stage 1 implementation: poll a target directory that we configure once at
// first use. Production-grade streaming via CDP Page.downloadWillBegin etc.
// belongs to Stage 2.

type browserDownloadsTool struct {
	holder      *browserSessionHolder
	initOnce    sync.Once
	downloadDir string
}

func (t *browserDownloadsTool) Name() string { return "browser.downloads" }
func (t *browserDownloadsTool) Risk() Risk   { return RiskMedium }

func (t *browserDownloadsTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `List files downloaded during this session, or wait for a specific download to finish.

actions:
  - list: return all files under the per-session download dir
  - wait: block until a new file appears (or matches a name regex)

Downloaded files land in ~/.brain/downloads/<session>/.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":       { "type": "string", "enum": ["list","wait"], "description": "default: list" },
    "name_pattern": { "type": "string", "description": "regex matching filename (action=wait)" },
    "timeout_ms":   { "type": "integer", "description": "default 15000" },
    "since_ts":     { "type": "integer", "description": "unix millis, filter list to files modified after" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":     { "type": "string" },
    "directory":  { "type": "string" },
    "count":      { "type": "integer" },
    "files":      { "type": "array" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:downloads",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "safe",
		},
	}
}

func (t *browserDownloadsTool) ensureInit(ctx context.Context) (string, error) {
	var initErr error
	t.initOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		dir := filepath.Join(home, ".brain", "downloads", fmt.Sprintf("session-%d", time.Now().Unix()))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			initErr = err
			return
		}
		t.downloadDir = dir
		sess, err := t.holder.get(ctx)
		if err != nil {
			initErr = err
			return
		}
		// Configure CDP to save downloads here
		_ = sess.Exec(ctx, "Browser.setDownloadBehavior", map[string]interface{}{
			"behavior":     "allow",
			"downloadPath": dir,
		}, nil)
	})
	return t.downloadDir, initErr
}

type fileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime_ms"`
}

func (t *browserDownloadsTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Action      string `json:"action"`
		NamePattern string `json:"name_pattern"`
		TimeoutMS   int    `json:"timeout_ms"`
		SinceTS     int64  `json:"since_ts"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Action == "" {
		input.Action = "list"
	}
	if input.TimeoutMS <= 0 {
		input.TimeoutMS = 15_000
	}

	dir, err := t.ensureInit(ctx)
	if err != nil {
		return errResult("init downloads: %v", err), nil
	}

	switch input.Action {
	case "list":
		files := scanFiles(dir, input.SinceTS)
		return okResult(map[string]interface{}{
			"action": "list", "directory": dir, "count": len(files), "files": files,
		}), nil

	case "wait":
		deadline := time.Now().Add(time.Duration(input.TimeoutMS) * time.Millisecond)
		var re interface{ MatchString(string) bool }
		if input.NamePattern != "" {
			compiled, err := compileRegexSafe(input.NamePattern)
			if err != nil {
				return errResult("invalid name_pattern: %v", err), nil
			}
			re = compiled
		}
		startTS := input.SinceTS
		if startTS == 0 {
			startTS = time.Now().UnixMilli()
		}
		for time.Now().Before(deadline) {
			files := scanFiles(dir, startTS)
			for _, f := range files {
				// Skip temporary partial files
				if strings.HasSuffix(f.Name, ".crdownload") || strings.HasSuffix(f.Name, ".part") {
					continue
				}
				if re == nil || re.MatchString(f.Name) {
					return okResult(map[string]interface{}{
						"action": "wait", "directory": dir, "count": 1, "files": []fileInfo{f},
					}), nil
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		return ErrorResult(brainerrors.CodeToolTimeout, "download timeout after %dms", input.TimeoutMS), nil
	}
	return errResult("unknown action %q", input.Action), nil
}

func scanFiles(dir string, sinceTS int64) []fileInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime().UnixMilli()
		if sinceTS > 0 && mt < sinceTS {
			continue
		}
		out = append(out, fileInfo{
			Name:    e.Name(),
			Path:    filepath.Join(dir, e.Name()),
			Size:    info.Size(),
			ModTime: mt,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// browser.storage
// ---------------------------------------------------------------------------

type browserStorageTool struct{ holder *browserSessionHolder }

func (t *browserStorageTool) Name() string { return "browser.storage" }
func (t *browserStorageTool) Risk() Risk   { return RiskMedium }

func (t *browserStorageTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Export / import browser state for the current origin.

Covers: cookies (all for origin), localStorage, sessionStorage.
Use this to persist authenticated sessions across Agent runs without
re-logging in ("session state save/load", like Playwright's storage_state).

actions:
  - export: read all state, return as JSON (write to path if supplied)
  - import: overwrite current origin's state from JSON (or path)
  - clear:  drop cookies + localStorage + sessionStorage for origin`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": ["export","import","clear"] },
    "path":   { "type": "string", "description": "Optional file path to read/write state" },
    "state":  { "type": "object", "description": "Inline state (action=import) if path not used" }
  },
  "required": ["action"]
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action":  { "type": "string" },
    "state":   { "type": "object" },
    "written": { "type": "string" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserStorageTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Action string                 `json:"action"`
		Path   string                 `json:"path"`
		State  map[string]interface{} `json:"state"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	switch input.Action {
	case "export":
		state, err := exportStorage(ctx, sess)
		if err != nil {
			return errResult("export: %v", err), nil
		}
		payload := map[string]interface{}{"action": "export", "state": state}
		if input.Path != "" {
			data, _ := json.MarshalIndent(state, "", "  ")
			if err := os.WriteFile(input.Path, data, 0o600); err != nil {
				return errResult("write %s: %v", input.Path, err), nil
			}
			payload["written"] = input.Path
		}
		return okResult(payload), nil

	case "import":
		state := input.State
		if input.Path != "" {
			data, err := os.ReadFile(input.Path)
			if err != nil {
				return errResult("read %s: %v", input.Path, err), nil
			}
			if err := json.Unmarshal(data, &state); err != nil {
				return errResult("parse %s: %v", input.Path, err), nil
			}
		}
		if err := importStorage(ctx, sess, state); err != nil {
			return errResult("import: %v", err), nil
		}
		return okResult(map[string]interface{}{"action": "import"}), nil

	case "clear":
		if err := clearStorage(ctx, sess); err != nil {
			return errResult("clear: %v", err), nil
		}
		return okResult(map[string]interface{}{"action": "clear"}), nil
	}
	return errResult("unknown action %q", input.Action), nil
}

func exportStorage(ctx context.Context, sess *cdp.BrowserSession) (map[string]interface{}, error) {
	// Cookies via CDP
	var cookiesResp struct {
		Cookies []map[string]interface{} `json:"cookies"`
	}
	if err := sess.Exec(ctx, "Network.getAllCookies", nil, &cookiesResp); err != nil {
		return nil, fmt.Errorf("getAllCookies: %w", err)
	}

	// localStorage + sessionStorage via Runtime.evaluate
	js := `JSON.stringify({
		local: Object.fromEntries(Object.keys(localStorage).map(function(k){return [k, localStorage.getItem(k)]})),
		session: Object.fromEntries(Object.keys(sessionStorage).map(function(k){return [k, sessionStorage.getItem(k)]}))
	})`
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &out); err != nil {
		return nil, err
	}
	var raw string
	_ = json.Unmarshal(out.Result.Value, &raw)
	var storage map[string]map[string]string
	_ = json.Unmarshal([]byte(raw), &storage)

	return map[string]interface{}{
		"cookies":        cookiesResp.Cookies,
		"localStorage":   storage["local"],
		"sessionStorage": storage["session"],
	}, nil
}

func importStorage(ctx context.Context, sess *cdp.BrowserSession, state map[string]interface{}) error {
	if cookies, ok := state["cookies"].([]interface{}); ok {
		converted := make([]map[string]interface{}, 0, len(cookies))
		for _, c := range cookies {
			if cm, ok := c.(map[string]interface{}); ok {
				converted = append(converted, cm)
			}
		}
		if err := sess.Exec(ctx, "Network.setCookies", map[string]interface{}{
			"cookies": converted,
		}, nil); err != nil {
			return fmt.Errorf("setCookies: %w", err)
		}
	}
	applyKV := func(area string, src map[string]interface{}) error {
		data, _ := json.Marshal(src)
		js := fmt.Sprintf(`(function(){
  var obj = %s;
  Object.keys(obj).forEach(function(k){ try { %s.setItem(k, obj[k]); } catch(e){} });
  return Object.keys(obj).length;
})()`, string(data), area)
		return sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression": js, "returnByValue": true,
		}, nil)
	}
	if ls, ok := state["localStorage"].(map[string]interface{}); ok {
		if err := applyKV("localStorage", ls); err != nil {
			return err
		}
	}
	if ss, ok := state["sessionStorage"].(map[string]interface{}); ok {
		if err := applyKV("sessionStorage", ss); err != nil {
			return err
		}
	}
	return nil
}

func clearStorage(ctx context.Context, sess *cdp.BrowserSession) error {
	if err := sess.Exec(ctx, "Network.clearBrowserCookies", nil, nil); err != nil {
		return err
	}
	return sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    `localStorage.clear(); sessionStorage.clear(); true`,
		"returnByValue": true,
	}, nil)
}

// ---------------------------------------------------------------------------
// browser.fill_form
// ---------------------------------------------------------------------------

type browserFillFormTool struct{ holder *browserSessionHolder }

func (t *browserFillFormTool) Name() string { return "browser.fill_form" }
func (t *browserFillFormTool) Risk() Risk   { return RiskMedium }

func (t *browserFillFormTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Fill multiple form fields in one call by label/name/placeholder.

Cuts a 10-field form from 10 tool calls to 1. Accepts a map {field_key: value};
matching priority:
  1. <label for=field_key>  → field
  2. name="field_key"
  3. aria-label exact
  4. placeholder exact
  5. case-insensitive contains of the above

Return per-field outcome (matched / not_found / error).`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "fields": { "type": "object", "description": "map of label→value" },
    "submit": { "type": "boolean", "description": "click submit after (default false)" }
  },
  "required": ["fields"]
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "filled":    { "type": "integer" },
    "missing":   { "type": "array" },
    "results":   { "type": "array" },
    "submitted": { "type": "boolean" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserFillFormTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Fields map[string]interface{} `json:"fields"`
		Submit bool                   `json:"submit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if len(input.Fields) == 0 {
		return errResult("fields is required and must not be empty"), nil
	}
	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	fieldsJSON, _ := json.Marshal(input.Fields)
	js := fmt.Sprintf(`(function(){
  var fields = %s;
  var results = [];
  function lookup(key){
    var lbl = document.querySelector('label[for="'+CSS.escape(key)+'"]');
    if(lbl){ var el = document.getElementById(key); if(el) return el; }
    var byName = document.querySelector('[name="'+CSS.escape(key)+'"]');
    if(byName) return byName;
    var byAria = document.querySelector('[aria-label="'+CSS.escape(key)+'"]');
    if(byAria) return byAria;
    var byPh = document.querySelector('[placeholder="'+CSS.escape(key)+'"]');
    if(byPh) return byPh;
    // Case-insensitive contains
    var lower = key.toLowerCase();
    var inputs = document.querySelectorAll('input, textarea, select');
    for(var i=0; i<inputs.length; i++){
      var el = inputs[i];
      var hints = [el.name, el.placeholder, el.getAttribute('aria-label'), el.id];
      for(var j=0; j<hints.length; j++){
        if(hints[j] && hints[j].toLowerCase().indexOf(lower) >= 0) return el;
      }
    }
    return null;
  }
  Object.keys(fields).forEach(function(k){
    var el = lookup(k);
    if(!el){ results.push({key:k, status:'not_found'}); return; }
    try {
      var val = fields[k];
      if(el.tagName === 'SELECT'){
        el.value = val;
        el.dispatchEvent(new Event('change',{bubbles:true}));
      } else if(el.type === 'checkbox' || el.type === 'radio'){
        el.checked = !!val;
        el.dispatchEvent(new Event('change',{bubbles:true}));
      } else {
        el.focus();
        el.value = val == null ? '' : String(val);
        el.dispatchEvent(new Event('input',{bubbles:true}));
        el.dispatchEvent(new Event('change',{bubbles:true}));
      }
      results.push({key:k, status:'ok', tag:el.tagName.toLowerCase(), name:el.name||el.id||''});
    } catch(e){ results.push({key:k, status:'error', error:String(e)}); }
  });
  var submitted = false;
  if(%t){
    var btn = document.querySelector('button[type="submit"], input[type="submit"]');
    if(btn){ btn.click(); submitted = true; }
  }
  return JSON.stringify({results: results, submitted: submitted});
})()`, string(fieldsJSON), input.Submit)

	var raw struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true,
	}, &raw); err != nil {
		return errResult("fill_form: %v", err), nil
	}
	var asStr string
	if err := json.Unmarshal(raw.Result.Value, &asStr); err != nil {
		return errResult("bad result"), nil
	}
	var parsed struct {
		Results []map[string]interface{} `json:"results"`
		Submitted bool                   `json:"submitted"`
	}
	if err := json.Unmarshal([]byte(asStr), &parsed); err != nil {
		return errResult("parse: %v", err), nil
	}
	filled := 0
	missing := []string{}
	for _, r := range parsed.Results {
		if s, _ := r["status"].(string); s == "ok" {
			filled++
		} else if s == "not_found" {
			missing = append(missing, fmt.Sprint(r["key"]))
		}
	}
	return okResult(map[string]interface{}{
		"filled":    filled,
		"missing":   missing,
		"results":   parsed.Results,
		"submitted": parsed.Submitted,
	}), nil
}

// ---------------------------------------------------------------------------
// browser.changes — DOM diff since last call (MutationObserver)
// ---------------------------------------------------------------------------

type browserChangesTool struct {
	holder *browserSessionHolder
	once   sync.Once
}

func (t *browserChangesTool) Name() string { return "browser.changes" }
func (t *browserChangesTool) Risk() Risk   { return RiskSafe }

func (t *browserChangesTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Report DOM changes since the previous call.

Installs a MutationObserver on first use. Subsequent calls return only the
records buffered since the previous call, then reset. Useful for SPAs where
content arrives async after an action — rather than re-snapshotting the whole
page (expensive), just inspect what changed.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "limit": { "type": "integer", "description": "Max records (default 100)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "initialized":   { "type": "boolean" },
    "count":         { "type": "integer" },
    "records":       { "type": "array" }
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

const mutationObserverSetupJS = `
(function(){
  if(window.__brainMO) return 'already';
  window.__brainMORecords = [];
  window.__brainMO = new MutationObserver(function(list){
    list.forEach(function(m){
      window.__brainMORecords.push({
        type: m.type,
        target: m.target.nodeName ? m.target.nodeName.toLowerCase() : '',
        targetId: m.target.getAttribute ? (m.target.getAttribute('data-brain-id')||'') : '',
        added: Array.from(m.addedNodes||[]).map(function(n){return n.nodeName ? n.nodeName.toLowerCase() : ''}).slice(0, 5),
        removed: Array.from(m.removedNodes||[]).map(function(n){return n.nodeName ? n.nodeName.toLowerCase() : ''}).slice(0, 5),
        attributeName: m.attributeName || '',
        ts: Date.now()
      });
      if(window.__brainMORecords.length > 1000) window.__brainMORecords.shift();
    });
  });
  window.__brainMO.observe(document.documentElement, {
    childList: true, subtree: true, attributes: true, attributeFilter: ['class','role','aria-hidden','aria-expanded','hidden']
  });
  return 'installed';
})()
`

func (t *browserChangesTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Limit int `json:"limit"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Limit <= 0 {
		input.Limit = 100
	}
	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	initialized := false
	// Always try to install (idempotent); page navigation resets the world.
	_ = sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    mutationObserverSetupJS,
		"returnByValue": true,
	}, nil)

	readJS := fmt.Sprintf(`(function(){
  if(!window.__brainMORecords){ return JSON.stringify({initialized:false, records:[]}); }
  var recs = window.__brainMORecords.slice(-%d);
  window.__brainMORecords = [];
  return JSON.stringify({initialized:true, records:recs});
})()`, input.Limit)

	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": readJS, "returnByValue": true,
	}, &out); err != nil {
		return errResult("changes: %v", err), nil
	}
	var raw string
	_ = json.Unmarshal(out.Result.Value, &raw)
	var parsed struct {
		Initialized bool                     `json:"initialized"`
		Records     []map[string]interface{} `json:"records"`
	}
	_ = json.Unmarshal([]byte(raw), &parsed)
	initialized = parsed.Initialized

	t.once.Do(func() { _ = initialized }) // silence linter; the intent is documented
	return okResult(map[string]interface{}{
		"initialized": initialized,
		"count":       len(parsed.Records),
		"records":     parsed.Records,
	}), nil
}

// ---------------------------------------------------------------------------
// small helper: safe regex compile
// ---------------------------------------------------------------------------

func compileRegexSafe(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	return regexp.Compile(p)
}
