package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// browser.visual_inspect — 阶段 3 多模态兜底工具(文档 40 §3.4)。
//
// 何时触发:Agent 在以下情境下调用本工具。
//   - pattern_match 未命中任何模式
//   - understand 返回 semantic_quality = low_confidence 或 structural_only
//   - 连续多 turn 对页面无进展
//
// 工作方式:
//   一次调用内部串起 screenshot + understand(跳过 LLM),把两者结果合并成一个
//   压缩过的视觉上下文包返回给 Agent。Agent 拿着这个结果和 LLM 做视觉推理。
//
// 定位:兜底而不是主路径。Agent 应优先用 understand/pattern_match,只在它们不够
// 用时才调 visual_inspect,成本显著高于常规感知。

type browserVisualInspectTool struct {
	holder *browserSessionHolder
}

func (t *browserVisualInspectTool) Name() string { return "browser.visual_inspect" }
func (t *browserVisualInspectTool) Risk() Risk   { return RiskSafe }

func (t *browserVisualInspectTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Multimodal fallback: capture a screenshot + bundle the structural snapshot
and (optionally) cached semantic labels into a single result for visual reasoning.

When to use (ordered priority):
  - pattern_match returned no matches AND understand semantic_quality is low
  - You've spent 3+ turns on the same page with no progress
  - You're about to perform a destructive action and want visual confirmation
  - The page has heavy canvas / custom-drawn UI that snapshot cannot see

When NOT to use:
  - First contact with a page (use snapshot + understand first — much cheaper)
  - Simple form filling where pattern_match or understand is sufficient
  - Every turn (this is an expensive tool, ~5x the tokens of understand)

Output includes:
  - screenshot (base64 PNG by default, or JPEG if quality set)
  - structural snapshot (same format as browser.snapshot, optionally trimmed)
  - cached semantic labels for annotated elements (never calls LLM here)
  - a 'quality' hint describing what was available`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "full_page":     { "type": "boolean", "description": "Capture entire scrollable page (default false)" },
    "format":        { "type": "string",  "description": "png (default) or jpeg" },
    "quality":       { "type": "integer", "description": "JPEG quality 0-100 (only when format=jpeg)" },
    "max_elements":  { "type": "integer", "description": "Cap interactive elements in bundled snapshot (default 40)" },
    "include_text":  { "type": "boolean", "description": "Include readable page text blocks (default false; large)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "quality":   { "type": "string", "description": "full | partial | screenshot_only" },
    "url":       { "type": "string" },
    "screenshot":{ "type": "object", "properties": {
        "format":   { "type": "string" },
        "data":     { "type": "string", "description": "base64-encoded image" },
        "encoding": { "type": "string" }
    }},
    "snapshot":  { "type": "object", "description": "Trimmed browser.snapshot result (elements[])" },
    "semantics": { "type": "array",  "description": "Cached semantic labels for elements (may be empty)" },
    "hints":     { "type": "array",  "description": "Short textual hints about what to focus on" }
  }
}`),
		Brain: "browser",
		Concurrency: &ToolConcurrencySpec{
			Capability:          "web.read",
			ResourceKeyTemplate: "browser:session",
			AccessMode:          "shared-session",
			Scope:               "turn",
			ApprovalClass:       "readonly",
		},
	}
}

type visualInspectInput struct {
	FullPage    bool   `json:"full_page"`
	Format      string `json:"format"`
	Quality     *int   `json:"quality"`
	MaxElements int    `json:"max_elements"`
	IncludeText bool   `json:"include_text"`
}

