package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

// browser.snapshot — fused accessibility + interactive element snapshot.
//
// See sdk/docs/39-Browser-Brain感知与嗅探增强设计.md §3.1.
//
// Output is a flat list of visible interactive elements, each tagged with a
// stable `data-brain-id` attribute. Subsequent click/type/hover calls can
// reference the element by `id` instead of writing CSS selectors.
//
// The accessibility tree (when mode=a11y|both) is produced from
// Accessibility.getFullAXTree; the interactive element list comes from an
// injected JS function that walks the DOM for common interactive selectors
// and normalizes visibility / bounding box / ARIA semantics.

// brainElement is the unified DOM+AX record returned by browser.snapshot.
type brainElement struct {
	ID         int    `json:"id"`
	Tag        string `json:"tag"`
	Role       string `json:"role"`
	Type       string `json:"type,omitempty"`
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	Href       string `json:"href,omitempty"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	W          int    `json:"w"`
	H          int    `json:"h"`
	InViewport bool   `json:"inViewport"`
	// AX-layer enrichment (present when mode includes accessibility)
	AxRole   string `json:"axRole,omitempty"`
	AxName   string `json:"axName,omitempty"`
	Focused  bool   `json:"focused,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Checked  bool   `json:"checked,omitempty"`
	Expanded bool   `json:"expanded,omitempty"`
	// Frame / visibility metadata
	Partial bool `json:"partial,omitempty"` // element extends beyond viewport
}

// brainInteractiveSelector 是 DOM 采集的共用选择器,snapshotJS / 增量 JS
// 共用一份,避免两边语义漂移。
const brainInteractiveSelector = `a,button,input,select,textarea,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[role=radio],[role=switch],[role=combobox],[role=listbox],[role=textbox],[onclick],[tabindex]:not([tabindex="-1"])`

// brainInstallObserverJS 在当前页安装一次 MutationObserver 和本侧全局
// __brainDirty = Set<Element>,用于 P3.4 增量 snapshot。重复注入是安全的:
// 再次调用会复用上次的 observer(通过 __brainObserverInstalled 标志判断)。
//
// 增量 snapshot 的契约:
//   1. 全量 snapshot 把 __brainDirty 清空;
//   2. observer 监听 childList/subtree/attributes,把变动 target 及其所有
//      匹配选择器的后代塞进 __brainDirty;
//   3. 增量 snapshot 只遍历 __brainDirty,给这些元素补 data-brain-id(沿用
//      全局 __brainNextID 自增),返回 {updated: [brainElement...]};
//   4. 调用方把 updated 合入上一次缓存,更新 inViewport / 坐标等易变字段。
const brainInstallObserverJS = `
(function(){
  if (window.__brainObserverInstalled) return "already";
  window.__brainObserverInstalled = true;
  window.__brainDirty = new Set();
  if (typeof window.__brainNextID !== 'number') window.__brainNextID = 0;
  var SEL = '` + brainInteractiveSelector + `';
  function pushIfInteractive(el){
    if (!(el instanceof Element)) return;
    if (el.matches && el.matches(SEL)) window.__brainDirty.add(el);
    // 子树里的交互元素也算脏
    if (el.querySelectorAll){
      el.querySelectorAll(SEL).forEach(function(c){ window.__brainDirty.add(c); });
    }
  }
  var mo = new MutationObserver(function(list){
    for (var i = 0; i < list.length; i++){
      var r = list[i];
      if (r.type === 'childList'){
        r.addedNodes.forEach(pushIfInteractive);
      } else if (r.type === 'attributes'){
        pushIfInteractive(r.target);
      }
    }
  });
  mo.observe(document.documentElement || document.body, {
    childList: true, subtree: true, attributes: true,
    attributeFilter: ['class','style','hidden','disabled','aria-hidden','value']
  });
  // 记录 observer 以便 reset 时拆除
  window.__brainObserver = mo;
  return "installed";
})();
`

