package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

// ---------------------------------------------------------------------------
// BrowserSession holder — shared across all browser tools
// ---------------------------------------------------------------------------

// browserSessionHolder manages the shared browser session for all browser tools.
// The session is lazily initialized on first use.
type browserSessionHolder struct {
	mu      sync.Mutex
	session *cdp.BrowserSession
	netbuf  *netBuf         // Network event buffer; attached on first session creation.
	history *anomalyHistory // Anomaly history; attached on first session creation.

	// snapshotCache 承载 P3.4 增量 snapshot 的上一次结果。browserSnapshotTool
	// 在 incremental=true 且 MutationObserver 已注入时读取,和本次采集到的
	// dirty 元素合并。pageKey 用 URL 做边界 —— 跨页导航后必须全扫,避免上
	// 个页面的 data-brain-id 映射污染新页面。
	snapshotCache        []brainElement
	snapshotCachePageKey string
	// observerInstalled 表示已经 Exec 过 brainInstallObserverJS,且 URL
	// 未变化(URL 变化时自然会被 MutationObserver 本身 reset,但本侧也清零)。
	observerInstalled bool
}

func newBrowserSessionHolder() *browserSessionHolder {
	return &browserSessionHolder{
		netbuf:  newNetBuf(200),
		history: newAnomalyHistory(),
	}
}

var (
	sharedBrowserSessionMu       sync.RWMutex
	sharedBrowserSessionOwner    *browserSessionHolder
	sharedBrowserSessionAccessor func() (*cdp.BrowserSession, bool)
)

func registerSharedBrowserSessionAccessor(holder *browserSessionHolder) {
	sharedBrowserSessionMu.Lock()
	defer sharedBrowserSessionMu.Unlock()
	sharedBrowserSessionOwner = holder
	sharedBrowserSessionAccessor = holder.current
}

func unregisterSharedBrowserSessionAccessor(holder *browserSessionHolder) {
	sharedBrowserSessionMu.Lock()
	defer sharedBrowserSessionMu.Unlock()
	if sharedBrowserSessionOwner != holder {
		return
	}
	sharedBrowserSessionOwner = nil
	sharedBrowserSessionAccessor = nil
}

// CurrentSharedBrowserSession returns the already-created shared browser
// session, if one exists. It never creates a new session and is intended for
// host-side consumers that need read-only access to the live browser session.
func CurrentSharedBrowserSession() (*cdp.BrowserSession, bool) {
	sharedBrowserSessionMu.RLock()
	accessor := sharedBrowserSessionAccessor
	sharedBrowserSessionMu.RUnlock()
	if accessor == nil {
		return nil, false
	}
	return accessor()
}

// anomalyHistory exposes the per-session history for v2 tools.
func (h *browserSessionHolder) anomalyHistory() *anomalyHistory {
	return h.history
}

// current returns the active session only if it has already been created.
// Unlike get, it never launches a new browser session.
func (h *browserSessionHolder) current() (*cdp.BrowserSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session == nil {
		return nil, false
	}
	return h.session, true
}

// get returns the current session, creating one if needed.
// On first creation, it subscribes the NetBuf to CDP Network.* events so
// every browser tool observes the same in-flight request stream.
func (h *browserSessionHolder) get(ctx context.Context) (*cdp.BrowserSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.session != nil {
		return h.session, nil
	}

	s, err := cdp.NewBrowserSession(ctx)
	if err != nil {
		return nil, err
	}
	h.session = s
	// Subscribe NetBuf and JS-error watcher to CDP events. Both are idempotent
	// per-client: the session is freshly created so no listeners exist yet.
	h.netbuf.attach(s.Client())
	attachJSErrorWatcher(s.Client(), h.history)
	return s, nil
}

// close shuts down the session if running.
func (h *browserSessionHolder) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session != nil {
		h.session.Close()
		h.session = nil
	}
	unregisterSharedBrowserSessionAccessor(h)
}

// ---------------------------------------------------------------------------
// NewBrowserTools returns all browser tools sharing a single session.
// ---------------------------------------------------------------------------

// NewBrowserTools creates all browser tools sharing a single browser session.
//
// Side-effecting tools (click/type/open/navigate/…) are automatically wrapped
// with anomalyInjectingTool so that post-action high/blocker anomalies are
// appended to the tool_result under `_anomalies`. Read-only tools and the
// anomaly tool itself are returned unwrapped to avoid recursion.
func NewBrowserTools() []Tool {
	applyBrowserFeatureGateEnvDefaults()

	holder := newBrowserSessionHolder()
	registerSharedBrowserSessionAccessor(holder)
	patternExec := &browserPatternExecTool{holder: holder}
	raw := []Tool{
		&browserOpenTool{holder: holder},
		&browserNavigateTool{holder: holder},
		&browserSnapshotTool{holder: holder},        // P0-1 — semantic+interactive snapshot
		&browserNetworkTool{holder: holder},         // P0-2 — observed request buffer
		&browserWaitNetworkIdleTool{holder: holder}, // P0-2 — true networkIdle
		&browserCheckAnomalyTool{holder: holder},    // P0-3 — anomaly perception MVP
		&browserUnderstandTool{holder: holder},      // Phase 1 — L4-L7 semantic annotation + cache
		&browserSitemapTool{holder: holder},         // P0-3 — whole-site BFS + route patterns
		&browserPatternMatchTool{holder: holder},    // Phase 2 — UI pattern match
		patternExec,                                 // Phase 2 — UI pattern exec
		&browserPatternListTool{},                   // Phase 2 — UI pattern list
		&browserIframeTool{holder: holder},          // P1 — iframe punch-through
		&browserDownloadsTool{holder: holder},       // P1 — downloads dir watcher
		&browserStorageTool{holder: holder},         // P1 — cookies + localStorage export/import
		&browserFillFormTool{holder: holder},        // P1 — batch form fill
		&browserChangesTool{holder: holder},         // P1 — DOM diff since last call
		&browserCheckAnomalyV2Tool{holder: holder},  // Task #10 — full anomaly perception
		&browserClickTool{holder: holder},
		&browserDoubleClickTool{holder: holder},
		&browserRightClickTool{holder: holder},
		&browserTypeTool{holder: holder},
		&browserPressKeyTool{holder: holder},
		&browserScrollTool{holder: holder},
		&browserHoverTool{holder: holder},
		&browserDragTool{holder: holder},
		&browserGeometryTool{holder: holder},
		&browserSelectTool{holder: holder},
		&browserUploadFileTool{holder: holder},
		&browserScreenshotTool{holder: holder},
		&browserVisualInspectTool{holder: holder}, // Phase 3 — multimodal fallback
		&browserEvalTool{holder: holder},
		&browserWaitTool{holder: holder},
	}
	out := make([]Tool, len(raw))
	for i, t := range raw {
		wrapped := t
		if autoInjectTools[t.Name()] {
			wrapped = &anomalyInjectingTool{inner: wrapped, holder: holder}
		}
		if retryTools[t.Name()] {
			wrapped = WithRetry(wrapped)
		}
		if feature := browserFeatureForTool(t.Name()); feature != "" {
			wrapped = &browserFeatureGatedTool{inner: wrapped, feature: feature}
		}
		out[i] = wrapped
	}
	// pattern_exec needs the fully-wrapped sibling map so its internal calls
	// to browser.click / browser.type also get anomaly injection. M5 also
	// needs human.request_takeover so on_anomaly=human_intervention can route
	// through the coordinator without depending on a host-level registry.
	takeoverTool := NewHumanRequestTakeoverTool()
	siblings := make([]Tool, 0, len(out)+1)
	siblings = append(siblings, out...)
	siblings = append(siblings, takeoverTool)
	patternExec.setSiblings(siblings)
	// 把 takeover 工具也暴露给 browser 的主 registry,否则 browser Agent
	// Loop / planner 看不见这个工具,LLM 永远不会主动调它。是学习闭环
	// 的关键补丁:没有这一步,遇到滑块 LLM 只会在回答里"假装接管",
	// 不会真的阻塞 run 去录制用户操作。
	out = append(out, takeoverTool)
	return out
}

