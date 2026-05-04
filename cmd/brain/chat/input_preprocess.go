// input_preprocess.go — token 节省 P2-A
//
// 用户输入往往含废话(填充词、重复标点),会浪费 token 又扰乱 LLM 注意力。
// 本模块在用户输入送往 LLM 之前做无损预处理:
//   - 去**纯填充音**(嗯嗯嗯/呃呃/um um),不动有语义的短语
//   - 折叠 3+ 重复标点(!!! → !)
//   - 折叠 3+ 空白行
//   - 长粘贴摘要(超过阈值时存进 PasteStore,送 LLM 的换成"[PASTE id=xxx]"摘要)
//
// 注意:
// - Go regexp 是 RE2,不支持反向引用 \1,所以 3+ 重复标点用枚举的方式实现。
// - 长粘贴摘要默认开启(阈值 4000 chars,paste_store.go + read_paste.go 配套)。
//   原文存 PasteStore,LLM 看到 [PASTE id=xxx] 标记可调 central.read_paste(id) 取回。
// - 调用方应只把 PreprocessUserInput 的结果用于"当前 turn 发给 LLM 的临时拷贝",
//   持久化、Activity、PrintUserMessage、state.Messages 务必保留原文,以保证
//   下一轮 LLM 仍能看到完整历史,以及 SQLite 项目记忆里存的是真用户输入。

package chat

import (
	"regexp"
	"strings"
)

// PreprocessConfig 控制输入预处理行为。
type PreprocessConfig struct {
	// LongPasteThresholdChars 单条用户消息超过此字符数视为"长粘贴",生成摘要。
	// 设 0 关闭长粘贴摘要(默认关闭,避免数据丢失,见文件头注释)。
	LongPasteThresholdChars int
	// HeadLines 长粘贴保留的首部行数(仅当摘要功能开启时有效)。
	HeadLines int
	// TailLines 长粘贴保留的尾部行数(仅当摘要功能开启时有效)。
	TailLines int
}

// DefaultPreprocessConfig 默认值:无损去废话 + 长粘贴摘要(阈值 4000 chars)。
// 4000 chars ≈ 1000 token,超过此阈值才走摘要路径,常规对话(< 1000 字符)直通。
var DefaultPreprocessConfig = PreprocessConfig{
	LongPasteThresholdChars: 4000,
	HeadLines:               40,
	TailLines:               20,
}

// fillerWords 是**纯填充音**的多字重复型,作为子串出现时直接删除。
// 排除原则:任何具有语义的短语(如"i mean"/"you know what"/"你懂的"/"你懂吧")
// 都不放进来,避免误伤句子语义。
var fillerWords = []string{
	"嗯嗯嗯", "嗯嗯",
	"啊啊啊",
	"呃呃呃", "呃呃",
	"那个那个",
	"就是说就是说",
	"um um",
	"uh uh",
}

// punctRepeats 把 3+ 重复的同一个标点折叠成 1 个。
// Go RE2 不支持反向引用,所以为每种常见标点单独写一条,显式枚举。
var punctRepeats = []*regexp.Regexp{
	regexp.MustCompile(`!{3,}`),
	regexp.MustCompile(`\?{3,}`),
	regexp.MustCompile(`\.{3,}`), // 三个点的省略号 ... 也属于"重复",但这里用法是"!!!"风格,如要保留省略号可改为 4+
	regexp.MustCompile(`,{3,}`),
	regexp.MustCompile(`;{3,}`),
	regexp.MustCompile(`:{3,}`),
	regexp.MustCompile(`！{3,}`),
	regexp.MustCompile(`？{3,}`),
	regexp.MustCompile(`。{3,}`),
	regexp.MustCompile(`，{3,}`),
	regexp.MustCompile(`、{3,}`),
	regexp.MustCompile(`；{3,}`),
	regexp.MustCompile(`：{3,}`),
}

// punctReplacements 与 punctRepeats 一一对应:折叠后保留的字符。
var punctReplacements = []string{
	"!", "?", "...", ",", ";", ":",
	"！", "？", "。", "，", "、", "；", "：",
}

var (
	// blankLines 把 3+ 连续空行压成 2 个。
	blankLines = regexp.MustCompile(`\n{3,}`)
	// trailingSpace 行尾空白。
	trailingSpace = regexp.MustCompile(`[ \t]+\n`)
)

// PreprocessUserInput 对用户输入做无损去废话 + 必要时长粘贴摘要(默认关闭)。
// 返回 (处理后文本, 是否做了长粘贴摘要)。
//
// 调用方约束:返回的处理后文本仅能用于"当前 turn 发给 LLM 的临时 messages 拷贝",
// 不要写回 state.Messages、不要进 Activity.Input、不要进任何持久化。
// 否则下一轮 LLM 看到的历史会是处理后版本,与原文失去对应,且 SQLite 里存的不是真用户输入。
func PreprocessUserInput(input string, cfg PreprocessConfig) (string, bool) {
	if input == "" {
		return input, false
	}

	cleaned := stripFillers(input)
	for i, re := range punctRepeats {
		cleaned = re.ReplaceAllString(cleaned, punctReplacements[i])
	}
	cleaned = trailingSpace.ReplaceAllString(cleaned, "\n")
	cleaned = blankLines.ReplaceAllString(cleaned, "\n\n")
	cleaned = strings.TrimSpace(cleaned)

	if cfg.LongPasteThresholdChars > 0 && len(cleaned) > cfg.LongPasteThresholdChars {
		summary := summarizeLongPaste(cleaned, cfg.HeadLines, cfg.TailLines)
		return summary, true
	}

	return cleaned, false
}

// stripFillers 去掉纯填充音的多字重复型。
// 现在只保留无歧义的纯填充音(如"嗯嗯嗯"),不再包含"i mean"/"你懂的"等有语义的短语。
func stripFillers(s string) string {
	for _, w := range fillerWords {
		s = strings.ReplaceAll(s, w, "")
	}
	return s
}

// summarizeLongPaste 长粘贴摘要:把原文存进 PasteStore,返回带 [PASTE id=xxx] 标记
// + 首 head 行 + 中间省略 + 尾 tail 行的摘要。LLM 看到标记可调 central.read_paste(id)
// 取回完整原文。
//
// 行数不足 head+tail 时不摘要、不存 PasteStore,直接返回原文(避免无意义存储)。
func summarizeLongPaste(s string, head, tail int) string {
	lines := strings.Split(s, "\n")
	total := len(lines)
	if total <= head+tail {
		return s
	}

	entry := SharedPasteStore().Put(s)
	headText := strings.Join(lines[:head], "\n")
	tailText := strings.Join(lines[total-tail:], "\n")
	return FormatPasteSummary(entry, headText, tailText)
}
