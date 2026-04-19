package tool

import (
	"context"
	"regexp"
	"strings"
)

// 挂接 ui_patterns_auth.go 定义的 extraSeedProviders:空库首次 Seed 时后台
// admin 种子自动并入 seedPatterns 产出,不需要调用方再显式 SeedAdmin。
// 沿用 dev-auth 建立的跨场景包扩展点,不另造接线。
func init() {
	extraSeedProviders = append(extraSeedProviders, AdminSeedPatterns)
}

// Admin-CRUD UI pattern pack — 后台管理类通用操作的种子模式。
//
// 覆盖典型后台场景(AdminLTE / Ant Design Pro / React Admin 等框架共同
// 具备的)"表格分页 + 筛选 + 批量 + 编辑 + 导出"五要素。所有模式都复用
// 已有的 browser.* 工具(browser.click / browser.type / browser.fill_form
// / browser.downloads / browser.snapshot 等),不引入新工具。
//
// 误点保护:绝大多数 admin 操作都可能在中途弹出"删除确认"modal。
// 本文件里只有 admin_row_delete_confirm 把确认框当"正常流程",其余模式
// 的 OnAnomaly["confirm_delete"] = abort,避免 LLM 在做编辑/批量时误点
// 确认按钮造成数据丢失。
//
// 落库:调用方(cmd/brain 或 dashboard 启动路径)在拿到 PatternLibrary
// 之后应调用 SeedAdmin(ctx, lib) 做幂等 Upsert。AdminSeedPatterns() 返回
// 原始切片用于纯逻辑测试(跨框架 fixture 匹配)。