// retryTools 列出启用 Task #14 retry 装饰器的 browser 工具。只挑真正会遇到
// transient 错误的工具(导航/等待/下载),读只读工具(snapshot/network)无
// 重试价值——失败了也是参数或会话问题,重试只会浪费配额。
var retryTools = map[string]bool{
	"browser.open":              true,
	"browser.navigate":          true,
	"browser.wait":              true,
	"browser.wait_network_idle": true,
	"browser.downloads":         true,
}

// CloseBrowserSession closes the shared browser session.
// Call this when the brain shuts down.
func CloseBrowserSession(tools []Tool) {
	for _, t := range tools {
		if bt, ok := t.(interface{ closeSession() }); ok {
			bt.closeSession()
			return
		}
	}
}

// ---------------------------------------------------------------------------
// 1. browser.open — Open a URL
// ---------------------------------------------------------------------------

type browserOpenTool struct{ holder *browserSessionHolder }

func (t *browserOpenTool) closeSession() { t.holder.close() }
func (t *browserOpenTool) Name() string  { return "browser.open" }
func (t *browserOpenTool) Risk() Risk    { return RiskMedium }
func (t *browserOpenTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Open a URL in the browser. Creates a new browser session if needed.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": { "type": "string", "description": "URL to open" },
    "new_tab": { "type": "boolean", "description": "Open in a new tab (default: false, navigates current tab)" }
  },
  "required": ["url"]
}`),
		OutputSchema: browserOpenOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.navigate",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "task",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserOpenTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		URL    string `json:"url"`
		NewTab bool   `json:"new_tab"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.URL == "" {
		return errResult("url is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("browser launch failed: %v", err), nil
	}

	if input.NewTab {
		if err := sess.NewTab(ctx, input.URL); err != nil {
			return classifyNavigateError(err, "new tab"), nil
		}
	} else {
		if err := sess.Navigate(ctx, input.URL); err != nil {
			return classifyNavigateError(err, "navigate"), nil
		}
	}

	// Wait for page load.
	waitForLoad(ctx, sess, 10*time.Second)

	return okResult(map[string]interface{}{
		"status":    "ok",
		"url":       input.URL,
		"target_id": sess.TargetID(),
	}), nil
}

// ---------------------------------------------------------------------------
// 2. browser.navigate — Forward/back/refresh/new tab
// ---------------------------------------------------------------------------

type browserNavigateTool struct{ holder *browserSessionHolder }

func (t *browserNavigateTool) Name() string { return "browser.navigate" }
func (t *browserNavigateTool) Risk() Risk   { return RiskSafe }
func (t *browserNavigateTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Navigate: go back, forward, refresh, or switch/close tabs.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "description": "back, forward, refresh, list_tabs, switch_tab, close_tab" },
    "target_id": { "type": "string", "description": "Target ID for switch_tab/close_tab" }
  },
  "required": ["action"]
}`),
		OutputSchema: browserNavigateOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.navigate",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "task",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserNavigateTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Action   string `json:"action"`
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	switch input.Action {
	case "back":
		err = sess.Exec(ctx, "Page.navigateToHistoryEntry", nil, nil)
		// Use JS history.back() instead — more reliable.
		err = sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression": "history.back()",
		}, nil)
	case "forward":
		err = sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression": "history.forward()",
		}, nil)
	case "refresh":
		err = sess.Exec(ctx, "Page.reload", nil, nil)
	case "list_tabs":
		tabs, tabErr := sess.ListTabs(ctx)
		if tabErr != nil {
			return errResult("list tabs: %v", tabErr), nil
		}
		return okResult(tabs), nil
	case "switch_tab":
		if input.TargetID == "" {
			return errResult("target_id required for switch_tab"), nil
		}
		err = sess.SwitchTab(ctx, input.TargetID)
	case "close_tab":
		if input.TargetID == "" {
			return errResult("target_id required for close_tab"), nil
		}
		err = sess.CloseTab(ctx, input.TargetID)
	default:
		return errResult("unknown action: %s", input.Action), nil
	}

	if err != nil {
		return classifyNavigateError(err, "navigate "+input.Action), nil
	}
	return okResult(map[string]string{"status": "ok", "action": input.Action}), nil
}

// ---------------------------------------------------------------------------
// 3. browser.click — Click element
// ---------------------------------------------------------------------------

type browserClickTool struct{ holder *browserSessionHolder }

func (t *browserClickTool) Name() string { return "browser.click" }
func (t *browserClickTool) Risk() Risk   { return RiskMedium }
func (t *browserClickTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Click an element. Preferred: pass id from browser.snapshot. Falls back to CSS selector or explicit x,y coordinates.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector (fallback when no id)" },
    "x":        { "type": "number",  "description": "X coordinate (fallback when no id/selector)" },
    "y":        { "type": "number",  "description": "Y coordinate (fallback when no id/selector)" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int      `json:"id"`
		Selector string   `json:"selector"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	x, y, err := resolveTarget(ctx, sess, input.ID, input.Selector, input.X, input.Y)
	if err != nil {
		return errResult("%v", err), nil
	}

	if err := dispatchMouseClick(ctx, sess, x, y, "left", 1); err != nil {
		return errResult("click: %v", err), nil
	}
	return okResult(map[string]interface{}{"status": "ok", "x": x, "y": y}), nil
}

// ---------------------------------------------------------------------------
// 4. browser.double_click
// ---------------------------------------------------------------------------

type browserDoubleClickTool struct{ holder *browserSessionHolder }

