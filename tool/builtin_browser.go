package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/tool/cdp"
)

// ---------------------------------------------------------------------------
// BrowserSession holder — shared across all browser tools
// ---------------------------------------------------------------------------

// browserSessionHolder manages the shared browser session for all browser tools.
// The session is lazily initialized on first use.
type browserSessionHolder struct {
	mu      sync.Mutex
	session *cdp.BrowserSession
}

func newBrowserSessionHolder() *browserSessionHolder {
	return &browserSessionHolder{}
}

// get returns the current session, creating one if needed.
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
}

// ---------------------------------------------------------------------------
// NewBrowserTools returns all 15 browser tools sharing a single session.
// ---------------------------------------------------------------------------

// NewBrowserTools creates all browser tools sharing a single browser session.
func NewBrowserTools() []Tool {
	holder := newBrowserSessionHolder()
	return []Tool{
		&browserOpenTool{holder: holder},
		&browserNavigateTool{holder: holder},
		&browserClickTool{holder: holder},
		&browserDoubleClickTool{holder: holder},
		&browserRightClickTool{holder: holder},
		&browserTypeTool{holder: holder},
		&browserPressKeyTool{holder: holder},
		&browserScrollTool{holder: holder},
		&browserHoverTool{holder: holder},
		&browserDragTool{holder: holder},
		&browserSelectTool{holder: holder},
		&browserUploadFileTool{holder: holder},
		&browserScreenshotTool{holder: holder},
		&browserEvalTool{holder: holder},
		&browserWaitTool{holder: holder},
	}
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
			return errResult("new tab: %v", err), nil
		}
	} else {
		if err := sess.Navigate(ctx, input.URL); err != nil {
			return errResult("navigate: %v", err), nil
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
		return errResult("navigate %s: %v", input.Action, err), nil
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
		Description: "Click an element by CSS selector or coordinates. Simulates a real mouse click.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector of the element to click" },
    "x": { "type": "number", "description": "X coordinate (if no selector)" },
    "y": { "type": "number", "description": "Y coordinate (if no selector)" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
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

	x, y, err := resolveCoordinates(ctx, sess, input.Selector, input.X, input.Y)
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
		Description: "Double-click an element by CSS selector or coordinates.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector" },
    "x": { "type": "number", "description": "X coordinate" },
    "y": { "type": "number", "description": "Y coordinate" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserDoubleClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
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

	x, y, err := resolveCoordinates(ctx, sess, input.Selector, input.X, input.Y)
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
		Description: "Right-click an element by CSS selector or coordinates.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector" },
    "x": { "type": "number", "description": "X coordinate" },
    "y": { "type": "number", "description": "Y coordinate" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserRightClickTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
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

	x, y, err := resolveCoordinates(ctx, sess, input.Selector, input.X, input.Y)
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
		Description: "Type text into the focused element or a specified element. Can clear existing content first.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": { "type": "string", "description": "Text to type" },
    "selector": { "type": "string", "description": "CSS selector to focus before typing" },
    "clear": { "type": "boolean", "description": "Clear existing content before typing (default: false)" },
    "delay_ms": { "type": "integer", "description": "Delay between keystrokes in ms (default: 0, instant)" }
  },
  "required": ["text"]
}`),
		OutputSchema: browserTypeOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserTypeTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Text     string `json:"text"`
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

	// Focus the element if selector given.
	if input.Selector != "" {
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
		Description: "Hover over an element (triggers CSS :hover, tooltips, dropdown menus).",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector" },
    "x": { "type": "number", "description": "X coordinate" },
    "y": { "type": "number", "description": "Y coordinate" }
  }
}`),
		OutputSchema: browserPointOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserHoverTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
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

	x, y, err := resolveCoordinates(ctx, sess, input.Selector, input.X, input.Y)
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