func (t *browserVisualInspectTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var in visualInspectInput
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if in.MaxElements <= 0 {
		in.MaxElements = 40
	}

	sess, err := t.holder.get(ctx)
	if err != nil {
		return errResult("no browser session: %v", err), nil
	}

	pageURL, _ := readPageMeta(ctx, sess)

	// 1. 结构快照(复用 snapshot 能力)
	elements, snapErr := collectInteractive(ctx, sess)
	var snapshotOut map[string]interface{}
	quality := "full"
	if snapErr != nil {
		quality = "screenshot_only"
	} else {
		if len(elements) > in.MaxElements {
			elements = elements[:in.MaxElements]
		}
		snapshotOut = map[string]interface{}{
			"count":    len(elements),
			"elements": elements,
		}
	}

	// 2. 截图
	shotFormat := "png"
	shotParams := map[string]interface{}{"format": "png"}
	if in.Format == "jpeg" {
		shotFormat = "jpeg"
		shotParams["format"] = "jpeg"
		if in.Quality != nil {
			shotParams["quality"] = *in.Quality
		}
	}
	if in.FullPage {
		var layout struct {
			ContentSize struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"contentSize"`
		}
		if err := sess.Exec(ctx, "Page.getLayoutMetrics", nil, &layout); err == nil {
			shotParams["clip"] = map[string]interface{}{
				"x": 0, "y": 0,
				"width":  layout.ContentSize.Width,
				"height": layout.ContentSize.Height,
				"scale":  1,
			}
			shotParams["captureBeyondViewport"] = true
		}
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := sess.Exec(ctx, "Page.captureScreenshot", shotParams, &shot); err != nil {
		return errResult("screenshot: %v", err), nil
	}

	// 3. 语义标签(只读缓存,不调 LLM — 成本已经够高)
	semantics := t.lookupCachedSemantics(ctx, pageURL, elements)
	if quality == "full" && len(semantics) == 0 && len(elements) > 0 {
		quality = "partial"
	}

	// 4. 文案线索
	hints := t.buildHints(elements, semantics, quality)

	out := map[string]interface{}{
		"quality": quality,
		"url":     pageURL,
		"screenshot": map[string]interface{}{
			"format":   shotFormat,
			"data":     shot.Data,
			"encoding": "base64",
		},
		"hints": hints,
	}
	if snapshotOut != nil {
		out["snapshot"] = snapshotOut
	}
	if len(semantics) > 0 {
		out["semantics"] = semantics
	}
	return okResult(out), nil
}

// lookupCachedSemantics 读已有的 SemanticCache,不做新标注。
func (t *browserVisualInspectTool) lookupCachedSemantics(ctx context.Context, pageURL string, elements []brainElement) []map[string]interface{} {
	cache, err := NewSemanticCache("")
	if err != nil || cache == nil {
		return nil
	}
	defer cache.Close()

	urlPat := urlPattern(pageURL)
	dhash := domHash(elements)
	keys := make([]string, 0, len(elements))
	byKey := make(map[string]brainElement, len(elements))
	for _, el := range elements {
		k := elementKey(el)
		keys = append(keys, k)
		byKey[k] = el
	}
	hits, _ := cache.Lookup(ctx, urlPat, dhash, keys)
	out := make([]map[string]interface{}, 0, len(hits))
	for k, e := range hits {
		el := byKey[k]
		out = append(out, map[string]interface{}{
			"id":             el.ID,
			"action_intent":  e.ActionIntent,
			"reversibility":  e.Reversibility,
			"risk_level":     e.RiskLevel,
			"flow_role":      e.FlowRole,
		})
	}
	return out
}

func (t *browserVisualInspectTool) buildHints(elements []brainElement, semantics []map[string]interface{}, quality string) []string {
	var hints []string
	switch quality {
	case "screenshot_only":
		hints = append(hints, "structural snapshot failed — reason about the screenshot visually")
	case "partial":
		hints = append(hints, "no cached semantics for this page — look at element labels in the snapshot")
	}

	// 统计破坏性/primary 动作,提示 Agent 关注
	destructive := 0
	primary := 0
	for _, s := range semantics {
		if s["risk_level"] == "destructive" {
			destructive++
		}
		if s["flow_role"] == "primary" {
			primary++
		}
	}
	if destructive > 0 {
		hints = append(hints, fmt.Sprintf("%d destructive action(s) visible — verify intent before clicking", destructive))
	}
	if primary > 0 {
		hints = append(hints, fmt.Sprintf("%d primary action(s) — likely the task-forward buttons", primary))
	}

	// 元素稀少时给出兜底提示
	if len(elements) < 5 {
		hints = append(hints, "very few interactive elements — page may still be loading or heavily custom-drawn")
	}

	// 去重
	seen := map[string]bool{}
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		k := strings.ToLower(h)
		if !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return out
}