func (t *browserDoubleClickTool) Name() string { return "browser.double_click" }
func (t *browserDoubleClickTool) Risk() Risk   { return RiskMedium }
func (t *browserDoubleClickTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Double-click an element. Preferred: pass id from browser.snapshot.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector (fallback)" },
    "x":        { "type": "number",  "description": "X coordinate (fallback)" },
    "y":        { "type": "number",  "description": "Y coordinate (fallback)" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserDoubleClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int      `json:"id"`
		Selector string   `json:"selector"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	x, y, err := resolveTarget(ctx, sess, input.ID, input.Selector, input.X, input.Y)
	if err != nil {
		return errResult("%v", err), nil
	}

	if err := dispatchMouseClick(ctx, sess, x, y, "left", 2); err != nil {
		return errResult("double_click: %v", err), nil
	}
	return okResult(map[string]interface{}{"status": "ok", "x": x, "y": y}), nil
}

// ---------------------------------------------------------------------------
// 5. browser.right_click
// ---------------------------------------------------------------------------

type browserRightClickTool struct{ holder *browserSessionHolder }

func (t *browserRightClickTool) Name() string { return "browser.right_click" }
func (t *browserRightClickTool) Risk() Risk   { return RiskMedium }
func (t *browserRightClickTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Right-click an element. Preferred: pass id from browser.snapshot.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector (fallback)" },
    "x":        { "type": "number",  "description": "X coordinate (fallback)" },
    "y":        { "type": "number",  "description": "Y coordinate (fallback)" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserRightClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int      `json:"id"`
		Selector string   `json:"selector"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	x, y, err := resolveTarget(ctx, sess, input.ID, input.Selector, input.X, input.Y)
	if err != nil {
		return errResult("%v", err), nil
	}

	if err := dispatchMouseClick(ctx, sess, x, y, "right", 1); err != nil {
		return errResult("right_click: %v", err), nil
	}
	return okResult(map[string]interface{}{"status": "ok", "x": x, "y": y}), nil
}

// ---------------------------------------------------------------------------
// 6. browser.type — Type text into an element
// ---------------------------------------------------------------------------

type browserTypeTool struct{ holder *browserSessionHolder }

func (t *browserTypeTool) Name() string { return "browser.type" }
func (t *browserTypeTool) Risk() Risk   { return RiskMedium }
func (t *browserTypeTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Type text into an element. Preferred: pass id from browser.snapshot. Falls back to selector or the currently focused element.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "text":     { "type": "string",  "description": "Text to type" },
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector to focus before typing (fallback)" },
    "clear":    { "type": "boolean", "description": "Clear existing content before typing (default: false)" },
    "delay_ms": { "type": "integer", "description": "Delay between keystrokes in ms (default: 0)" }
  },
  "required": ["text"]
}`),
		OutputSchema: browserTypeOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserTypeTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Text     string `json:"text"`
		ID       int    `json:"id"`
		Selector string `json:"selector"`
		Clear    bool   `json:"clear"`
		DelayMS  int    `json:"delay_ms"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	// Focus by id > selector. Falls back to currently focused element.
	if input.ID > 0 {
		if err := focusBrainID(ctx, sess, input.ID); err != nil {
			return errResult("focus id=%d: %v", input.ID, err), nil
		}
	} else if input.Selector != "" {
		if err := focusElement(ctx, sess, input.Selector); err != nil {
			return errResult("focus %s: %v", input.Selector, err), nil
		}
	}

	// Clear existing content.
	if input.Clear {
		// Select all + delete.
		dispatchKeyEvent(ctx, sess, "rawKeyDown", "a", 65, KeyModCtrl)
		dispatchKeyEvent(ctx, sess, "keyUp", "a", 65, KeyModCtrl)
		dispatchKeyEvent(ctx, sess, "rawKeyDown", "Backspace", 8, 0)
		dispatchKeyEvent(ctx, sess, "keyUp", "Backspace", 8, 0)
	}

	// Type each character.
	delay := time.Duration(input.DelayMS) * time.Millisecond
	for _, ch := range input.Text {
		if err := sess.Exec(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
			"type": "char",
			"text": string(ch),
		}, nil); err != nil {
			return errResult("type char %c: %v", ch, err), nil
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}

	return okResult(map[string]interface{}{"status": "ok", "typed": len([]rune(input.Text))}), nil
}

// ---------------------------------------------------------------------------
// 7. browser.press_key — Press a key or key combination
// ---------------------------------------------------------------------------

type browserPressKeyTool struct{ holder *browserSessionHolder }

func (t *browserPressKeyTool) Name() string { return "browser.press_key" }
func (t *browserPressKeyTool) Risk() Risk   { return RiskSafe }
func (t *browserPressKeyTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Press a key or key combination (e.g. Enter, Tab, Escape, Ctrl+A, Ctrl+C, Ctrl+V, ArrowDown).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "key": { "type": "string", "description": "Key name: Enter, Tab, Escape, Backspace, Delete, ArrowUp, ArrowDown, ArrowLeft, ArrowRight, Home, End, PageUp, PageDown, F1-F12, or single character" },
    "modifiers": { "type": "array", "items": { "type": "string" }, "description": "Modifier keys: Ctrl, Alt, Shift, Meta" }
  },
  "required": ["key"]
}`),
		OutputSchema: browserKeyOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserPressKeyTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Key       string   `json:"key"`
		Modifiers []string `json:"modifiers"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.Key == "" {
		return errResult("key is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	mod := 0
	for _, m := range input.Modifiers {
		switch m {
		case "Ctrl", "Control":
			mod |= KeyModCtrl
		case "Alt":
			mod |= KeyModAlt
		case "Shift":
			mod |= KeyModShift
		case "Meta", "Cmd", "Command":
			mod |= KeyModMeta
		}
	}

	keyCode := resolveKeyCode(input.Key)
	dispatchKeyEvent(ctx, sess, "rawKeyDown", input.Key, keyCode, mod)
	dispatchKeyEvent(ctx, sess, "keyUp", input.Key, keyCode, mod)

	return okResult(map[string]string{"status": "ok", "key": input.Key}), nil
}

// ---------------------------------------------------------------------------
// 8. browser.scroll — Scroll page or element
// ---------------------------------------------------------------------------

type browserScrollTool struct{ holder *browserSessionHolder }

func (t *browserScrollTool) Name() string { return "browser.scroll" }
func (t *browserScrollTool) Risk() Risk   { return RiskSafe }
func (t *browserScrollTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Scroll the page or an element. Supports directional and pixel-based scrolling.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "direction": { "type": "string", "description": "up, down, left, right (scrolls by ~3 viewport units)" },
    "delta_x": { "type": "number", "description": "Horizontal scroll pixels (positive=right)" },
    "delta_y": { "type": "number", "description": "Vertical scroll pixels (positive=down)" },
    "selector": { "type": "string", "description": "CSS selector to scroll within (default: page)" },
    "x": { "type": "number", "description": "X position for scroll event (default: center)" },
    "y": { "type": "number", "description": "Y position for scroll event (default: center)" }
  }
}`),
		OutputSchema: browserScrollOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserScrollTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Direction string   `json:"direction"`
		DeltaX    *float64 `json:"delta_x"`
		DeltaY    *float64 `json:"delta_y"`
		Selector  string   `json:"selector"`
		X         *float64 `json:"x"`
		Y         *float64 `json:"y"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	// Resolve scroll amounts.
	dx, dy := 0.0, 0.0
	if input.DeltaX != nil {
		dx = *input.DeltaX
	}
	if input.DeltaY != nil {
		dy = *input.DeltaY
	}
	switch input.Direction {
	case "up":
		dy = -400
	case "down":
		dy = 400
	case "left":
		dx = -400
	case "right":
		dx = 400
	}

	// Resolve position.
	sx, sy := 400.0, 300.0
	if input.X != nil {
		sx = *input.X
	}
	if input.Y != nil {
		sy = *input.Y
	}
	if input.Selector != "" {
		cx, cy, sErr := getElementCenter(ctx, sess, input.Selector)
		if sErr == nil {
			sx, sy = cx, cy
		}
	}

	if err := sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type":   "mouseWheel",
		"x":      sx,
		"y":      sy,
		"deltaX": dx,
		"deltaY": dy,
	}, nil); err != nil {
		return errResult("scroll: %v", err), nil
	}

	return okResult(map[string]interface{}{"status": "ok", "delta_x": dx, "delta_y": dy}), nil
}

// ---------------------------------------------------------------------------
// 9. browser.hover — Move mouse over element
// ---------------------------------------------------------------------------

type browserHoverTool struct{ holder *browserSessionHolder }

