// auto_dispatch.go — 中央大脑自动委派触发器。
//
// 设计动机：仅靠 prompt 教 LLM "Tier 决策框架"实测不可靠（LLM 偷懒自己干）。
// 在 chat 把用户输入交给 LLM 之前，预扫一遍触发词，给 LLM 加一条**强制性
// hint** 系统消息："这个任务包含 <类型> 关键词，必须 delegate 给 <brain>"。
//
// 与 prompt 区别：
//   - prompt 是泛规则，LLM 可以选择性遵守
//   - 这里是**针对当前 input 的针对性 hint**，写明白 brain_id，LLM 不容易绕开
//
// 多个触发同时命中时按优先级：browser > verifier > code > data > quant
// （越下游越可能是用户最终目标 —— 截图 > 测试 > 写代码）。
package chat

import "strings"

// DispatchHint 返回给定用户 input 的自动委派建议。
// 空字符串表示没有强制 hint（中央大脑自由决定）。
func DispatchHint(input string) string {
	hint := AutoDispatchHints(input)
	if len(hint) == 0 {
		return ""
	}
	// 把多个 hint 拼成一段 system 文本
	var sb strings.Builder
	sb.WriteString("\n\n## 自动委派建议（针对当前用户输入）\n\n")
	sb.WriteString("基于关键词扫描，本次任务匹配以下专精大脑能力。**强烈建议**走对应委派：\n\n")
	for _, h := range hint {
		sb.WriteString("- ")
		sb.WriteString(h)
		sb.WriteString("\n")
	}
	sb.WriteString("\n如确实是 Tier 1 只读简单任务（看一眼 / 单行回答），可忽略此建议。" +
		"否则请用 `central.delegate` 或 `central.submit_workflow` 走对应大脑。\n")
	return sb.String()
}

// AutoDispatchHints 返回触发的所有委派建议（按优先级）。
// 每条建议是给 LLM 看的人话，已经包含 brain_id 推荐。
func AutoDispatchHints(input string) []string {
	low := strings.ToLower(strings.TrimSpace(input))
	if low == "" {
		return nil
	}

	var hints []string
	matched := map[string]bool{}

	// browser：触发关键词最强（用户要看效果 / 截图 / 操作网页）
	if hasAnyKeyword(low, browserKeywords) && !matched["browser"] {
		hints = append(hints, "→ **browser 大脑**：包含浏览器交互需求（截图 / 打开页面 / 点击 / 截屏）。"+
			"必须 `central.delegate` 给 browser，不要用 shell_exec curl 替代。")
		matched["browser"] = true
	}

	// verifier：测试 / 编译 / 验证
	if hasAnyKeyword(low, verifierKeywords) && !matched["verifier"] {
		hints = append(hints, "→ **verifier 大脑**：包含测试 / 编译 / 验证需求。"+
			"`central.delegate` 给 verifier 跑 go build / go test / 检查产出。")
		matched["verifier"] = true
	}

	// code：写文件 / 改代码 / 项目操作
	if hasAnyKeyword(low, codeKeywords) && !matched["code"] {
		hints = append(hints, "→ **code 大脑**：包含写代码 / 编辑文件 / 项目操作需求。"+
			"`central.delegate` 给 code 大脑写代码，不要用 central.write_file 自己干。")
		matched["code"] = true
	}

	// data：行情 / 数据查询
	if hasAnyKeyword(low, dataKeywords) && !matched["data"] {
		hints = append(hints, "→ **data 大脑**：包含行情 / 实时数据查询需求。"+
			"用 data.* 直接工具或 `central.delegate` 给 data。")
		matched["data"] = true
	}

	// quant：交易 / 仓位
	if hasAnyKeyword(low, quantKeywords) && !matched["quant"] {
		hints = append(hints, "→ **quant 大脑**：包含交易 / 仓位 / 策略需求。"+
			"用 quant.* 直接工具或 `central.delegate` 给 quant。")
		matched["quant"] = true
	}

	return hints
}

// hasAnyKeyword 判断 low 中是否含 keywords 中任一关键词。
// keywords 已是 lowercase。
func hasAnyKeyword(low string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// 触发词表 —— 全部 lowercase。
//
// 设计原则：只放高置信度的关键词，避免误伤（"代码"这种太宽，"写代码""改代码"才精确）。
// 中英混合，因为用户可能用任一种说。

var browserKeywords = []string{
	"打开网页", "打开页面", "打开浏览器", "浏览器打开",
	"截图", "截屏", "屏幕截图", "看到效果", "看效果", "运行起来看",
	"网页", "网站", "页面打开", "开个浏览器",
	"点击按钮", "登录", "填表",
	"open browser", "screenshot", "open page", "navigate to", "click on",
}

var verifierKeywords = []string{
	"跑测试", "跑单元测试", "运行测试", "测试一下", "测试通过",
	"go test", "go build", "go vet", "编译验证", "编译一下",
	"验证代码", "代码验证", "运行编译",
	"run tests", "build the", "verify the", "compile",
}

var codeKeywords = []string{
	"写代码", "写一段", "实现一个", "实现一段", "写一个函数", "改代码", "改一下代码",
	"编辑文件", "修改文件", "创建文件", "创建项目", "重构",
	"写 go", "写 python", "写 javascript", "写 ts", "写 rust",
	"实现", "重写", "改造代码",
	"hello world", "贪吃蛇", "demo", "项目骨架", "gf init",
	"write code", "implement", "refactor", "edit file", "create file",
}

var dataKeywords = []string{
	"行情", "实时数据", "k线", "k-line", "k 线", "盘口", "成交量",
	"price", "ticker", "candles", "orderbook",
}

var quantKeywords = []string{
	"交易", "下单", "仓位", "持仓", "策略", "回测", "止损", "止盈",
	"账户", "净值", "盈亏",
	"trade", "position", "strategy", "backtest", "pnl",
}
