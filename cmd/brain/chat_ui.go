package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

// ---------------------------------------------------------------------------
// Transcript display
// ---------------------------------------------------------------------------

func printUserMessage(input string) {
	printTranscriptBlock("\033[1;32m>\033[0m", input)
}

func printAssistantMessage(reply string) {
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

func printDiffPreviewBlock(lines []string) {
	if len(lines) == 0 {
		return
	}
	// Extract file path from first line for syntax highlighting.
	filePath := extractDiffFilePath(lines[0])
	colored := colorizeDiffLines(lines, filePath)
	for _, line := range colored {
		fmt.Println(line)
	}
	fmt.Println()
}

// extractDiffFilePath extracts the file path from a diff header line.
// Format: "diff -- /path/to/file (+1 -0)"
func extractDiffFilePath(header string) string {
	if !strings.HasPrefix(header, "diff -- ") {
		return ""
	}
	rest := strings.TrimPrefix(header, "diff -- ")
	// Path ends before " (+" or at end of line.
	if idx := strings.Index(rest, " (+"); idx >= 0 {
		return rest[:idx]
	}
	if idx := strings.Index(rest, " (-"); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

// ---------------------------------------------------------------------------
// Prompt frame rendering
// ---------------------------------------------------------------------------

func renderPromptFrame(session *lineReadSession, mode chatMode,
	providerName, model, workdir string, queueLines []string, running bool) {
	cols := terminalColumns()
	session.frameLines = 0

	for _, line := range queueLines {
		fmt.Print(formatFrameLine(line, cols))
		fmt.Print("\r\n")
		session.frameLines++
	}

	session.ed.promptWidth = printPrompt(mode)
	if len(session.ed.runes) > 0 {
		fmt.Print(session.ed.string())
	}
	fmt.Print("\r\n")
	session.frameLines++
	printPromptFooter(session, mode, providerName, model, workdir, cols, running)
	session.frameLines++
	moveCursorToPromptLine(session)
}

func rerenderPromptFrame(session *lineReadSession, mode chatMode,
	providerName, model, workdir string, queueLines []string, running bool) {
	clearPromptFrame(session)
	renderPromptFrame(session, mode, providerName, model, workdir, queueLines, running)
}

func detachPromptFrame(session *lineReadSession) {
	clearPromptFrame(session)
}

func moveCursorToPromptLine(session *lineReadSession) {
	cursor := session.ed.promptWidth + session.ed.displayWidthRange(0, session.ed.pos)
	fmt.Print("\033[1A\r")
	if cursor > 0 {
		fmt.Printf("\033[%dC", cursor)
	}
}

func clearPromptFrame(session *lineReadSession) {
	if session.frameLines <= 0 {
		return
	}
	if up := session.frameLines - 2; up > 0 {
		fmt.Printf("\033[%dA", up)
	}
	fmt.Print("\r")
	for i := 0; i < session.frameLines; i++ {
		fmt.Print("\033[2K")
		if i < session.frameLines-1 {
			fmt.Print("\033[1B\r")
		}
	}
	if session.frameLines > 1 {
		fmt.Printf("\033[%dA", session.frameLines-1)
	}
	fmt.Print("\r")
	session.frameLines = 0
}

func printPromptFooter(session *lineReadSession, mode chatMode, providerName, model, workdir string, cols int, running bool) {
	footer := fmt.Sprintf("Mode: %s  Provider: %s  Model: %s  Dir: %s", mode, providerName, model, workdir)
	if running && hasDraftInput(session) {
		footer += "  Tab to queue message"
	}
	fmt.Print(formatFrameLine(footer, cols))
}

func printPrompt(m chatMode) int {
	switch m {
	case modePlan:
		fmt.Print("\033[1;33m>\033[0m ")
		return 2
	case modeDefault:
		fmt.Print("\033[1;36m>\033[0m ")
		return 2
	case modeAcceptEdits:
		fmt.Print("\033[1;32m>\033[0m ")
		return 2
	case modeAuto:
		fmt.Print("\033[1;35m>\033[0m ")
		return 2
	case modeRestricted:
		fmt.Print("\033[1;34m>\033[0m ")
		return 2
	case modeBypassPermissions:
		fmt.Print("\033[1;31m>\033[0m ")
		return 2
	}
	return 0
}

func buildPromptHeaderLines(activity *chatActivity, queueLines []string, running bool) []string {
	lines := make([]string, 0, 8)
	if running {
		lines = append(lines, activity.renderLines()...)
	}
	lines = append(lines, queueLines...)
	return lines
}

// buildToolCallSummary generates a brief summary when the LLM returned
// tool calls but no text reply.
func buildToolCallSummary(messages []llm.Message) string {
	var actions []string
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "tool_use" && b.ToolName != "" {
				// Extract a short description from tool name + args.
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
			return fmt.Sprintf("Wrote %s", path)
		}
		return "Wrote file"
	case strings.HasSuffix(toolName, ".read_file"):
		path := unquoteJSON(args["path"])
		if path != "" {
			return fmt.Sprintf("Read %s", path)
		}
		return "Read file"
	case strings.HasSuffix(toolName, ".shell_exec"):
		cmd := unquoteJSON(args["command"])
		if cmd != "" {
			return fmt.Sprintf("Ran: %s", compactPreview(cmd, 60))
		}
		return "Ran command"
	case strings.HasSuffix(toolName, ".search"):
		query := unquoteJSON(args["query"])
		if query != "" {
			return fmt.Sprintf("Searched: %s", query)
		}
		return "Searched"
	default:
		return fmt.Sprintf("Called %s", toolName)
	}
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

// ---------------------------------------------------------------------------
// Text formatting
// ---------------------------------------------------------------------------

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
	if maxWidth <= 0 || displayWidth(text) <= maxWidth {
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
		rw := runeWidth(r)
		if width+rw > maxWidth-3 {
			break
		}
		b.WriteRune(r)
		width += rw
	}

	return strings.TrimRightFunc(b.String(), unicode.IsSpace) + "..."
}

func displayWidth(text string) int {
	width := 0
	for _, r := range text {
		width += runeWidth(r)
	}
	return width
}

func compactPreview(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if max > 0 && displayWidth(text) > max {
		return truncateDisplayWidth(text, max)
	}
	return text
}

// ---------------------------------------------------------------------------
// Result handling
// ---------------------------------------------------------------------------

func handleChatRunResult(state *chatState, provider llm.Provider, brainID string,
	maxTurns int, session *lineReadSession, mode chatMode, providerName, model,
	workdir string, queueLines []string, running *bool, rr chatRunResult,
	resultCh chan<- chatRunResult, progressCh chan<- chatProgressEvent,
	activity *chatActivity, stdinCh <-chan []byte, stdinErrCh <-chan error) {

	detachPromptFrame(session)
	defer renderPromptFrame(session, mode, providerName, model, workdir, queueLines, *running)
	defer func() {
		if !*running {
			activity.stop()
		}
	}()

	if rr.canceled {
		fmt.Println("  \033[1;33m! Cancelled\033[0m")
		fmt.Println()
		return
	}
	if rr.err != nil {
		fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %v\033[0m\n\n", rr.err)
		return
	}

	// Check for LLM errors buried in TurnResults (Runner.Execute returns nil
	// error even when the LLM call fails — the error is inside the last Turn).
	if rr.result != nil && rr.result.Run.State == loop.StateFailed {
		if errMsg := lastTurnError(rr.result); errMsg != "" {
			fmt.Fprintf(os.Stderr, "\033[1;31m! Error: %s\033[0m\n\n", errMsg)
			return
		}
	}

	state.messages = rr.result.FinalMessages

	replyText := rr.replyText
	if strings.TrimSpace(replyText) == "" {
		replyText = strings.TrimSpace(activity.content.String())
	}
	if strings.TrimSpace(replyText) == "" {
		// LLM returned no text — generate summary from tool calls.
		replyText = buildToolCallSummary(rr.result.FinalMessages)
	}

	if replyText != "" {
		printAssistantMessage(replyText)
	}

	elapsed := rr.result.Run.Budget.ElapsedTime.Milliseconds()
	unit := "ms"
	val := elapsed
	if elapsed >= 1000 {
		unit = "s"
		val = elapsed / 1000
	}
	fmt.Printf("\033[2m[turns:%d llm:%d tools:%d %d%s]\033[0m\n\n",
		rr.result.Run.Budget.UsedTurns,
		rr.result.Run.Budget.UsedLLMCalls,
		rr.result.Run.Budget.UsedToolCalls,
		val, unit)

	if shouldShowResponseSelector(state.brainID, replyText) {
		followUp := showResponseSelector(state.mode, stdinCh, stdinErrCh)
		if followUp != "" {
			activity.start()
			startChatRun(state, provider, brainID, maxTurns, followUp, resultCh, progressCh)
			*running = true
			queueLines = buildPromptHeaderLines(activity, state.queueDisplayLines(), *running)
		}
	}
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

func showResponseSelector(mode chatMode, stdinCh <-chan []byte, stdinErrCh <-chan error) string {
	var options []SelectorOption

	switch mode {
	case modePlan:
		options = []SelectorOption{
			{Label: "Looks good, make it happen", Value: "proceed"},
			{Label: "Try a different approach", Value: "regen"},
			{Label: "Give feedback...", Value: "feedback", IsInput: true},
		}
	default:
		options = []SelectorOption{
			{Label: "Continue", Value: "continue"},
			{Label: "Try a different approach", Value: "regen"},
			{Label: "Give feedback...", Value: "feedback", IsInput: true},
		}
	}

	fmt.Println("  \033[2mUp/Down select, Enter confirm, Esc skip\033[0m")
	result := RunSelectorWithChan(options, stdinCh, stdinErrCh)

	if result.Cancelled {
		return ""
	}

	switch result.Value {
	case "proceed":
		fmt.Println("  \033[32m> Proceeding\033[0m")
		fmt.Println()
		return "Good, please proceed with this plan. Execute all the steps you outlined."
	case "continue":
		fmt.Println("  \033[32m> Continue\033[0m")
		fmt.Println()
		return "Please continue."
	case "regen":
		fmt.Println("  \033[33m> Regenerating\033[0m")
		fmt.Println()
		return "Please propose a different approach. The previous response is not what I want — try again."
	case "feedback":
		if result.UserInput == "" {
			return ""
		}
		fmt.Printf("  \033[36m> %s\033[0m\n", result.UserInput)
		fmt.Println()
		return result.UserInput
	}
	return ""
}

// lastTurnError extracts the error message from the last failed Turn in a
// RunResult. Runner.Execute returns (result, nil) even when the LLM call
// fails — the error is only inside TurnResult.Error. This helper surfaces
// it so the UI can display it to the user instead of silently swallowing it.
func lastTurnError(rr *loop.RunResult) string {
	if rr == nil || len(rr.Turns) == 0 {
		return ""
	}
	last := rr.Turns[len(rr.Turns)-1]
	if last.Error == nil {
		return ""
	}
	return last.Error.Error()
}