// brainSnapshotJS — the DOM-walking interactive-element collector.
// See sdk/docs/39 §3.1. The `__brainSnapshot` global is re-entrant and
// idempotent: calling it twice re-labels the same elements with fresh IDs.
//
// 全量采集会把 __brainNextID 重新置 0 并清空 __brainDirty;增量 snapshot
// 依赖这里重置过的环境。
const brainSnapshotJS = `
(function(){
  var SEL = '` + brainInteractiveSelector + `';
  function visible(el){
    var r = el.getBoundingClientRect();
    var s = getComputedStyle(el);
    return r.width > 0 && r.height > 0
      && s.visibility !== 'hidden'
      && s.display !== 'none'
      && +s.opacity > 0;
  }
  function textOf(el){
    var aria = el.getAttribute('aria-label');
    if(aria) return aria.trim();
    var lbl = el.getAttribute('aria-labelledby');
    if(lbl){
      var ref = document.getElementById(lbl);
      if(ref) return (ref.innerText||'').trim();
    }
    if(el.labels && el.labels.length){
      return (el.labels[0].innerText||'').trim();
    }
    var inner = (el.innerText||'').trim();
    if(inner) return inner;
    return (el.placeholder || el.value || el.name || el.title || '').trim();
  }
  // Clear old IDs first — re-entrant
  document.querySelectorAll('[data-brain-id]').forEach(function(el){
    el.removeAttribute('data-brain-id');
  });
  // P3.4 增量:全扫完成后把 MutationObserver 侧的脏集合清空,并把 __brainNextID
  // 重置为 0,使后续增量采集在新 ID 空间上自增。observer 本身不拆,由
  // brainInstallObserverJS 保证全局只装一次。
  window.__brainNextID = 0;
  if (window.__brainDirty && typeof window.__brainDirty.clear === 'function'){
    window.__brainDirty.clear();
  }
  var n = 0;
  var out = [];
  document.querySelectorAll(SEL).forEach(function(el){
    if(!visible(el)) return;
    var id = ++n;
    el.setAttribute('data-brain-id', String(id));
    var r = el.getBoundingClientRect();
    var iv = r.top >= 0 && r.bottom <= innerHeight
          && r.left >= 0 && r.right <= innerWidth;
    var partial = !iv
      && r.bottom > 0 && r.top < innerHeight
      && r.right > 0 && r.left < innerWidth;
    out.push({
      id: id,
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute('role') || el.tagName.toLowerCase(),
      type: el.getAttribute('type') || '',
      name: textOf(el).slice(0, 120),
      value: (el.value || '').slice(0, 80),
      href: el.tagName === 'A' ? (el.getAttribute('href') || '') : '',
      x: Math.round(r.x + r.width/2),
      y: Math.round(r.y + r.height/2),
      w: Math.round(r.width),
      h: Math.round(r.height),
      inViewport: iv,
      partial: partial,
      focused: document.activeElement === el,
      disabled: !!el.disabled,
      checked: !!el.checked,
      expanded: el.getAttribute('aria-expanded') === 'true'
    });
  });
  // 把最终 n 同步到全局 __brainNextID,增量采集从 n+1 起累加 ID。
  window.__brainNextID = n;
  return JSON.stringify(out);
})();
`