// AdminSeedPatterns 返回后台管理 CRUD 场景的种子模式切片。
// 每个模式的 Source 都标为 "seed",ID 带 "admin_" 前缀以便和其他场景
// 包区分。调用方负责调用 lib.Upsert(ctx, p) 合并进库。
func AdminSeedPatterns() []*UIPattern {
	// OnAnomaly 里的通用"误点保护":遇到确认删除 modal → 中止。
	// admin_row_delete_confirm 自己会覆盖这一条,其他模式都沿用。
	deleteGuard := map[string]AnomalyHandler{
		"confirm_delete": {
			Action: "abort",
			Reason: "unexpected delete confirmation — pattern is not a delete flow",
		},
		"error_message": {Action: "abort", Reason: "server-side error surfaced"},
	}

	return []*UIPattern{
		{
			ID:          "admin_table_pagination",
			Category:    "admin",
			Source:      "seed",
			Description: "Jump to a specific numbered page in a data-table pagination bar",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root, [class*="DataGrid" i]`,
				},
				TextContains: []string{},
				// 至少要有分页条(数字按钮 / Prev-Next) —— 通过 selector 约束。
				// AdminLTE: .pagination li>a ; AntD: .ant-pagination ; RA: [class*="RaPagination"]
			},
			ElementRoles: map[string]ElementDescriptor{
				"page_number": {
					Role: "button",
					Name: "~^\\d+$",
					CSS: `.pagination li a, .ant-pagination-item a, ` +
						`[class*="RaPagination" i] button, nav[aria-label*="pag" i] button`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "page_number"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_matches", URLPattern: `[?&]page=|[?&]p=|[?&]pageIndex=`},
					{Type: "dom_contains", Selector: `.pagination .active, .ant-pagination-item-active, [aria-current="page"]`},
				}},
			},
			OnAnomaly: deleteGuard,
		},
		{
			ID:          "admin_table_next_page",
			Category:    "admin",
			Source:      "seed",
			Description: "Advance a data table to the next page using a Next/下一页 button",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
					`a[rel="next"], .pagination .next, .ant-pagination-next, ` +
						`[aria-label*="next" i], [class*="next-page" i]`,
				},
			},
			ElementRoles: map[string]ElementDescriptor{
				"next_button": {
					Role: "button",
					Name: "~(?i)^(next|下一页|»|>)$",
					CSS: `a[rel="next"], .pagination .next a, .ant-pagination-next, ` +
						`button[aria-label*="next" i], [class*="next-page" i] button`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "next_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_changed"},
					{Type: "dom_contains", Selector: `.ant-pagination-item-active, .pagination .active`},
				}},
			},
			OnAnomaly: deleteGuard,
		},
		{
			ID:          "admin_infinite_scroll_load_more",
			Category:    "admin",
			Source:      "seed",
			Description: "Load next chunk of an infinite-scroll list (scroll to bottom or click Load more)",
			AppliesWhen: MatchCondition{
				// 无限滚动的 hallmark:列表容器 + 没有传统分页条 + 可能有 Load-more 按钮
				Has: []string{
					`[class*="list" i], [class*="feed" i], [class*="scroll" i], ul`,
				},
				HasNot: []string{
					`.pagination, .ant-pagination`,
				},
			},
			ElementRoles: map[string]ElementDescriptor{
				"load_more_button": {
					Role: "button",
					Name: "~(?i)(load\\s*more|show\\s*more|加载更多|查看更多|更多)",
					CSS:  `[class*="load-more" i], [class*="show-more" i], button[class*="more" i]`,
				},
			},
			ActionSequence: []ActionStep{
				// 先尝试 Load-more 按钮;没有按钮就让 scroll 工具触发滚动加载。
				{Tool: "browser.click", TargetRole: "load_more_button", Optional: true},
				{Tool: "browser.scroll", Params: map[string]interface{}{"to": "bottom"}, Optional: true},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				// 无限滚动没有 url 变化,只能看 DOM 是否增加。松校验:body 存在即可,
				// 具体"列表增长"由调用方上层做快照 diff。
				{Type: "dom_contains", Selector: "body"},
			},
			OnAnomaly: deleteGuard,
		},
		{
			ID:          "admin_filter_apply",
			Category:    "admin",
			Source:      "seed",
			Description: "Apply a filter (dropdown / date-range / multi-select tag) to a data table and submit",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
				},
				TextContains: []string{},
				// 至少出现一个常见筛选控件
			},
			ElementRoles: map[string]ElementDescriptor{
				"filter_trigger": {
					Role: "button",
					Name: "~(?i)(filter|筛选|过滤|search|查询)",
					CSS: `[class*="filter" i] button, button[class*="filter" i], ` +
						`.ant-btn-primary, [type="submit"]`,
				},
				"filter_select": {
					Tag: "select",
					CSS: `select[name*="filter" i], select[name*="status" i], select[name*="type" i], ` +
						`.ant-select, [role="combobox"]`,
				},
				"filter_date_from": {
					Tag:  "input",
					Type: "date",
					CSS:  `input[type="date"], input[placeholder*="start" i], input[placeholder*="from" i], input[placeholder*="起" i]`,
				},
				"filter_date_to": {
					Tag:  "input",
					Type: "date",
					CSS:  `input[type="date"]:nth-of-type(2), input[placeholder*="end" i], input[placeholder*="to" i], input[placeholder*="止" i]`,
				},
				"apply_button": {
					Role: "button",
					Name: "~(?i)(apply|filter|search|查询|确定|确认)",
					CSS:  `button[type="submit"], .ant-btn-primary`,
				},
			},
			ActionSequence: []ActionStep{
				// 三种筛选控件任一可选,最后统一点击 apply。browser.fill_form 可接
				// 多字段填充,这里按 role 逐一尝试以便独立失败。
				{Tool: "browser.select_option", TargetRole: "filter_select", Params: map[string]interface{}{"value": "$filter.value"}, Optional: true},
				{Tool: "browser.type", TargetRole: "filter_date_from", Params: map[string]interface{}{"text": "$filter.date_from", "clear": true}, Optional: true},
				{Tool: "browser.type", TargetRole: "filter_date_to", Params: map[string]interface{}{"text": "$filter.date_to", "clear": true}, Optional: true},
				{Tool: "browser.click", TargetRole: "apply_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_matches", URLPattern: `[?&](status|type|filter|from|to|q)=`},
					{Type: "dom_contains", Selector: `[class*="filter-tag" i], .ant-tag, [class*="chip" i]`},
				}},
			},
			OnAnomaly: deleteGuard,
		},
		{
			ID:          "admin_batch_action",
			Category:    "admin",
			Source:      "seed",
			Description: "Bulk-operate on a data table: select-all checkbox + batch action button + 2-step confirm",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
					`input[type="checkbox"]`,
				},
			},
			ElementRoles: map[string]ElementDescriptor{
				"select_all_checkbox": {
					Tag:  "input",
					Type: "checkbox",
					CSS: `thead input[type="checkbox"], .ant-table-thead input[type="checkbox"], ` +
						`[aria-label*="select all" i], [class*="select-all" i] input`,
				},
				"batch_action_button": {
					Role: "button",
					Name: "~(?i)(batch|bulk|批量|delete\\s*selected|apply\\s*to\\s*selected)",
					CSS: `[class*="batch" i] button, [class*="bulk" i] button, button[data-action*="batch" i]`,
				},
				"confirm_button": {
					Role: "button",
					Name: "~(?i)(confirm|ok|yes|确认|确定)",
					CSS: `.ant-modal-confirm-btns .ant-btn-primary, .modal-footer button.btn-primary, ` +
						`[role="alertdialog"] button[class*="primary" i]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "select_all_checkbox"},
				{Tool: "browser.click", TargetRole: "batch_action_button"},
				// 二次确认 modal 的确认点击 —— 这里 confirm_delete 是预期行为,
				// OnAnomaly 覆盖了 deleteGuard。
				{Tool: "browser.click", TargetRole: "confirm_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "dom_contains", Selector: `.ant-message-success, .toast-success, [role="alert"][class*="success" i]`},
					{Type: "url_changed"},
				}},
			},
			OnAnomaly: map[string]AnomalyHandler{
				// 批量操作本身会触发确认 modal,把它当"正常路径"的一部分 —— 不 abort。
				// error_message 仍然 abort,避免在失败后继续走。
				"error_message":  {Action: "abort", Reason: "batch action rejected by server"},
				"session_expired": {Action: "human_intervention"},
			},
		},
		{
			ID:          "admin_row_edit",
			Category:    "admin",
			Source:      "seed",
			Description: "Open a row's editor (inline / modal / side drawer) and save",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
				},
				TextContains: []string{},
			},
			ElementRoles: map[string]ElementDescriptor{
				"edit_trigger": {
					Role: "button",
					Name: "~(?i)(edit|修改|编辑)",
					CSS: `button[class*="edit" i], a[class*="edit" i], [aria-label*="edit" i]`,
				},
				"save_button": {
					Role: "button",
					Name: "~(?i)(save|submit|update|保存|提交|确定)",
					CSS: `button[type="submit"], .ant-btn-primary, .modal-footer .btn-primary, ` +
						`.ant-drawer-footer .ant-btn-primary`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "edit_trigger"},
				// 实际填字段由调用方通过 browser.fill_form 完成(字段名随业务变化),
				// 这里只负责打开→保存的外壳。
				{Tool: "browser.fill_form", Params: map[string]interface{}{"fields": "$edit.fields"}, Optional: true},
				{Tool: "browser.click", TargetRole: "save_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "dom_contains", Selector: `.ant-message-success, .toast-success, [role="alert"][class*="success" i]`},
					{Type: "url_changed"},
				}},
			},
			OnAnomaly: deleteGuard,
		},
		{
			ID:          "admin_row_delete_confirm",
			Category:    "admin",
			Source:      "seed",
			Description: "Delete a row and confirm the destructive action in the follow-up modal",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
				},
				TextContains: []string{},
			},
			ElementRoles: map[string]ElementDescriptor{
				"delete_trigger": {
					Role: "button",
					Name: "~(?i)(delete|remove|删除|移除)",
					CSS: `button[class*="delete" i], button[class*="danger" i], a[class*="delete" i], ` +
						`[aria-label*="delete" i]`,
				},
				"confirm_button": {
					Role: "button",
					Name: "~(?i)(confirm|yes|delete|ok|确认|确定|删除)",
					CSS: `.ant-modal-confirm-btns .ant-btn-dangerous, .ant-modal-confirm-btns .ant-btn-primary, ` +
						`.modal-footer .btn-danger, [role="alertdialog"] button[class*="danger" i]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "delete_trigger"},
				{Tool: "browser.click", TargetRole: "confirm_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "dom_contains", Selector: `.ant-message-success, .toast-success`},
					{Type: "url_changed"},
				}},
			},
			// 这个模式本来就是删除 —— confirm_delete 异常就是预期。error_message
			// 仍然 abort。
			OnAnomaly: map[string]AnomalyHandler{
				"error_message":   {Action: "abort", Reason: "delete rejected by server (FK/perm)"},
				"session_expired": {Action: "human_intervention"},
			},
		},
		{
			ID:          "admin_export_csv",
			Category:    "admin",
			Source:      "seed",
			Description: "Export current data-table view to CSV/Excel and wait for download to land",
			AppliesWhen: MatchCondition{
				Has: []string{
					`table, [role="table"], .ant-table, .MuiDataGrid-root`,
				},
				TextContains: []string{},
			},
			ElementRoles: map[string]ElementDescriptor{
				"export_button": {
					Role: "button",
					Name: "~(?i)(export|download|导出|下载|csv|excel)",
					CSS: `button[class*="export" i], a[class*="export" i], [aria-label*="export" i], ` +
						`button[class*="download" i]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "export_button"},
				// browser.downloads 感知下载落盘(由 Browser Brain 的下载管理器实现)。
				{Tool: "browser.downloads", Params: map[string]interface{}{"wait_for_new": true, "timeout_ms": 15000}},
			},
			PostConditions: []PostCondition{
				// browser.downloads 返回即认为导出成功;url 不一定变。这里再做一次
				// dom 兜底 —— 如果页面弹出 "导出成功" 提示也算通过。
				{Type: "any_of", Any: []PostCondition{
					{Type: "dom_contains", Selector: `.ant-message-success, .toast-success, [class*="download-ready" i]`},
					{Type: "dom_contains", Selector: "body"}, // browser.downloads 已经 post-check
				}},
			},
			OnAnomaly: deleteGuard,
		},
	}
}