func (t *browserHoverTool) Name() string { return "browser.hover" }
func (t *browserHoverTool) Risk() Risk   { return RiskSafe }
func (t *browserHoverTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Hover over an element (triggers :hover, tooltips, dropdowns). Preferred: pass id from browser.snapshot.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector (fallback)" },
    "x":        { "type": "number",  "description": "X coordinate (fallback)" },
    "y":        { "type": "number",  "description": "Y coordinate (fallback)" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserHoverTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int      `json:"id"`
		Selector string   `json:"selector"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	x, y, err := resolveTarget(ctx, sess, input.ID, input.Selector, input.X, input.Y)
	if err != nil {
		return errResult("%v", err), nil
	}

	if err := sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mouseMoved",
		"x":    x,
		"y":    y,
	}, nil); err != nil {
		return errResult("hover: %v", err), nil
	}

	return okResult(map[string]interface{}{"status": "ok", "x": x, "y": y}), nil
}

// ---------------------------------------------------------------------------
// 10. browser.drag — Drag and drop
// ---------------------------------------------------------------------------

type browserDragTool struct{ holder *browserSessionHolder }

type browserDragInput struct {
	FromID       int      `json:"from_id"`
	FromSelector string   `json:"from_selector"`
	ToID         int      `json:"to_id"`
	ToSelector   string   `json:"to_selector"`
	FromX        *float64 `json:"from_x"`
	FromY        *float64 `json:"from_y"`
	ToX          *float64 `json:"to_x"`
	ToY          *float64 `json:"to_y"`
	Strategy     string   `json:"strategy"`
	Steps        int      `json:"steps"`
	Human        *bool    `json:"human"`
}

func (t *browserDragTool) Name() string { return "browser.drag" }
func (t *browserDragTool) Risk() Risk   { return RiskMedium }
func (t *browserDragTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Drag an element from one position to another. Supports ids from browser.snapshot, selectors, or explicit coordinates. Can auto-compute end coordinates for slider-like targets.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from_id": { "type": "integer", "description": "data-brain-id of element to drag (preferred over selector)" },
    "from_selector": { "type": "string", "description": "CSS selector of element to drag" },
    "to_id": { "type": "integer", "description": "data-brain-id of drop target / slider track (preferred over selector)" },
    "to_selector": { "type": "string", "description": "CSS selector of drop target" },
    "from_x": { "type": "number" }, "from_y": { "type": "number" },
    "to_x": { "type": "number" }, "to_y": { "type": "number" },
    "strategy": { "type": "string", "description": "auto (default), center, or slider. auto infers slider-like targets and computes a more realistic end point." },
    "steps": { "type": "integer", "description": "Number of intermediate mouse move steps (default: 10)" }
  }
}`),
		OutputSchema: browserDragOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserDragTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input browserDragInput
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	fromX, fromY, fromGeom, err := resolveDragPoint(ctx, sess, input.FromID, input.FromSelector, input.FromX, input.FromY)
	if err != nil {
		return errResult("from: %v", err), nil
	}
	toX, toY, toGeom, err := resolveDragDestination(ctx, sess, input, fromGeom)
	if err != nil {
		return errResult("to: %v", err), nil
	}

	// 默认启用人类化轨迹(抖动 + 加速减速 + 随机步长时间),对抗
	// 滑块拼图验证码的 bot 检测。传 "human": false 禁用。
	human := true
	if input.Human != nil {
		human = *input.Human
	}

	steps := input.Steps
	if steps <= 0 {
		if human {
			steps = 35 + rand.Intn(20) // 35-54 步,更像人类
		} else {
			steps = 10
		}
	}

	// Mouse down at source.
	sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mousePressed", "x": fromX, "y": fromY, "button": "left", "clickCount": 1,
	}, nil)

	// 按下后短暂停顿(人类反应时间),再开始移动。
	if human {
		time.Sleep(time.Duration(80+rand.Intn(120)) * time.Millisecond)
	}

	dx := toX - fromX
	dy := toY - fromY
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		var frac float64
		if human {
			// easeInOutQuad:先加速后减速,模拟人类"快速启动、到目标
			// 附近再精细微调"的动作曲线。
			if t < 0.5 {
				frac = 2 * t * t
			} else {
				frac = 1 - math.Pow(-2*t+2, 2)/2
			}
		} else {
			frac = t
		}
		mx := fromX + dx*frac
		my := fromY + dy*frac
		if human {
			// Y 轴微抖动(±2 像素),X 轴尾部微微过冲后回拉。
			mx += (rand.Float64() - 0.5) * 1.2
			my += (rand.Float64() - 0.5) * 2.5
		}
		sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type": "mouseMoved", "x": mx, "y": my, "button": "left",
		}, nil)
		if human {
			time.Sleep(time.Duration(8+rand.Intn(22)) * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// 到达终点后短停顿再释放(人类确认位置),模拟真实松手动作。
	if human {
		time.Sleep(time.Duration(100+rand.Intn(200)) * time.Millisecond)
	}

	// Mouse up at destination.
	sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mouseReleased", "x": toX, "y": toY, "button": "left", "clickCount": 1,
	}, nil)

	postCheck := inspectPostDragState(ctx, sess, input, fromGeom, toGeom, toX, toY)

	return okResult(map[string]interface{}{
		"status": "ok", "from": [2]float64{fromX, fromY}, "to": [2]float64{toX, toY},
		"human": human, "steps": steps, "post_check": postCheck,
	}), nil
}

// ---------------------------------------------------------------------------
// 11. browser.geometry — Read an element's box / center / edges
// ---------------------------------------------------------------------------

type browserGeometryTool struct{ holder *browserSessionHolder }