// brainIncrementalSnapshotJS — 增量采集:只处理 __brainDirty 里的脏元素,
// 给没 data-brain-id 的分配新 ID(承接 __brainNextID);已分配的重新读取
// 坐标/可见性。返回 {updated: [brainElement...], removed: [id...]}。
//
// removed 给调用方一个机会删掉已经从 DOM 消失的元素的缓存条目,减少
// "inViewport" 字段在 UI 上表现为假阳性。
const brainIncrementalSnapshotJS = `
(function(){
  var SEL = '` + brainInteractiveSelector + `';
  function visible(el){
    var r = el.getBoundingClientRect();
    var s = getComputedStyle(el);
    return r.width > 0 && r.height > 0
      && s.visibility !== 'hidden'
      && s.display !== 'none'
      && +s.opacity > 0;
  }
  function textOf(el){
    var aria = el.getAttribute('aria-label');
    if(aria) return aria.trim();
    var lbl = el.getAttribute('aria-labelledby');
    if(lbl){
      var ref = document.getElementById(lbl);
      if(ref) return (ref.innerText||'').trim();
    }
    if(el.labels && el.labels.length){
      return (el.labels[0].innerText||'').trim();
    }
    var inner = (el.innerText||'').trim();
    if(inner) return inner;
    return (el.placeholder || el.value || el.name || el.title || '').trim();
  }
  if (!window.__brainDirty){
    // observer 没装(或页面整个换过),让调用方回退全量。
    return JSON.stringify({miss: true});
  }
  if (typeof window.__brainNextID !== 'number') window.__brainNextID = 0;

  var updated = [];
  var removed = [];
  var dirty = Array.from(window.__brainDirty);
  window.__brainDirty.clear();

  for (var i = 0; i < dirty.length; i++){
    var el = dirty[i];
    // 已从 DOM 摘除
    if (!(el instanceof Element) || !el.isConnected){
      // 没法从 disconnected 拿到原 brain-id,尝试从 el.dataset
      if (el && el.dataset && el.dataset.brainId){
        removed.push(parseInt(el.dataset.brainId));
      }
      continue;
    }
    if (!el.matches(SEL)) continue;
    if (!visible(el)) {
      if (el.dataset && el.dataset.brainId){
        removed.push(parseInt(el.dataset.brainId));
        el.removeAttribute('data-brain-id');
      }
      continue;
    }
    var id = el.getAttribute('data-brain-id');
    if (!id){
      window.__brainNextID += 1;
      id = String(window.__brainNextID);
      el.setAttribute('data-brain-id', id);
    }
    var r = el.getBoundingClientRect();
    var iv = r.top >= 0 && r.bottom <= innerHeight
          && r.left >= 0 && r.right <= innerWidth;
    var partial = !iv
      && r.bottom > 0 && r.top < innerHeight
      && r.right > 0 && r.left < innerWidth;
    updated.push({
      id: parseInt(id),
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute('role') || el.tagName.toLowerCase(),
      type: el.getAttribute('type') || '',
      name: textOf(el).slice(0, 120),
      value: (el.value || '').slice(0, 80),
      href: el.tagName === 'A' ? (el.getAttribute('href') || '') : '',
      x: Math.round(r.x + r.width/2),
      y: Math.round(r.y + r.height/2),
      w: Math.round(r.width),
      h: Math.round(r.height),
      inViewport: iv,
      partial: partial,
      focused: document.activeElement === el,
      disabled: !!el.disabled,
      checked: !!el.checked,
      expanded: el.getAttribute('aria-expanded') === 'true'
    });
  }
  return JSON.stringify({updated: updated, removed: removed});
})();
`

// browserSnapshotTool implements browser.snapshot.
type browserSnapshotTool struct{ holder *browserSessionHolder }

func (t *browserSnapshotTool) Name() string { return "browser.snapshot" }
func (t *browserSnapshotTool) Risk() Risk   { return RiskSafe }

