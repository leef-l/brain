package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/leef-l/brain/cmd/brain/diff"
	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

func PrintUserMessage(input string) {
	printTranscriptBlock("\033[1;32m>\033[0m", input)
}

func PrintAssistantMessage(reply string) {
	printTranscriptBlock("\033[1;36m>\033[0m", reply)
}

func printTranscriptBlock(prefix, body string) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i == 0 {
			fmt.Printf("%s %s\n", prefix, line)
			continue
		}
		fmt.Printf("  %s\n", line)
	}
	fmt.Println()
}

func PrintDiffPreviewBlock(lines []string) {
	if len(lines) == 0 {
		return
	}
	filePath := extractDiffFilePath(lines[0])
	colored := diff.ColorizeDiffLines(lines, filePath)
	for _, line := range colored {
		fmt.Println(line)
	}
	fmt.Println()
}

func extractDiffFilePath(header string) string {
	if !strings.HasPrefix(header, "diff -- ") {
		return ""
	}
	rest := strings.TrimPrefix(header, "diff -- ")
	if idx := strings.Index(rest, " (+"); idx >= 0 {
		return rest[:idx]
	}
	if idx := strings.Index(rest, " (-"); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

func RenderPromptFrame(session *term.LineReadSession, mode env.PermissionMode,
	providerName, model, workdir string, queueLines []string, running bool) {
	cols := term.TerminalColumns()
	session.FrameLines = 0

	for _, line := range queueLines {
		fmt.Print(formatFrameLine(line, cols))
		fmt.Print("\r\n")
		session.FrameLines++
	}

	session.Ed.PromptWidth = PrintPrompt(mode)
	if len(session.Ed.Runes) > 0 {
		fmt.Print(session.Ed.String())
	}
	fmt.Print("\r\n")
	session.FrameLines++
	printPromptFooter(session, mode, providerName, model, workdir, cols, running)
	session.FrameLines++
	moveCursorToPromptLine(session)

	// 重新画完 prompt frame 后,光标在 prompt 行起点,行占用计数清零,
	// 避免上条输入残留的值干扰下次 RedrawFull 的回退/清屏逻辑。
	session.Ed.LastEndRow = 0
	session.Ed.LastCursorRow = 0
}

func RerenderPromptFrame(session *term.LineReadSession, mode env.PermissionMode,
	providerName, model, workdir string, queueLines []string, running bool) {
	clearPromptFrame(session)
	RenderPromptFrame(session, mode, providerName, model, workdir, queueLines, running)
}

func DetachPromptFrame(session *term.LineReadSession) {
	clearPromptFrame(session)
}

func moveCursorToPromptLine(session *term.LineReadSession) {
	cursor := session.Ed.PromptWidth + session.Ed.DisplayWidthRange(0, session.Ed.Pos)
	fmt.Print("\033[1A\r")
	if cursor > 0 {
		fmt.Printf("\033[%dC", cursor)
	}
}

func clearPromptFrame(session *term.LineReadSession) {
	if session.FrameLines <= 0 {
		return
	}
	// FrameLines 统计的是"逻辑行"数(queue N + prompt 1 + footer 1)。
	// 但如果用户在 prompt 里输入了长到自动折行的内容,prompt 实际在终端
	// 上占了 1 + LastEndRow 个物理行。清屏时必须把折行带来的额外物理行
	// 也一起清掉,否则多出来的那几行会残留在屏幕上形成"幻影"。
	totalRows := session.FrameLines + session.Ed.LastEndRow
	// 此刻光标在 prompt 行(RenderPromptFrame 结尾 moveCursorToPromptLine
	// 把光标移到 prompt 首列);上方是 queue 行,下方是 prompt 折行 + footer。
	// 需要先回到整帧顶部(queue 的第一行):
	//   上移 = queueLines(= FrameLines - 2,因为 FrameLines 包含 prompt+footer)
	if up := session.FrameLines - 2; up > 0 {
		fmt.Printf("\033[%dA", up)
	}
	fmt.Print("\r")
	for i := 0; i < totalRows; i++ {
		fmt.Print("\033[2K")
		if i < totalRows-1 {
			fmt.Print("\033[1B\r")
		}
	}
	if totalRows > 1 {
		fmt.Printf("\033[%dA", totalRows-1)
	}
	fmt.Print("\r")
	session.FrameLines = 0
}

func printPromptFooter(session *term.LineReadSession, mode env.PermissionMode, providerName, model, workdir string, cols int, running bool) {
	footer := fmt.Sprintf("Mode: %s  Provider: %s  Model: %s  Dir: %s", mode, providerName, model, workdir)
	if running && HasDraftInput(session) {
		footer += "  Tab to queue message"
	}
	fmt.Print(formatFrameLine(footer, cols))
}

func PrintPrompt(m env.PermissionMode) int {
	switch m {
	case env.ModePlan:
		fmt.Print("\033[1;33m>\033[0m ")
		return 2
	case env.ModeDefault:
		fmt.Print("\033[1;36m>\033[0m ")
		return 2
	case env.ModeAcceptEdits:
		fmt.Print("\033[1;32m>\033[0m ")
		return 2
	case env.ModeAuto:
		fmt.Print("\033[1;35m>\033[0m ")
		return 2
	case env.ModeRestricted:
		fmt.Print("\033[1;34m>\033[0m ")
		return 2
	case env.ModeBypassPermissions:
		fmt.Print("\033[1;31m>\033[0m ")
		return 2
	}
	return 0
}

func BuildPromptHeaderLines(activity *Activity, queueLines []string, running bool, completionLines ...[]string) []string {
	lines := make([]string, 0, 8)
	if running {
		lines = append(lines, activity.RenderLines()...)
	}
	lines = append(lines, queueLines...)
	if len(completionLines) > 0 && len(completionLines[0]) > 0 {
		lines = append(lines, completionLines[0]...)
	}
	return lines
}

func BuildToolCallSummary(messages []llm.Message) string {
	var actions []string
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ToolName != "" {
				summary := summarizeToolAction(b.ToolName, b.Input)
				if summary != "" {
					actions = append(actions, summary)
				}
			}
		}
	}
	if len(actions) == 0 {
		return ""
	}
	return strings.Join(actions, "\n")
}