func (t *browserGeometryTool) Name() string { return "browser.geometry" }
func (t *browserGeometryTool) Risk() Risk   { return RiskSafe }
func (t *browserGeometryTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Read an element's geometry (bounding box, edges, center). Preferred: pass id from browser.snapshot. Useful before browser.drag for slider CAPTCHA.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector of the element (fallback)" }
  }
}`),
		OutputSchema: browserGeometryOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.inspect",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserGeometryTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int    `json:"id"`
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.ID <= 0 && strings.TrimSpace(input.Selector) == "" {
		return errResult("either id (from browser.snapshot) or selector is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	box, target, err := resolveElementGeometry(ctx, sess, input.ID, input.Selector)
	if err != nil {
		return errResult("%v", err), nil
	}
	return okResult(map[string]interface{}{
		"status": "ok",
		"target": target,
		"box": map[string]float64{
			"x":        box.X,
			"y":        box.Y,
			"width":    box.Width,
			"height":   box.Height,
			"left":     box.Left,
			"top":      box.Top,
			"right":    box.Right,
			"bottom":   box.Bottom,
			"center_x": box.CenterX,
			"center_y": box.CenterY,
		},
	}), nil
}

// ---------------------------------------------------------------------------
// 12. browser.select — Select dropdown option
// ---------------------------------------------------------------------------

type browserSelectTool struct{ holder *browserSessionHolder }

func (t *browserSelectTool) Name() string { return "browser.select" }
func (t *browserSelectTool) Risk() Risk   { return RiskMedium }
func (t *browserSelectTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Select an option from a <select> dropdown by value, text, or index. Preferred: pass id from browser.snapshot.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id":       { "type": "integer", "description": "data-brain-id from browser.snapshot (preferred)" },
    "selector": { "type": "string",  "description": "CSS selector of the <select> element (fallback)" },
    "value":    { "type": "string",  "description": "Option value to select" },
    "text":     { "type": "string",  "description": "Option visible text to select" },
    "index":    { "type": "integer", "description": "Option index to select (0-based)" }
  }
}`),
		OutputSchema: browserSelectOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserSelectTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		ID       int    `json:"id"`
		Selector string `json:"selector"`
		Value    string `json:"value"`
		Text     string `json:"text"`
		Index    *int   `json:"index"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.ID <= 0 && input.Selector == "" {
		return errResult("either id (from browser.snapshot) or selector is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	// Prefer id → [data-brain-id="N"] lookup; else the caller's selector.
	targetSel := input.Selector
	if input.ID > 0 {
		targetSel = fmt.Sprintf(`[data-brain-id="%d"]`, input.ID)
	}

	// Build JS to select the option.
	var js string
	switch {
	case input.Value != "":
		js = fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if(!el) return JSON.stringify({error:"element not found"});
			el.value = %q;
			el.dispatchEvent(new Event('change',{bubbles:true}));
			return JSON.stringify({status:"ok",value:el.value});
		})()`, targetSel, input.Value)
	case input.Text != "":
		js = fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if(!el) return JSON.stringify({error:"element not found"});
			for(var i=0;i<el.options.length;i++){
				if(el.options[i].text===%q){el.selectedIndex=i;break;}
			}
			el.dispatchEvent(new Event('change',{bubbles:true}));
			return JSON.stringify({status:"ok",value:el.value,text:el.options[el.selectedIndex].text});
		})()`, targetSel, input.Text)
	case input.Index != nil:
		js = fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if(!el) return JSON.stringify({error:"element not found"});
			el.selectedIndex = %d;
			el.dispatchEvent(new Event('change',{bubbles:true}));
			return JSON.stringify({status:"ok",index:%d,value:el.value});
		})()`, targetSel, *input.Index, *input.Index)
	default:
		return errResult("one of value, text, or index is required"), nil
	}

	return evalJS(ctx, sess, js)
}

// ---------------------------------------------------------------------------
// 12. browser.upload_file — Upload file to input
// ---------------------------------------------------------------------------

type browserUploadFileTool struct{ holder *browserSessionHolder }

func (t *browserUploadFileTool) Name() string { return "browser.upload_file" }
func (t *browserUploadFileTool) Risk() Risk   { return RiskMedium }
func (t *browserUploadFileTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Upload a file to a <input type='file'> element.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector of the file input" },
    "files": { "type": "array", "items": { "type": "string" }, "description": "Absolute paths of files to upload" }
  },
  "required": ["selector", "files"]
}`),
		OutputSchema: browserUploadOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserUploadFileTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Selector string   `json:"selector"`
		Files    []string `json:"files"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.Selector == "" || len(input.Files) == 0 {
		return errResult("selector and files are required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	// Get DOM node for the selector.
	nodeID, err := querySelector(ctx, sess, input.Selector)
	if err != nil {
		return errResult("query %s: %v", input.Selector, err), nil
	}

	if err := sess.Exec(ctx, "DOM.setFileInputFiles", map[string]interface{}{
		"nodeId": nodeID,
		"files":  input.Files,
	}, nil); err != nil {
		return errResult("upload: %v", err), nil
	}

	return okResult(map[string]interface{}{"status": "ok", "files": input.Files}), nil
}

// ---------------------------------------------------------------------------
// 13. browser.screenshot — Capture screenshot
// ---------------------------------------------------------------------------

type browserScreenshotTool struct{ holder *browserSessionHolder }

func (t *browserScreenshotTool) Name() string { return "browser.screenshot" }
func (t *browserScreenshotTool) Risk() Risk   { return RiskSafe }
func (t *browserScreenshotTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Capture a screenshot of the page or a specific element. Returns base64-encoded PNG.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector to screenshot (default: full page)" },
    "full_page": { "type": "boolean", "description": "Capture entire scrollable page (default: false, viewport only)" },
    "quality": { "type": "integer", "description": "JPEG quality 0-100 (default: PNG)" },
    "format": { "type": "string", "description": "Image format: png (default) or jpeg" }
  }
}`),
		OutputSchema: browserScreenshotOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.screenshot",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-read",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

func (t *browserScreenshotTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Selector string `json:"selector"`
		FullPage bool   `json:"full_page"`
		Quality  *int   `json:"quality"`
		Format   string `json:"format"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	params := map[string]interface{}{}
	format := "png"
	if input.Format == "jpeg" {
		params["format"] = "jpeg"
		format = "jpeg"
		if input.Quality != nil {
			params["quality"] = *input.Quality
		}
	} else {
		params["format"] = "png"
	}

	if input.FullPage {
		// Get full page dimensions.
		var layout struct {
			ContentSize struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"contentSize"`
		}
		if err := sess.Exec(ctx, "Page.getLayoutMetrics", nil, &layout); err == nil {
			params["clip"] = map[string]interface{}{
				"x": 0, "y": 0,
				"width":  layout.ContentSize.Width,
				"height": layout.ContentSize.Height,
				"scale":  1,
			}
			params["captureBeyondViewport"] = true
		}
	} else if input.Selector != "" {
		// Get element bounding box.
		box, bErr := getElementBox(ctx, sess, input.Selector)
		if bErr != nil {
			return errResult("element box: %v", bErr), nil
		}
		params["clip"] = map[string]interface{}{
			"x": box[0], "y": box[1],
			"width": box[2], "height": box[3],
			"scale": 1,
		}
	}

	var result struct {
		Data string `json:"data"` // base64-encoded image
	}
	if err := sess.Exec(ctx, "Page.captureScreenshot", params, &result); err != nil {
		return errResult("screenshot: %v", err), nil
	}

	return okResult(map[string]interface{}{
		"status":   "ok",
		"format":   format,
		"data":     result.Data,
		"encoding": "base64",
	}), nil
}

// ---------------------------------------------------------------------------
// 14. browser.eval — Execute JavaScript
// ---------------------------------------------------------------------------

type browserEvalTool struct{ holder *browserSessionHolder }

func (t *browserEvalTool) Name() string { return "browser.eval" }
func (t *browserEvalTool) Risk() Risk   { return RiskHigh }
func (t *browserEvalTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Execute JavaScript in the page context and return the result. Can query DOM, read state, or interact with page APIs.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "expression": { "type": "string", "description": "JavaScript expression to evaluate" },
    "await_promise": { "type": "boolean", "description": "Wait for promise to resolve (default: true)" }
  },
  "required": ["expression"]
}`),
		OutputSchema: browserEvalOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.evaluate",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "exclusive-session",
			Scope:               "turn",
			ApprovalClass:       "external-network",
		},
	}
}