func (t *browserSnapshotTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Produce a flat, stable list of visible interactive elements on the current page,
tagging each with data-brain-id. Subsequent click/type/hover calls can target elements by id
instead of writing CSS selectors.

When to use:
  - First action on any new page to orient yourself
  - When DOM has changed significantly (after navigation, modal open, SPA transition)

When NOT to use:
  - For pure informational reading (use eval or screenshot)
  - After every single tool call (snapshot is re-run by click/type as needed)

mode:
  - "interactive" (default): DOM-walk only, fast. Returns interactive elements list.
  - "a11y": Accessibility.getFullAXTree only, for semantic role/name
  - "both": merge DOM walk with AX tree for richest output
  - "text": return document.body.innerText (human-readable page text).
           Use this to read page content (search results, articles, prices).
  - "html": return full document.documentElement.outerHTML (raw HTML).
           Use this when you need to parse structured data or specific DOM attributes.

Text/html modes return the payload in the "content" field (truncated to
max_chars, default 50000). They do not populate the "elements" array.

incremental (default true): if a prior snapshot exists on the same page, only
re-scan DOM subtrees mutated since last call (MutationObserver-driven).
Set false to force a full re-scan (e.g. when the page's DOM IDs went stale).
Full scan is always used on first call, on URL change, or when mode=a11y|both.`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "mode":          { "type": "string", "enum": ["interactive","a11y","both","text","html"], "description": "Data sources to combine (default: interactive)" },
    "max_elements":  { "type": "integer", "description": "Truncate to N elements (default 200)" },
    "max_chars":     { "type": "integer", "description": "For text/html modes: truncate content to N chars (default 50000)" },
    "viewport_only": { "type": "boolean", "description": "Only return elements currently in viewport" },
    "incremental":   { "type": "boolean", "description": "Reuse cached snapshot + observer diff when available (default true)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "count":    { "type": "integer" },
    "total":    { "type": "integer", "description": "Before truncation" },
    "mode":     { "type": "string" },
    "url":      { "type": "string" },
    "title":    { "type": "string" },
    "elements": { "type": "array" },
    "content":  { "type": "string", "description": "Page text/html when mode=text|html" },
    "truncated":{ "type": "boolean", "description": "Content was truncated to max_chars" }
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

func (t *browserSnapshotTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Mode         string `json:"mode"`
		MaxElements  int    `json:"max_elements"`
		MaxChars     int    `json:"max_chars"`
		ViewportOnly bool   `json:"viewport_only"`
		Incremental  *bool  `json:"incremental,omitempty"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Mode == "" {
		input.Mode = "interactive"
	}
	if input.MaxElements <= 0 {
		input.MaxElements = 200
	}
	if input.MaxChars <= 0 {
		input.MaxChars = 50000
	}
	// 默认 true。a11y / both 模式下禁用增量(AX 树没法增量 diff)。
	incremental := true
	if input.Incremental != nil {
		incremental = *input.Incremental
	}
	if input.Mode != "interactive" {
		incremental = false
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	// 读 URL 做页面边界 key。换页必须丢缓存(observer 自己也会失效,但本侧
	// 以 URL 兜底)。
	url, title := readPageMeta(ctx, sess)

	// text/html 模式:不走元素扫描,直接用 Runtime.evaluate 抓页面文本/HTML。
	if input.Mode == "text" || input.Mode == "html" {
		var expr string
		if input.Mode == "text" {
			expr = "document.body && document.body.innerText || ''"
		} else {
			expr = "document.documentElement && document.documentElement.outerHTML || ''"
		}
		var evalResult struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
		}, &evalResult); err != nil {
			return errResult("evaluate %s: %v", input.Mode, err), nil
		}
		content := evalResult.Result.Value
		truncated := false
		if len(content) > input.MaxChars {
			content = content[:input.MaxChars]
			truncated = true
		}
		// 清空 interactive 缓存:text/html 不经过 DOM 扫描,observer 状态下次要重新建。
		t.holder.observerInstalled = false
		t.holder.snapshotCache = nil
		t.holder.snapshotCachePageKey = ""
		return okResult(map[string]interface{}{
			"mode":      input.Mode,
			"url":       url,
			"title":     title,
			"content":   content,
			"truncated": truncated,
			"count":     len(content),
			"total":     len(content),
		}), nil
	}

	used := "full"
	var elements []brainElement

	// Interactive 模式下才走增量路径。
	if input.Mode == "interactive" {
		if incremental && t.holder.observerInstalled &&
			t.holder.snapshotCachePageKey == url && len(t.holder.snapshotCache) > 0 {
			// 尝试增量;miss/错误 -> 回退全量
			updated, removed, miss, err := collectIncremental(ctx, sess)
			if err == nil && !miss {
				elements = mergeIncrementalUpdate(t.holder.snapshotCache, updated, removed)
				t.holder.snapshotCache = elements
				used = "incremental"
			}
		}

		if used == "full" {
			// 1) 全量扫描(重置 __brainNextID 与 __brainDirty)
			elements, err = collectInteractive(ctx, sess)
			if err != nil {
				return errResult("collect interactive: %v", err), nil
			}
			// 2) 确保 MutationObserver 已就位,下一次才能走增量
			if installErr := installSnapshotObserver(ctx, sess); installErr == nil {
				t.holder.observerInstalled = true
			} else {
				t.holder.observerInstalled = false
			}
			t.holder.snapshotCache = elements
			t.holder.snapshotCachePageKey = url
		}
	} else {
		// a11y / both:按旧路径,不缓存、不 observer。
		if input.Mode == "both" {
			elements, err = collectInteractive(ctx, sess)
			if err != nil {
				return errResult("collect interactive: %v", err), nil
			}
		}
		axElems, axErr := collectAccessibility(ctx, sess)
		if axErr == nil {
			if input.Mode == "a11y" {
				elements = axElems
			} else {
				elements = mergeAXIntoInteractive(elements, axElems)
			}
		}
		// Mode 切到非 interactive 后,下一次 interactive 调用必须全扫。
		t.holder.observerInstalled = false
		t.holder.snapshotCache = nil
		t.holder.snapshotCachePageKey = ""
	}

	total := len(elements)

	if input.ViewportOnly {
		filtered := make([]brainElement, 0, len(elements))
		for _, e := range elements {
			if e.InViewport {
				filtered = append(filtered, e)
			}
		}
		elements = filtered
	}

	if len(elements) > input.MaxElements {
		elements = elements[:input.MaxElements]
	}

	return okResult(map[string]interface{}{
		"count":    len(elements),
		"total":    total,
		"mode":     input.Mode,
		"url":      url,
		"title":    title,
		"elements": elements,
		"snapshot_source": used, // "full" | "incremental"
	}), nil
}