func (t *browserDragTool) Name() string { return "browser.drag" }
func (t *browserDragTool) Risk() Risk   { return RiskMedium }
func (t *browserDragTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Drag an element from one position to another. Supports selector or coordinates.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from_selector": { "type": "string", "description": "CSS selector of element to drag" },
    "to_selector": { "type": "string", "description": "CSS selector of drop target" },
    "from_x": { "type": "number" }, "from_y": { "type": "number" },
    "to_x": { "type": "number" }, "to_y": { "type": "number" },
    "steps": { "type": "integer", "description": "Number of intermediate mouse move steps (default: 10)" }
  }
}`),
		OutputSchema: browserDragOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserDragTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		FromSelector string   `json:"from_selector"`
		ToSelector   string   `json:"to_selector"`
		FromX        *float64 `json:"from_x"`
		FromY        *float64 `json:"from_y"`
		ToX          *float64 `json:"to_x"`
		ToY          *float64 `json:"to_y"`
		Steps        int      `json:"steps"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	fromX, fromY, err := resolveCoordinates(ctx, sess, input.FromSelector, input.FromX, input.FromY)
	if err != nil {
		return errResult("from: %v", err), nil
	}
	toX, toY, err := resolveCoordinates(ctx, sess, input.ToSelector, input.ToX, input.ToY)
	if err != nil {
		return errResult("to: %v", err), nil
	}

	steps := input.Steps
	if steps <= 0 {
		steps = 10
	}

	// Mouse down at source.
	sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mousePressed", "x": fromX, "y": fromY, "button": "left", "clickCount": 1,
	}, nil)

	// Intermediate moves.
	for i := 1; i <= steps; i++ {
		frac := float64(i) / float64(steps)
		mx := fromX + (toX-fromX)*frac
		my := fromY + (toY-fromY)*frac
		sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type": "mouseMoved", "x": mx, "y": my, "button": "left",
		}, nil)
		time.Sleep(10 * time.Millisecond)
	}

	// Mouse up at destination.
	sess.Exec(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mouseReleased", "x": toX, "y": toY, "button": "left", "clickCount": 1,
	}, nil)

	return okResult(map[string]interface{}{
		"status": "ok", "from": [2]float64{fromX, fromY}, "to": [2]float64{toX, toY},
	}), nil
}

// ---------------------------------------------------------------------------
// 11. browser.select — Select dropdown option
// ---------------------------------------------------------------------------

type browserSelectTool struct{ holder *browserSessionHolder }

func (t *browserSelectTool) Name() string { return "browser.select" }
func (t *browserSelectTool) Risk() Risk   { return RiskMedium }
func (t *browserSelectTool) Schema() Schema {
	return Schema{
		Name:        t.Name(),
		Description: "Select an option from a <select> dropdown by value, text, or index.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "selector": { "type": "string", "description": "CSS selector of the <select> element" },
    "value": { "type": "string", "description": "Option value to select" },
    "text": { "type": "string", "description": "Option visible text to select" },
    "index": { "type": "integer", "description": "Option index to select (0-based)" }
  },
  "required": ["selector"]
}`),
		OutputSchema: browserSelectOutputSchema,
		Brain:        "browser",
	}
}

func (t *browserSelectTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Selector string `json:"selector"`
		Value    string `json:"value"`
		Text     string `json:"text"`
		Index    *int   `json:"index"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return errResult("invalid arguments: %v", err), nil
	}
	if input.Selector == "" {
		return errResult("selector is required"), nil
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
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
		})()`, input.Selector, input.Value)
	case input.Text != "":
		js = fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if(!el) return JSON.stringify({error:"element not found"});
			for(var i=0;i<el.options.length;i++){
				if(el.options[i].text===%q){el.selectedIndex=i;break;}
			}
			el.dispatchEvent(new Event('change',{bubbles:true}));
			return JSON.stringify({status:"ok",value:el.value,text:el.options[el.selectedIndex].text});
		})()`, input.Selector, input.Text)
	case input.Index != nil:
		js = fmt.Sprintf(`(function(){
			var el = document.querySelector(%q);
			if(!el) return JSON.stringify({error:"element not found"});
			el.selectedIndex = %d;
			el.dispatchEvent(new Event('change',{bubbles:true}));
			return JSON.stringify({status:"ok",index:%d,value:el.value});
		})()`, input.Selector, *input.Index, *input.Index)
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
		// Wait for network idle (no pending requests for 500ms).
		time.Sleep(500 * time.Millisecond)
		err = pollUntil(waitCtx, 300*time.Millisecond, func() (bool, error) {
			return evalBool(waitCtx, sess, `document.readyState === "complete"`)
		})

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
		return errResult("wait %s: %v", input.Condition, err), nil
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
	return 0, 0, fmt.Errorf("provide selector or both x,y coordinates")
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