func (t *browserEvalTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Expression   string `json:"expression"`
		AwaitPromise *bool  `json:"await_promise"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.Expression == "" {
		return errResult("expression is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	awaitPromise := true
	if input.AwaitPromise != nil {
		awaitPromise = *input.AwaitPromise
	}

	var result struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
			Desc  string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":            input.Expression,
		"awaitPromise":          awaitPromise,
		"returnByValue":         true,
		"includeCommandLineAPI": true,
	}, &result); err != nil {
		return errResult("eval: %v", err), nil
	}

	if result.ExceptionDetails != nil {
		return errResult("JS error: %s", result.ExceptionDetails.Text), nil
	}

	return okResult(map[string]interface{}{
		"type":  result.Result.Type,
		"value": result.Result.Value,
	}), nil
}

// ---------------------------------------------------------------------------
// 15. browser.wait — Wait for condition
// ---------------------------------------------------------------------------

type browserWaitTool struct{ holder *browserSessionHolder }

func (t *browserWaitTool) Name() string { return "browser.wait" }
func (t *browserWaitTool) Risk() Risk   { return RiskSafe }
func (t *browserWaitTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Wait for a condition: element appears/disappears, page loads, or custom JS predicate.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "condition": { "type": "string", "description": "visible, hidden, load, idle, or js" },
    "selector": { "type": "string", "description": "CSS selector (for visible/hidden)" },
    "expression": { "type": "string", "description": "JS expression that returns truthy (for js condition)" },
    "timeout_ms": { "type": "integer", "description": "Max wait time in ms (default: 10000)" }
  },
  "required": ["condition"]
}`),
		OutputSchema: browserWaitOutputSchema,
		Brain:        "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.interact",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-read",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