// collectInteractive injects brainSnapshotJS and returns the element list.
func collectInteractive(ctx context.Context, sess *cdp.BrowserSession) ([]brainElement, error) {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    brainSnapshotJS,
		"returnByValue": true,
	}, &result); err != nil {
		return nil, err
	}
	if len(result.ExceptionDetails) > 0 {
		return nil, fmt.Errorf("JS exception: %s", string(result.ExceptionDetails))
	}

	// brainSnapshotJS returns JSON.stringify'd array → result.value is a JSON string.
	var jsonStr string
	if err := json.Unmarshal(result.Result.Value, &jsonStr); err != nil {
		return nil, fmt.Errorf("snapshot result not a string: %s", string(result.Result.Value))
	}
	var elements []brainElement
	if err := json.Unmarshal([]byte(jsonStr), &elements); err != nil {
		return nil, fmt.Errorf("parse elements: %v (raw: %s)", err, jsonStr[:min(500, len(jsonStr))])
	}
	return elements, nil
}

// installSnapshotObserver 在页面里装一次 MutationObserver + __brainDirty。
// 重复调用幂等。失败不致命:调用方自行走全量分支。
func installSnapshotObserver(ctx context.Context, sess *cdp.BrowserSession) error {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    brainInstallObserverJS,
		"returnByValue": true,
	}, &result); err != nil {
		return err
	}
	if len(result.ExceptionDetails) > 0 {
		return fmt.Errorf("install observer JS exception: %s", string(result.ExceptionDetails))
	}
	return nil
}