// SeedAdmin 将后台管理场景种子模式幂等地合并进 lib。重复调用安全:
// Upsert 根据 id 做 ON CONFLICT DO UPDATE,不会产生重复行。
// 调用方通常在 NewPatternLibrary 返回后立即调用一次。
func SeedAdmin(ctx context.Context, lib *PatternLibrary) error {
	if lib == nil {
		return nil
	}
	for _, p := range AdminSeedPatterns() {
		if err := lib.Upsert(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Pure-Go match helper — 用于单测/离线 fixture 评估 MatchCondition,不依赖
// CDP session。生产匹配走 ui_pattern_match.go 的 evaluateMatch(真 DOM)。
// ---------------------------------------------------------------------------

// MatchStaticHTML 在不打开浏览器的前提下评估 MatchCondition 是否会命中
// 给定的 (url, title, html) 组合。适合跨框架 fixture 回归:同一个 pattern
// 对 AdminLTE / AntD Pro / React Admin 三份 HTML 都应返回 true。
//
// 与真 DOM 评估的差异:
//   - selector 走 regex 子串匹配 —— 不能表达所有 CSS 语法,但 Has/HasNot
//     里用到的典型形式(标签名 / class 前缀 / 属性选择器 ) 都覆盖。
//   - text/title 规则和 evaluateMatch 一致。
//
// 返回 true 表示"在离线 fixture 下模式被判定适用"。
func MatchStaticHTML(cond *MatchCondition, url, title, html string) bool {
	if cond == nil {
		return true
	}
	if cond.URLPattern != "" {
		re, err := regexp.Compile(cond.URLPattern)
		if err != nil || !re.MatchString(url) {
			return false
		}
	}
	titleLower := strings.ToLower(title)
	for _, needle := range cond.TitleContains {
		if !strings.Contains(titleLower, strings.ToLower(needle)) {
			return false
		}
	}
	htmlLower := strings.ToLower(html)
	for _, needle := range cond.TextContains {
		if needle == "" {
			continue
		}
		if !strings.Contains(htmlLower, strings.ToLower(needle)) {
			return false
		}
	}
	for _, sel := range cond.Has {
		if !staticSelectorMatches(htmlLower, sel) {
			return false
		}
	}
	for _, sel := range cond.HasNot {
		if staticSelectorMatches(htmlLower, sel) {
			return false
		}
	}
	return true
}

// staticSelectorMatches 把一个(可能是逗号分隔的)CSS selector 片段拆成
// 若干候选,任意一个能在 html(小写)里找到对应痕迹就算命中。这是对真
// querySelector 的粗糙近似,仅供离线 fixture 测试使用。
//
// 支持的形式:
//   - tag (table / input / button / form / a / ul / nav / select)
//   - .class-prefix / [class*="x"] / [class*="x" i]
//   - [attr="v"] / [attr*="v" i] / [role="x"] / [aria-label*="x" i]
//   - #id
//   - input[type="password"] / input[type="checkbox"] 等复合
func staticSelectorMatches(htmlLower, selector string) bool {
	for _, part := range splitSelectorList(selector) {
		if matchSingleSelector(htmlLower, strings.TrimSpace(part)) {
			return true
		}
	}
	return false
}

// splitSelectorList 按逗号拆分 selector 列表,但保留括号/引号里的逗号。
// 目前 seed 里没遇到复杂嵌套,简单 state-machine 够用。
func splitSelectorList(s string) []string {
	out := []string{}
	depth := 0
	inStr := byte(0)
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = c
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// matchSingleSelector:对单个 selector(无逗号)做近似匹配。
func matchSingleSelector(htmlLower, sel string) bool {
	sel = strings.ToLower(strings.TrimSpace(sel))
	if sel == "" {
		return false
	}
	// 拆出所有 [attr...] 段,单独校验;剩下的基字(tag / .class / #id)再查。
	attrRE := regexp.MustCompile(`\[([^\]]+)\]`)
	attrMatches := attrRE.FindAllStringSubmatch(sel, -1)
	base := attrRE.ReplaceAllString(sel, "")
	base = strings.TrimSpace(base)

	// attribute 段全部要满足
	for _, m := range attrMatches {
		if !attributeHit(htmlLower, m[1]) {
			return false
		}
	}

	// base 可能是 tag / .class / #id / tag.class 的组合;允许空(纯 [attr] selector)。
	if base == "" {
		return true
	}
	return baseHit(htmlLower, base)
}

// attributeHit 判定 "[attr=\"v\"]" / "[attr*=\"v\" i]" / "[attr]" 等形式在
// html 里是否出现。做法是转成 substring(忽略大小写,html 已经 lower)。
func attributeHit(htmlLower, inner string) bool {
	inner = strings.TrimSpace(inner)
	// 可能带 " i" 后缀(case-insensitive 标志) —— 我们已经 lower-case,直接吃掉。
	inner = strings.TrimSuffix(inner, " i")
	inner = strings.TrimSpace(inner)

	// [attr="value"] / [attr*="value"] / [attr^="value"] / [attr$="value"] / [attr]
	var attr, op, val string
	if idx := firstIndexOfAny(inner, []string{"*=", "^=", "$=", "|=", "~=", "="}); idx >= 0 {
		op = opAt(inner, idx)
		attr = strings.TrimSpace(inner[:idx])
		rest := strings.TrimSpace(inner[idx+len(op):])
		rest = strings.Trim(rest, `"'`)
		val = rest
	} else {
		attr = inner
	}
	if attr == "" {
		return false
	}
	if val == "" {
		// 只要求 attr 出现即可
		return strings.Contains(htmlLower, attr+"=") || strings.Contains(htmlLower, attr+" ") ||
			strings.Contains(htmlLower, attr+">")
	}
	// 近似:只要 html 里同时出现 attr= 和 value 子串即算命中。对 "data-brain-id"
	// 之类的稀有属性名 + 高信息量 value 来说误报风险可接受。
	needleLower := strings.ToLower(val)
	switch op {
	case "=":
		// 精确:attr="value"
		return strings.Contains(htmlLower, attr+`="`+needleLower+`"`) ||
			strings.Contains(htmlLower, attr+`='`+needleLower+`'`)
	default:
		// *=, ^=, $=, |=, ~= 都退化为"attr 出现 且 value 子串出现"
		if !strings.Contains(htmlLower, attr+"=") {
			return false
		}
		return strings.Contains(htmlLower, needleLower)
	}
}

func firstIndexOfAny(s string, needles []string) int {
	earliest := -1
	for _, n := range needles {
		if i := strings.Index(s, n); i >= 0 {
			if earliest < 0 || i < earliest {
				earliest = i
			}
		}
	}
	return earliest
}

func opAt(s string, idx int) string {
	if idx+1 < len(s) && s[idx+1] == '=' {
		return s[idx : idx+2]
	}
	return "="
}

// baseHit 处理 tag[.class]* / .class / #id / :pseudo(忽略) 的近似匹配。
func baseHit(htmlLower, base string) bool {
	// 去掉伪类/伪元素(:hover 等) —— fixture 不关心。
	if idx := strings.Index(base, ":"); idx >= 0 {
		base = base[:idx]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return true
	}
	// 分离 id / class / tag
	// 按 . 和 # 切,保留第一段为 tag(可空)。
	tag := ""
	classes := []string{}
	id := ""
	buf := ""
	kind := byte('t') // t=tag, c=class, i=id
	flush := func() {
		switch kind {
		case 't':
			tag = buf
		case 'c':
			classes = append(classes, buf)
		case 'i':
			id = buf
		}
		buf = ""
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch c {
		case '.':
			flush()
			kind = 'c'
		case '#':
			flush()
			kind = 'i'
		default:
			buf += string(c)
		}
	}
	flush()

	if tag != "" && tag != "*" {
		// 检查 <tag 或 <tag>
		if !strings.Contains(htmlLower, "<"+tag+">") &&
			!strings.Contains(htmlLower, "<"+tag+" ") &&
			!strings.Contains(htmlLower, "<"+tag+"\n") &&
			!strings.Contains(htmlLower, "<"+tag+"\t") {
			return false
		}
	}
	for _, cls := range classes {
		if cls == "" {
			continue
		}
		// class 可能出现在 class="a b c" 中:宽松地查 "cls" 子串出现过就算
		if !strings.Contains(htmlLower, cls) {
			return false
		}
	}
	if id != "" {
		if !strings.Contains(htmlLower, `id="`+id+`"`) &&
			!strings.Contains(htmlLower, `id='`+id+`'`) {
			return false
		}
	}
	return true
}