func (t *browserWaitTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Condition  string `json:"condition"`
		Selector   string `json:"selector"`
		Expression string `json:"expression"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	timeout := time.Duration(input.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch input.Condition {
	case "visible":
		if input.Selector == "" {
			return errResult("selector required for visible condition"), nil
		}
		err = pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
			js := fmt.Sprintf(`(function(){
				var el = document.querySelector(%q);
				if(!el) return false;
				var r = el.getBoundingClientRect();
				return r.width > 0 && r.height > 0;
			})()`, input.Selector)
			return evalBool(waitCtx, sess, js)
		})

	case "hidden":
		if input.Selector == "" {
			return errResult("selector required for hidden condition"), nil
		}
		err = pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
			js := fmt.Sprintf(`(function(){
				var el = document.querySelector(%q);
				if(!el) return true;
				var r = el.getBoundingClientRect();
				return r.width === 0 || r.height === 0;
			})()`, input.Selector)
			return evalBool(waitCtx, sess, js)
		})

	case "load":
		err = pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
			return evalBool(waitCtx, sess, `document.readyState === "complete"`)
		})

	case "idle":
		// Legacy "idle" — now backed by real network observation from netbuf
		// (quiet period default 500ms). Prefer wait.network_idle for new code.
		err = waitNetworkIdle(waitCtx, t.holder.netbuf, 500*time.Millisecond)

	case "js":
		if input.Expression == "" {
			return errResult("expression required for js condition"), nil
		}
		err = pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
			return evalBool(waitCtx, sess, input.Expression)
		})

	default:
		return errResult("unknown condition: %s (use visible, hidden, load, idle, or js)", input.Condition), nil
	}

	if err != nil {
		return ErrorResult(brainerrors.CodeToolTimeout, "wait %s: %v", input.Condition, err), nil
	}
	return okResult(map[string]string{"status": "ok", "condition": input.Condition}), nil
}

// ===========================================================================
// Shared helpers
// ===========================================================================

// errResult creates an error Result with formatted message.
func errResult(format string, a ...interface{}) *Result {
	msg := fmt.Sprintf(format, a...)
	return &Result{Output: jsonStr(msg), IsError: true}
}

// classifyNavigateError 把 CDP / 网络层错误归到 brain-v3 的 error_code,让
// retry 装饰器能正确决策。超时/网络抖动归 CodeToolTimeout(transient),其余
// 归 CodeToolExecutionFailed(permanent)。
func classifyNavigateError(err error, phase string) *Result {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"),
		strings.Contains(msg, "deadline"),
		strings.Contains(msg, "reset"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "temporarily unavailable"),
		strings.Contains(msg, "net::err_internet_disconnected"),
		strings.Contains(msg, "net::err_network_changed"):
		return ErrorResult(brainerrors.CodeToolTimeout, "%s: %v", phase, err)
	}
	return ErrorResult(brainerrors.CodeToolExecutionFailed, "%s: %v", phase, err)
}

// okResult creates a success Result from any JSON-serializable value.
func okResult(v interface{}) *Result {
	data, _ := json.Marshal(v)
	return &Result{Output: data}
}

// resolveCoordinates gets x,y from selector or explicit coordinates.
func resolveCoordinates(ctx context.Context, sess *cdp.BrowserSession, selector string, x, y *float64) (float64, float64, error) {
	if selector != "" {
		cx, cy, err := getElementCenter(ctx, sess, selector)
		if err != nil {
			return 0, 0, fmt.Errorf("element %q: %v", selector, err)
		}
		return cx, cy, nil
	}
	if x != nil && y != nil {
		return *x, *y, nil
	}
	return 0, 0, fmt.Errorf("provide id, selector, or both x,y coordinates")
}

// resolveTarget is the snapshot-aware variant of resolveCoordinates.
// Priority: id (from browser.snapshot) > selector > explicit x,y.
// Preferred by new code; older tools still call resolveCoordinates for compatibility.
func resolveTarget(ctx context.Context, sess *cdp.BrowserSession, id int, selector string, x, y *float64) (float64, float64, error) {
	if id > 0 {
		return resolveBrainID(ctx, sess, id)
	}
	return resolveCoordinates(ctx, sess, selector, x, y)
}

// getElementCenter returns the center coordinates of an element.
func getElementCenter(ctx context.Context, sess *cdp.BrowserSession, selector string) (float64, float64, error) {
	js := fmt.Sprintf(`(function(){
		var el = document.querySelector(%q);
		if(!el) return null;
		var r = el.getBoundingClientRect();
		return JSON.stringify({x: r.x + r.width/2, y: r.y + r.height/2});
	})()`, selector)

	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return 0, 0, err
	}

	var pos struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}

	// The value might be a string (from JSON.stringify) or null.
	var strVal string
	if json.Unmarshal(result.Result.Value, &strVal) == nil {
		if err := json.Unmarshal([]byte(strVal), &pos); err != nil {
			return 0, 0, fmt.Errorf("parse position: %v", err)
		}
		return pos.X, pos.Y, nil
	}

	return 0, 0, fmt.Errorf("element %q not found", selector)
}

// getElementBox returns [x, y, width, height] of an element.
func getElementBox(ctx context.Context, sess *cdp.BrowserSession, selector string) ([4]float64, error) {
	js := fmt.Sprintf(`(function(){
		var el = document.querySelector(%q);
		if(!el) return null;
		var r = el.getBoundingClientRect();
		return JSON.stringify({x:r.x,y:r.y,w:r.width,h:r.height});
	})()`, selector)

	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return [4]float64{}, err
	}

	var strVal string
	if json.Unmarshal(result.Result.Value, &strVal) == nil {
		var box struct {
			X, Y, W, H float64
		}
		if json.Unmarshal([]byte(strVal), &box) == nil {
			return [4]float64{box.X, box.Y, box.W, box.H}, nil
		}
	}
	return [4]float64{}, fmt.Errorf("element %q not found", selector)
}

type elementGeometry struct {
	X       float64
	Y       float64
	Width   float64
	Height  float64
	Left    float64
	Top     float64
	Right   float64
	Bottom  float64
	CenterX float64
	CenterY float64
}

func resolveElementGeometry(ctx context.Context, sess *cdp.BrowserSession, id int, selector string) (elementGeometry, string, error) {
	switch {
	case id > 0:
		box, err := getBrainIDBox(ctx, sess, id)
		if err != nil {
			return elementGeometry{}, "", err
		}
		return normalizeElementBox(box), fmt.Sprintf("[data-brain-id=%d]", id), nil
	case strings.TrimSpace(selector) != "":
		box, err := getElementBox(ctx, sess, selector)
		if err != nil {
			return elementGeometry{}, "", err
		}
		return normalizeElementBox(box), selector, nil
	default:
		return elementGeometry{}, "", fmt.Errorf("provide id or selector")
	}
}

func normalizeElementBox(box [4]float64) elementGeometry {
	x := box[0]
	y := box[1]
	w := box[2]
	h := box[3]
	return elementGeometry{
		X:       x,
		Y:       y,
		Width:   w,
		Height:  h,
		Left:    x,
		Top:     y,
		Right:   x + w,
		Bottom:  y + h,
		CenterX: x + w/2,
		CenterY: y + h/2,
	}
}

func inferSliderTrackGeometry(ctx context.Context, sess *cdp.BrowserSession, id int, selector string) (*elementGeometry, error) {
	sourceSelector := ""
	switch {
	case id > 0:
		sourceSelector = fmt.Sprintf(`[data-brain-id="%d"]`, id)
	case strings.TrimSpace(selector) != "":
		sourceSelector = selector
	default:
		return nil, fmt.Errorf("cannot infer target without source id or selector")
	}

	js := fmt.Sprintf(`(function(){
		var source = document.querySelector(%q);
		if(!source) return null;
		var src = source.getBoundingClientRect();
		var candidates = [];
		function pushCandidate(el, scoreBase) {
			if (!el || !el.getBoundingClientRect) return;
			var r = el.getBoundingClientRect();
			if (r.width <= 0 || r.height <= 0) return;
			if (r.width <= src.width * 1.5) return;
			var verticalGap = Math.abs((src.y + src.height/2) - (r.y + r.height/2));
			if (verticalGap > Math.max(src.height, r.height) * 1.5) return;
			var score = scoreBase + r.width - verticalGap * 2 - Math.abs(r.height - src.height);
			candidates.push({x:r.x,y:r.y,w:r.width,h:r.height,score:score});
		}
		var current = source;
		for (var depth = 0; depth < 5 && current; depth++) {
			var parent = current.parentElement;
			if (!parent) break;
			pushCandidate(parent, 100 - depth * 10);
			var children = parent.children || [];
			for (var i = 0; i < children.length; i++) {
				var child = children[i];
				if (child === current) continue;
				pushCandidate(child, 60 - depth * 10);
			}
			current = parent;
		}
		if (!candidates.length) return null;
		candidates.sort(function(a,b){ return b.score - a.score; });
		return JSON.stringify(candidates[0]);
	})()`, sourceSelector)

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
	var s string
	if err := json.Unmarshal(result.Result.Value, &s); err != nil || strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("could not infer slider target geometry from source")
	}
	var box struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := json.Unmarshal([]byte(s), &box); err != nil {
		return nil, fmt.Errorf("parse inferred target geometry: %v", err)
	}
	geom := normalizeElementBox([4]float64{box.X, box.Y, box.W, box.H})
	return &geom, nil
}

func resolveDragPoint(ctx context.Context, sess *cdp.BrowserSession, id int, selector string, x, y *float64) (float64, float64, *elementGeometry, error) {
	if x != nil && y != nil {
		return *x, *y, nil, nil
	}
	if id > 0 || strings.TrimSpace(selector) != "" {
		geom, _, err := resolveElementGeometry(ctx, sess, id, selector)
		if err != nil {
			return 0, 0, nil, err
		}
		return geom.CenterX, geom.CenterY, &geom, nil
	}
	return 0, 0, nil, fmt.Errorf("provide id, selector, or both x,y coordinates")
}

func resolveDragDestination(ctx context.Context, sess *cdp.BrowserSession, input browserDragInput, fromGeom *elementGeometry) (float64, float64, *elementGeometry, error) {
	if input.ToX != nil && input.ToY != nil {
		return *input.ToX, *input.ToY, nil, nil
	}
	strategy := strings.TrimSpace(strings.ToLower(input.Strategy))
	if strategy == "" {
		strategy = "auto"
	}
	var (
		toGeom *elementGeometry
		err    error
	)
	if input.ToID > 0 || strings.TrimSpace(input.ToSelector) != "" {
		var geom elementGeometry
		geom, _, err = resolveElementGeometry(ctx, sess, input.ToID, input.ToSelector)
		if err != nil {
			return 0, 0, nil, err
		}
		toGeom = &geom
	} else if strategy == "auto" || strategy == "slider" {
		toGeom, err = inferSliderTrackGeometry(ctx, sess, input.FromID, input.FromSelector)
		if err != nil && strategy == "slider" {
			return 0, 0, nil, err
		}
	}
	if toGeom == nil {
		return 0, 0, nil, fmt.Errorf("provide to target (id, selector, or x,y), or use strategy=auto/slider with a draggable source element")
	}
	x, y, err := computeDragDestination(strategy, fromGeom, toGeom)
	if err != nil {
		return 0, 0, nil, err
	}
	return x, y, toGeom, nil
}

func computeDragDestination(strategy string, fromGeom, toGeom *elementGeometry) (float64, float64, error) {
	if toGeom == nil {
		return 0, 0, fmt.Errorf("target geometry unavailable")
	}
	switch strategy {
	case "center":
		return toGeom.CenterX, toGeom.CenterY, nil
	case "slider":
		return computeSliderDestination(fromGeom, toGeom)
	case "auto":
		if looksLikeSliderDrag(fromGeom, toGeom) {
			return computeSliderDestination(fromGeom, toGeom)
		}
		return toGeom.CenterX, toGeom.CenterY, nil
	default:
		return 0, 0, fmt.Errorf("unknown strategy: %s (use auto, center, or slider)", strategy)
	}
}

func looksLikeSliderDrag(fromGeom, toGeom *elementGeometry) bool {
	if fromGeom == nil || toGeom == nil {
		return false
	}
	if toGeom.Width <= fromGeom.Width*1.5 {
		return false
	}
	if toGeom.Height <= 0 || fromGeom.Height <= 0 {
		return false
	}
	verticalGap := math.Abs(fromGeom.CenterY - toGeom.CenterY)
	return verticalGap <= math.Max(fromGeom.Height, toGeom.Height)
}

func computeSliderDestination(fromGeom, toGeom *elementGeometry) (float64, float64, error) {
	if fromGeom == nil || toGeom == nil {
		return 0, 0, fmt.Errorf("slider strategy requires both source and target geometry")
	}
	margin := math.Max(2, fromGeom.Width*0.08)
	x := toGeom.Right - fromGeom.Width/2 - margin
	minX := toGeom.Left + fromGeom.Width/2
	if x < minX {
		x = minX
	}
	y := fromGeom.CenterY
	minY := toGeom.Top + math.Min(fromGeom.Height, toGeom.Height)/2
	maxY := toGeom.Bottom - math.Min(fromGeom.Height, toGeom.Height)/2
	if maxY < minY {
		y = toGeom.CenterY
	} else {
		if y < minY {
			y = minY
		}
		if y > maxY {
			y = maxY
		}
	}
	return x, y, nil
}

func inspectPostDragState(ctx context.Context, sess *cdp.BrowserSession, input browserDragInput, fromGeom, toGeom *elementGeometry, expectedX, expectedY float64) map[string]interface{} {
	_ = toGeom
	check := map[string]interface{}{
		"verified":             false,
		"source_moved":         false,
		"movement_distance":    0.0,
		"distance_to_expected": -1.0,
		"success_hint":         false,
		"success_text":         "",
	}

	if fromGeom == nil {
		return check
	}

	// 给前端一点时间提交拖动后的状态更新。
	time.Sleep(180 * time.Millisecond)

	currentGeom, ok := rereadDragSourceGeometry(ctx, sess, input)
	if ok {
		movement := math.Hypot(currentGeom.CenterX-fromGeom.CenterX, currentGeom.CenterY-fromGeom.CenterY)
		distanceToExpected := math.Hypot(currentGeom.CenterX-expectedX, currentGeom.CenterY-expectedY)
		check["movement_distance"] = movement
		check["distance_to_expected"] = distanceToExpected
		sourceMoved := movement >= math.Max(4, fromGeom.Width*0.2)
		check["source_moved"] = sourceMoved
		if sourceMoved && distanceToExpected <= math.Max(12, fromGeom.Width) {
			check["verified"] = true
		}
	}

	if okHint, text := detectDragSuccessHint(ctx, sess); okHint {
		check["success_hint"] = true
		check["success_text"] = text
		check["verified"] = true
	}

	return check
}

func rereadDragSourceGeometry(ctx context.Context, sess *cdp.BrowserSession, input browserDragInput) (*elementGeometry, bool) {
	switch {
	case input.FromID > 0:
		geom, _, err := resolveElementGeometry(ctx, sess, input.FromID, "")
		if err == nil {
			return &geom, true
		}
	case strings.TrimSpace(input.FromSelector) != "":
		geom, _, err := resolveElementGeometry(ctx, sess, 0, input.FromSelector)
		if err == nil {
			return &geom, true
		}
	}
	return nil, false
}

func detectDragSuccessHint(ctx context.Context, sess *cdp.BrowserSession) (bool, string) {
	js := `(function(){
		var text = (document.body && document.body.innerText ? document.body.innerText : "").replace(/\s+/g, " ").trim();
		var hints = ["验证通过", "验证成功", "success", "verified", "passed"];
		for (var i = 0; i < hints.length; i++) {
			var idx = text.toLowerCase().indexOf(hints[i].toLowerCase());
			if (idx >= 0) {
				return JSON.stringify({ok:true, text:text.slice(Math.max(0, idx - 20), Math.min(text.length, idx + 40))});
			}
		}
		return JSON.stringify({ok:false, text:""});
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
		return false, ""
	}
	var s string
	if err := json.Unmarshal(result.Result.Value, &s); err != nil {
		return false, ""
	}
	var payload struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		return false, ""
	}
	return payload.OK, payload.Text
}