// collectIncremental 调一次 brainIncrementalSnapshotJS,解析 {updated, removed}。
// miss=true 表示页面里 observer 已经被清掉(例如整页重载),调用方应回退全量。
func collectIncremental(ctx context.Context, sess *cdp.BrowserSession) (updated []brainElement, removed []int, miss bool, err error) {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    brainIncrementalSnapshotJS,
		"returnByValue": true,
	}, &result); err != nil {
		return nil, nil, false, err
	}
	if len(result.ExceptionDetails) > 0 {
		return nil, nil, false, fmt.Errorf("incremental JS exception: %s", string(result.ExceptionDetails))
	}
	var jsonStr string
	if err := json.Unmarshal(result.Result.Value, &jsonStr); err != nil {
		return nil, nil, false, fmt.Errorf("incremental result not a string: %s", string(result.Result.Value))
	}
	var parsed struct {
		Miss    bool           `json:"miss"`
		Updated []brainElement `json:"updated"`
		Removed []int          `json:"removed"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil, nil, false, fmt.Errorf("parse incremental: %v", err)
	}
	if parsed.Miss {
		return nil, nil, true, nil
	}
	return parsed.Updated, parsed.Removed, false, nil
}

// mergeIncrementalUpdate 把增量结果合入上一次的缓存:
//   - removed 里的 ID 从缓存里剔除;
//   - updated 里的 ID 如果命中缓存,原地替换;未命中则 append。
// 保持原有元素相对顺序,避免 Agent 看到的 id 顺序大幅抖动。
func mergeIncrementalUpdate(prev, updated []brainElement, removed []int) []brainElement {
	if len(updated) == 0 && len(removed) == 0 {
		return prev
	}
	removedSet := make(map[int]struct{}, len(removed))
	for _, id := range removed {
		removedSet[id] = struct{}{}
	}
	updatedByID := make(map[int]brainElement, len(updated))
	for _, el := range updated {
		updatedByID[el.ID] = el
	}
	out := make([]brainElement, 0, len(prev)+len(updated))
	seen := make(map[int]struct{}, len(prev))
	for _, el := range prev {
		if _, gone := removedSet[el.ID]; gone {
			continue
		}
		if upd, ok := updatedByID[el.ID]; ok {
			out = append(out, upd)
		} else {
			out = append(out, el)
		}
		seen[el.ID] = struct{}{}
	}
	for _, el := range updated {
		if _, ok := seen[el.ID]; ok {
			continue
		}
		out = append(out, el)
	}
	return out
}

// axNode mirrors CDP Accessibility.AXNode fields we care about.
type axNode struct {
	NodeID    string        `json:"nodeId"`
	Role      *axStringProp `json:"role"`
	Name      *axStringProp `json:"name"`
	Value     *axStringProp `json:"value"`
	Ignored   bool          `json:"ignored"`
	Focused   bool          `json:"focused"`
	ChildIDs  []string      `json:"childIds"`
	BackendID int64         `json:"backendDOMNodeId"`
	Props     []axProp      `json:"properties"`
}

type axStringProp struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type axProp struct {
	Name  string       `json:"name"`
	Value axStringProp `json:"value"`
}

// collectAccessibility pulls the full AX tree.
func collectAccessibility(ctx context.Context, sess *cdp.BrowserSession) ([]brainElement, error) {
	var result struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := sess.Exec(ctx, "Accessibility.getFullAXTree", map[string]interface{}{}, &result); err != nil {
		return nil, fmt.Errorf("getFullAXTree: %w", err)
	}

	out := make([]brainElement, 0, len(result.Nodes))
	for i, n := range result.Nodes {
		if n.Ignored {
			continue
		}
		role := ""
		if n.Role != nil {
			role = n.Role.Value
		}
		// Skip uninteresting container nodes.
		if !isInteractiveAxRole(role) {
			continue
		}
		name := ""
		if n.Name != nil {
			name = n.Name.Value
		}
		value := ""
		if n.Value != nil {
			value = n.Value.Value
		}
		e := brainElement{
			ID:      i + 1, // fallback ID; real id overwritten by merge step
			Role:    role,
			AxRole:  role,
			Name:    name,
			AxName:  name,
			Value:   value,
			Focused: n.Focused,
		}
		for _, p := range n.Props {
			switch p.Name {
			case "disabled":
				e.Disabled = p.Value.Value == "true"
			case "checked":
				e.Checked = p.Value.Value == "true"
			case "expanded":
				e.Expanded = p.Value.Value == "true"
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func isInteractiveAxRole(role string) bool {
	switch role {
	case "button", "link", "textbox", "combobox", "listbox", "checkbox",
		"radio", "switch", "tab", "menuitem", "menu", "option",
		"slider", "spinbutton", "searchbox":
		return true
	}
	return false
}

// mergeAXIntoInteractive enriches DOM-walk elements with AX semantic names / roles.
// We match by visual bounding box proximity and text similarity.
func mergeAXIntoInteractive(dom, ax []brainElement) []brainElement {
	// Simple pass: match AX entries by normalized name to DOM entries.
	// Unmatched AX entries are appended with synthetic IDs.
	axByName := make(map[string]*brainElement, len(ax))
	for i := range ax {
		key := strings.ToLower(strings.TrimSpace(ax[i].AxName))
		if key == "" {
			continue
		}
		if _, ok := axByName[key]; !ok {
			axByName[key] = &ax[i]
		}
	}
	for i := range dom {
		key := strings.ToLower(strings.TrimSpace(dom[i].Name))
		if a, ok := axByName[key]; ok {
			dom[i].AxRole = a.AxRole
			dom[i].AxName = a.AxName
			if !dom[i].Focused {
				dom[i].Focused = a.Focused
			}
			if !dom[i].Disabled {
				dom[i].Disabled = a.Disabled
			}
			if !dom[i].Checked {
				dom[i].Checked = a.Checked
			}
			if !dom[i].Expanded {
				dom[i].Expanded = a.Expanded
			}
		}
	}
	return dom
}

// readPageMeta returns (url, title) for the current page. Errors are swallowed.
func readPageMeta(ctx context.Context, sess *cdp.BrowserSession) (string, string) {
	js := `JSON.stringify({url:location.href, title:document.title})`
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return "", ""
	}
	var s string
	if json.Unmarshal(result.Result.Value, &s) != nil {
		return "", ""
	}
	var meta struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(s), &meta) != nil {
		return "", ""
	}
	return meta.URL, meta.Title
}

// resolveBrainID converts a data-brain-id integer to (x,y) center coordinates.
// Returns a helpful error if the id is missing (e.g. no snapshot was taken,
// or the DOM has changed since).
func resolveBrainID(ctx context.Context, sess *cdp.BrowserSession, id int) (float64, float64, error) {
	if id <= 0 {
		return 0, 0, fmt.Errorf("invalid brain id: %d", id)
	}
	js := fmt.Sprintf(`(function(){
  var el = document.querySelector('[data-brain-id="%d"]');
  if(!el) return null;
  var r = el.getBoundingClientRect();
  return JSON.stringify({x: r.x+r.width/2, y: r.y+r.height/2, w: r.width, h: r.height});
})()`, id)

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
	var s string
	if err := json.Unmarshal(result.Result.Value, &s); err != nil {
		return 0, 0, fmt.Errorf("element [data-brain-id=%d] not found — run browser.snapshot first", id)
	}
	var pos struct {
		X, Y, W, H float64
	}
	if err := json.Unmarshal([]byte(s), &pos); err != nil {
		return 0, 0, fmt.Errorf("parse pos: %v", err)
	}
	if pos.W <= 0 || pos.H <= 0 {
		return 0, 0, fmt.Errorf("element [data-brain-id=%d] has zero size", id)
	}
	return pos.X, pos.Y, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// focusBrainID calls element.focus() on a snapshot-tagged element.
func focusBrainID(ctx context.Context, sess *cdp.BrowserSession, id int) error {
	if id <= 0 {
		return fmt.Errorf("invalid brain id: %d", id)
	}
	js := fmt.Sprintf(`(function(){
  var el = document.querySelector('[data-brain-id="%d"]');
  if(!el) return "not_found";
  el.focus();
  return "ok";
})()`, id)
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := sess.Exec(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    js,
		"returnByValue": true,
	}, &result); err != nil {
		return err
	}
	var s string
	if json.Unmarshal(result.Result.Value, &s) != nil {
		return fmt.Errorf("focus id=%d: unexpected result", id)
	}
	if s != "ok" {
		return fmt.Errorf("element [data-brain-id=%d] not found — run browser.snapshot first", id)
	}
	return nil
}