func summarizeToolAction(toolName string, input json.RawMessage) string {
	var args map[string]json.RawMessage
	json.Unmarshal(input, &args)

	switch {
	case strings.HasSuffix(toolName, ".write_file"):
		path := unquoteJSON(args["path"])
		if path != "" {
			return fmt.Sprintf("写入 %s", path)
		}
		return "写入文件"
	case strings.HasSuffix(toolName, ".read_file"):
		path := unquoteJSON(args["path"])
		if path != "" {
			return fmt.Sprintf("读取 %s", path)
		}
		return "读取文件"
	case strings.HasSuffix(toolName, ".shell_exec"):
		cmd := unquoteJSON(args["command"])
		if cmd != "" {
			return fmt.Sprintf("执行: %s", CompactPreview(cmd, 60))
		}
		return "执行命令"
	case strings.HasSuffix(toolName, ".search"):
		query := unquoteJSON(args["query"])
		if query != "" {
			return fmt.Sprintf("搜索: %s", query)
		}
		return "搜索"
	default:
		if label, ok := toolLabelZh[toolName]; ok {
			return label
		}
		return fmt.Sprintf("调用 %s", toolName)
	}
}

var toolLabelZh = map[string]string{
	"data.get_candles":         "查询 K 线数据",
	"data.get_snapshot":        "查询市场快照",
	"data.get_feature_vector":  "获取特征向量",
	"data.provider_health":     "检查数据源健康",
	"data.validation_stats":    "查询数据质量统计",
	"data.backfill_status":     "查询回填进度",
	"data.active_instruments":  "查询活跃品种",
	"data.replay_start":        "启动历史回放",
	"data.replay_stop":         "停止历史回放",
	"quant.global_portfolio":   "查询全局投资组合",
	"quant.global_risk_status": "查询全局风控状态",
	"quant.strategy_weights":   "查询策略权重",
	"quant.daily_pnl":          "查询当日损益",
	"quant.account_status":     "查询账户状态",
	"quant.pause_trading":      "暂停交易",
	"quant.resume_trading":     "恢复交易",
	"quant.account_pause":      "暂停账户交易",
	"quant.account_resume":     "恢复账户交易",
	"quant.account_close_all":  "全部平仓",
	"quant.force_close":        "强制平仓",
	"quant.trace_query":        "查询信号追踪",
	"quant.trade_history":      "查询交易历史",
	"quant.backtest_start":     "启动回测",
}

func unquoteJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), "\"")
}

func formatFrameLine(line string, cols int) string {
	line = strings.Join(strings.Fields(line), " ")
	if cols <= 0 {
		return line
	}
	if cols > 1 {
		cols--
	}
	return truncateDisplayWidth(line, cols)
}

func truncateDisplayWidth(text string, maxWidth int) string {
	if maxWidth <= 0 || DisplayWidth(text) <= maxWidth {
		return text
	}
	if maxWidth <= 3 {
		return strings.Repeat(".", maxWidth)
	}

	var (
		b     strings.Builder
		width int
	)

	for _, r := range text {
		rw := term.RuneWidth(r)
		if width+rw > maxWidth-3 {
			break
		}
		b.WriteRune(r)
		width += rw
	}

	return strings.TrimRightFunc(b.String(), unicode.IsSpace) + "..."
}

func DisplayWidth(text string) int {
	width := 0
	for _, r := range text {
		width += term.RuneWidth(r)
	}
	return width
}

func CompactPreview(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if max > 0 && DisplayWidth(text) > max {
		return truncateDisplayWidth(text, max)
	}
	return text
}

func HandleChatRunResult(state *State, provider llm.Provider, brainID string,
	maxTurns int, session *term.LineReadSession, mode env.PermissionMode, providerName, model,
	workdir string, queueLines []string, running *bool, rr RunResult,
	resultCh chan<- RunResult, progressCh chan<- ProgressEvent,
	activity *Activity, stdinCh <-chan []byte, stdinErrCh <-chan error) {

	DetachPromptFrame(session)
	defer RenderPromptFrame(session, state.Mode, providerName, model, workdir, queueLines, *running)
	defer func() {
		if !*running {
			activity.Stop()
			if state.PlanResumeAfterRun {
				state.PlanResumeAfterRun = false
				state.SwitchMode(env.ModePlan)
				fmt.Println("  \033[2m(returned to plan mode)\033[0m")
				fmt.Println()
			}
		}
	}()

	if rr.Canceled {
		fmt.Println("  \033[1;33m! Cancelled\033[0m")
		fmt.Println()
		return
	}
	if rr.Err != nil {
		fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %v\033[0m\n\n", rr.Err)
		return
	}

	if rr.Result != nil && rr.Result.Run.State == loop.StateFailed {
		errMsg := LastTurnError(rr.Result)
		if errMsg == "" {
			errMsg = "unexpected error: run failed"
		}
		fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %s\033[0m\n\n", errMsg)
		return
	}

	state.Messages = rr.Result.FinalMessages

	replyText := rr.ReplyText
	if strings.TrimSpace(replyText) == "" {
		replyText = strings.TrimSpace(activity.Content.String())
	}
	if strings.TrimSpace(replyText) == "" {
		replyText = BuildToolCallSummary(rr.Result.FinalMessages)
	}

	if shouldPrintAssistantReply(activity.Content.String(), replyText) {
		PrintAssistantMessage(replyText)
	}

	elapsed := rr.Result.Run.Budget.ElapsedTime.Milliseconds()
	unit := "ms"
	val := elapsed
	if elapsed >= 1000 {
		unit = "s"
		val = elapsed / 1000
	}
	fmt.Printf("\033[2m[turns:%d llm:%d tools:%d %d%s]\033[0m\n\n",
		rr.Result.Run.Budget.UsedTurns,
		rr.Result.Run.Budget.UsedLLMCalls,
		rr.Result.Run.Budget.UsedToolCalls,
		val, unit)

	if shouldShowResponseSelector(state.BrainID, replyText) {
		outcome := showResponseSelector(state.Mode, stdinCh, stdinErrCh)
		if outcome.followUp != "" {
			if outcome.planProceed && state.Mode == env.ModePlan {
				state.SwitchMode(env.ModeAcceptEdits)
				state.PlanResumeAfterRun = true
			}
			activity.Start()
			StartChatRun(state, provider, brainID, maxTurns, outcome.followUp, resultCh, progressCh)
			*running = true
			queueLines = BuildPromptHeaderLines(activity, state.QueueDisplayLines(), *running)
		}
	}
}

func shouldPrintAssistantReply(streamedContent, replyText string) bool {
	replyText = strings.TrimSpace(replyText)
	if replyText == "" {
		return false
	}
	// 正文已经在 running 期间通过 ProgressContent 实时打到终端时,
	// 结束态不要再重复整段打印一次,否则 transcript 会出现两份内容。
	if strings.TrimSpace(streamedContent) != "" {
		return false
	}
	return true
}

func shouldShowResponseSelector(brainID, replyText string) bool {
	if brainID != "central" || replyText == "" {
		return false
	}

	numbered := 0
	for _, line := range strings.Split(replyText, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 2 && line[0] >= '1' && line[0] <= '9' && (strings.HasPrefix(line[1:], ".") || strings.HasPrefix(line[1:], "、")) {
			numbered++
		}
	}
	return numbered >= 2
}

type selectorOutcome struct {
	followUp    string
	planProceed bool
}

func showResponseSelector(mode env.PermissionMode, stdinCh <-chan []byte, stdinErrCh <-chan error) selectorOutcome {
	var options []term.SelectorOption

	switch mode {
	case env.ModePlan:
		options = []term.SelectorOption{
			{Label: "Looks good, make it happen", Value: "proceed"},
			{Label: "Try a different approach", Value: "regen"},
			{Label: "Give feedback...", Value: "feedback", IsInput: true},
		}
	default:
		options = []term.SelectorOption{
			{Label: "Continue", Value: "continue"},
			{Label: "Try a different approach", Value: "regen"},
			{Label: "Give feedback...", Value: "feedback", IsInput: true},
		}
	}

	fmt.Println("  \033[2mUp/Down select, Enter confirm, Esc skip\033[0m")
	result := term.RunSelectorWithChan(options, stdinCh, stdinErrCh)

	if result.Cancelled {
		return selectorOutcome{}
	}

	switch result.Value {
	case "proceed":
		fmt.Println("  \033[32m> Proceeding (switching to accept-edits for execution)\033[0m")
		fmt.Println()
		return selectorOutcome{
			followUp:    "Good, please proceed with this plan. Execute all the steps you outlined.",
			planProceed: true,
		}
	case "continue":
		fmt.Println("  \033[32m> Continue\033[0m")
		fmt.Println()
		return selectorOutcome{followUp: "Please continue."}
	case "regen":
		fmt.Println("  \033[33m> Regenerating\033[0m")
		fmt.Println()
		return selectorOutcome{followUp: "Please propose a different approach. The previous response is not what I want — try again."}
	case "feedback":
		if result.UserInput == "" {
			return selectorOutcome{}
		}
		fmt.Printf("  \033[36m> %s\033[0m\n", result.UserInput)
		fmt.Println()
		return selectorOutcome{followUp: result.UserInput}
	}
	return selectorOutcome{}
}

func LastTurnError(rr *loop.RunResult) string {
	if rr == nil || len(rr.Turns) == 0 {
		return ""
	}
	last := rr.Turns[len(rr.Turns)-1]
	if last.Error == nil {
		return ""
	}
	return last.Error.Error()
}

func HasDraftInput(session *term.LineReadSession) bool {
	return strings.TrimSpace(session.Editor().String()) != ""
}