// focusElement focuses a DOM element by selector.
func focusElement(ctx context.Context, sess *cdp.BrowserSession, selector string) error {
	js := fmt.Sprintf(`(function(){
		var el = document.querySelector(%q);
		if(!el) return false;
		el.focus();
		return true;
	})()`, selector)
	ok, err := evalBool(ctx, sess, js)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("element %q not found", selector)
	}
	return nil
}

// querySelector returns the DOM nodeId for a selector.
func querySelector(ctx context.Context, sess *cdp.BrowserSession, selector string) (int64, error) {
	// Get document root.
	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := sess.Exec(ctx, "DOM.getDocument", map[string]interface{}{
		"depth": 0,
	}, &doc); err != nil {
		return 0, err
	}

	var result struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := sess.Exec(ctx, "DOM.querySelector", map[string]interface{}{
		"nodeId":   doc.Root.NodeID,
		"selector": selector,
	}, &result); err != nil {
		return 0, err
	}
	if result.NodeID == 0 {
		return 0, fmt.Errorf("element %q not found", selector)
	}
	return result.NodeID, nil
}

// dispatchMouseClick sends mousePressed + mouseReleased events.
func dispatchMouseClick(ctx context.Context, sess *cdp.BrowserSession, x, y float64, button string, clickCount int) error {
	if err := sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	}, nil); err != nil {
		return err
	}
	return sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
	}, nil)
}

// Key modifier flags for CDP Input.dispatchKeyEvent.
const (
	KeyModAlt   = 1
	KeyModCtrl  = 2
	KeyModMeta  = 4
	KeyModShift = 8
)

// dispatchKeyEvent sends a single key event.
func dispatchKeyEvent(ctx context.Context, sess *cdp.BrowserSession, eventType string, key string, keyCode int, modifiers int) error {
	params := map[string]interface{}{
		"type":                  eventType,
		"key":                   key,
		"windowsVirtualKeyCode": keyCode,
		"nativeVirtualKeyCode":  keyCode,
		"modifiers":             modifiers,
	}
	return sess.Exec(ctx, "Input.dispatchKeyEvent", params, nil)
}

// resolveKeyCode maps key names to virtual key codes.
func resolveKeyCode(key string) int {
	switch key {
	case "Enter":
		return 13
	case "Tab":
		return 9
	case "Escape":
		return 27
	case "Backspace":
		return 8
	case "Delete":
		return 46
	case "ArrowUp":
		return 38
	case "ArrowDown":
		return 40
	case "ArrowLeft":
		return 37
	case "ArrowRight":
		return 39
	case "Home":
		return 36
	case "End":
		return 35
	case "PageUp":
		return 33
	case "PageDown":
		return 34
	case "Space", " ":
		return 32
	case "F1":
		return 112
	case "F2":
		return 113
	case "F3":
		return 114
	case "F4":
		return 115
	case "F5":
		return 116
	case "F6":
		return 117
	case "F7":
		return 118
	case "F8":
		return 119
	case "F9":
		return 120
	case "F10":
		return 121
	case "F11":
		return 122
	case "F12":
		return 123
	default:
		if len(key) == 1 {
			ch := key[0]
			if ch >= 'a' && ch <= 'z' {
				return int(ch - 32) // uppercase ASCII
			}
			return int(ch)
		}
		return 0
	}
}

// evalBool evaluates JS and returns the boolean result.
func evalBool(ctx context.Context, sess *cdp.BrowserSession, js string) (bool, error) {
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
	var b bool
	json.Unmarshal(result.Result.Value, &b)
	return b, nil
}

// evalJS evaluates JS and returns the result as a tool Result.
func evalJS(ctx context.Context, sess *cdp.BrowserSession, js string) (*Result, error) {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return errResult("eval: %v", err), nil
	}
	if result.ExceptionDetails != nil {
		return errResult("JS error: %s", result.ExceptionDetails.Text), nil
	}

	// Try to parse the string value as JSON (many helpers return JSON.stringify).
	var strVal string
	if json.Unmarshal(result.Result.Value, &strVal) == nil {
		// Try parsing as JSON object.
		var obj interface{}
		if json.Unmarshal([]byte(strVal), &obj) == nil {
			return okResult(obj), nil
		}
		return okResult(strVal), nil
	}
	return &Result{Output: result.Result.Value}, nil
}

// pollUntil polls a condition function until it returns true or context expires.
func pollUntil(ctx context.Context, interval time.Duration, fn func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Check once immediately.
	ok, err := fn()
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for condition")
		case <-ticker.C:
			ok, err := fn()
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

// waitForLoad waits for page load with a timeout.
func waitForLoad(ctx context.Context, sess *cdp.BrowserSession, timeout time.Duration) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pollUntil(waitCtx, 200*time.Millisecond, func() (bool, error) {
		return evalBool(waitCtx, sess, `document.readyState === "complete"`)
	})
}
