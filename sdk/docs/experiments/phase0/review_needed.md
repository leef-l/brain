# 16 条待人工审核条目

## 如何用

打开对应 `labels/<page>.json` 文件,找到 `elements` 数组里 `id` 等于下面编号的项,把 `ground_truth` 字段填上,`review_status` 改成 `reviewed`。

每一条的 JSON 长这样:
```json
{"id": 29, "element": {...}, "draft": {...}, "ground_truth": null, "review_status": "pending"}
```
把 `ground_truth: null` 改成:
```json
{"action_intent": "...", "reversibility": "...", "risk_level": "...", "flow_role": "..."}
```
然后把 `review_status` 改为 `"reviewed"`。

---

## `labels/adminlte_dashboard.json`

文件路径: `sdk/docs/experiments/phase0/labels/adminlte_dashboard.json`

### [C1] id=1  `<a>` 无文本 <a>(推测 hamburger)

**当前草稿**:
- intent: Toggle or open a navigation/menu element in the top navbar
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `navigation`

**我的建议**: LLM 猜的,建议标 ambiguous 剔除

### [C2] id=4  `<a>` 无文本 <a>(推测通知)

**当前草稿**:
- intent: Open a notifications or messages dropdown panel showing items (nearby badges with counts 3 and 15 suggest notification area)
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `utility`

**我的建议**: LLM 猜的,建议标 ambiguous 剔除

### [C3] id=7  `<a>` 无文本 <a>(推测右侧栏)

**当前草稿**:
- intent: Open a user-related or settings dropdown/panel in the far-right area of the navbar
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `utility`

**我的建议**: LLM 猜的,建议标 ambiguous 剔除

### [B1] id=3  `<a>` Contact(href=#)

**当前草稿**:
- intent: Navigate to the Contact page or section
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `cross_page_nav`

**我的建议**: 草稿: role=cross_page_nav | 建议改 utility(失效占位)

## `labels/antd_pro_dashboard.json`

文件路径: `sdk/docs/experiments/phase0/labels/antd_pro_dashboard.json`

### [B2] id=7  `<a>` Analysis(当前页自身)

**当前草稿**:
- intent: Navigate to the Analysis page under the Dashboard section.
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `navigation`

**我的建议**: 草稿: role=navigation | 建议改 utility(无效链接)

## `labels/autoex_product.json`

文件路径: `sdk/docs/experiments/phase0/labels/autoex_product.json`

### [A1] id=29  `<button>` 匿名 <button>(推测 newsletter 订阅)

**当前草稿**:
- intent: Subscribe to the site's newsletter or mailing list by submitting an email address.
- reversibility: `semi_reversible`
- risk_level: `external_effect`
- flow_role: `secondary`

**我的建议**: 草稿: risk=external_effect  | 建议: external_effect(发邮件) | 要你定: external_effect OR safe_caution

## `labels/discourse_login.json`

文件路径: `sdk/docs/experiments/phase0/labels/discourse_login.json`

### [C4] id=6  `<a>` I forgot my password

**当前草稿**:
- intent: Navigate to the password reset flow to recover a forgotten password.
- reversibility: `reversible`
- risk_level: `safe_caution`
- flow_role: `utility`

**我的建议**: 草稿: risk=safe_caution | 要你定: safe_caution OR safe

## `labels/gitea_login.json`

文件路径: `sdk/docs/experiments/phase0/labels/gitea_login.json`

### [B3] id=3  `<a>` Help(外域 docs.gitea.com)

**当前草稿**:
- intent: Open the Gitea documentation site for help and guidance.
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `utility`

**我的建议**: 草稿: role=utility | 建议改 cross_page_nav

## `labels/govuk_form.json`

文件路径: `sdk/docs/experiments/phase0/labels/govuk_form.json`

### [A2] id=1  `<button>` Accept analytics cookies

**当前草稿**:
- intent: Accept the use of analytics cookies on the site
- reversibility: `semi_reversible`
- risk_level: `safe_caution`
- flow_role: `utility`

**我的建议**: 草稿: risk=safe_caution | 建议: safe_caution(保守) | 要你定: safe_caution OR safe

## `labels/rjsf_form.json`

文件路径: `sdk/docs/experiments/phase0/labels/rjsf_form.json`

### [B7] id=41  `<button>` hamburger/brand 按钮

**当前草稿**:
- intent: The user wants to navigate to the react-jsonschema-form playground home or toggle the samples/navigation drawer open.
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `navigation`

**我的建议**: 草稿: role=navigation | 建议保留 navigation(同页抽屉)

### [C5] id=74  `<input>` 字段名 '_'(可疑)

**当前草稿**:
- intent: The user wants to enter or edit a secondary form field value (labeled '_') in the playground's rendered form preview.
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `primary`

**我的建议**: LLM 自己也困惑 | 建议保草稿 primary 但低置信度

### [A3] id=2  `<button>` BLANK 样本按钮

**当前草稿**:
- intent: The user wants to load the 'BLANK' sample schema template into the playground editors.
- reversibility: `semi_reversible`
- risk_level: `safe_caution`
- flow_role: `navigation`

**我的建议**: 草稿: rev=semi_reversible | 建议: reversible(能切回) | 要你定: reversible OR semi_reversible

## `labels/saleor_product.json`

文件路径: `sdk/docs/experiments/phase0/labels/saleor_product.json`

### [B4] id=8  `<button>` 购物车按钮 "0 items in cart"

**当前草稿**:
- intent: Open the shopping cart/bag to view its contents (currently 0 items).
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `utility`

**我的建议**: 草稿: role=utility | 建议改 navigation(打开侧栏)

### [B5] id=3  `<a>` "All" 分类链接

**当前草稿**:
- intent: Navigate to the full product listing page showing all products.
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `navigation`

**我的建议**: 草稿: role=navigation | 建议改 cross_page_nav

## `labels/startpage_search.json`

文件路径: `sdk/docs/experiments/phase0/labels/startpage_search.json`

### [B6] id=19  `<button>` "All" tab 切换

**当前草稿**:
- intent: Filter search results to show all result types (web results).
- reversibility: `reversible`
- risk_level: `safe`
- flow_role: `navigation`

**我的建议**: 草稿: role=navigation | 建议保留 navigation(同页 tab)
