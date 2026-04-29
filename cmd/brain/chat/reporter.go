package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/leef-l/brain/cmd/brain/diff"
	"github.com/leef-l/brain/cmd/brain/term"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

type ProgressEventKind string

const (
	ProgressContent   ProgressEventKind = "content"
	ProgressThinking  ProgressEventKind = "thinking"
	ProgressToolPlan  ProgressEventKind = "tool_plan"
	ProgressToolStart ProgressEventKind = "tool_start"
	ProgressToolEnd   ProgressEventKind = "tool_end"
	ProgressFinished  ProgressEventKind = "finished"
)

type ProgressEvent struct {
	RunID        string
	Kind         ProgressEventKind
	Text         string
	ToolName     string
	Args         string
	Detail       string
	OK           bool
	PreviewLines []string
}

// ChatEvent 是统一的事件通道类型，repl.go 通过单个 select 消费所有 run 的事件。
type ChatEvent struct {
	RunID    string
	Type     string // "progress" | "result"
	Progress *ProgressEvent
	Result   *RunResult
}

type Activity struct {
	RunID       string
	Input       string
	StartedAt   time.Time
	Status      string
	CurrentTool string
	Content     strings.Builder
	Events      []string
}

func (a *Activity) Start() {
	a.StartedAt = time.Now()
	a.Status = "Thinking"
	a.CurrentTool = ""
	a.Content.Reset()
	a.Events = nil
}

func (a *Activity) Stop() {
	a.StartedAt = time.Time{}
	a.Status = ""
	a.CurrentTool = ""
	a.Content.Reset()
	a.Events = nil
}

func (a *Activity) Running() bool {
	return !a.StartedAt.IsZero()
}

func (a *Activity) Apply(ev ProgressEvent) bool {
	if a == nil {
		return false
	}
	if !a.Running() {
		a.Start()
	}

	switch ev.Kind {
	case ProgressThinking:
		a.Status = "Thinking"
	case ProgressContent:
		a.Content.WriteString(ev.Text)
		if strings.TrimSpace(a.Content.String()) != "" {
			a.Status = "Drafting response"
		}
	case ProgressToolPlan:
		a.CurrentTool = ev.ToolName
		a.Status = "Preparing tool " + ev.ToolName
		a.appendEvent(renderToolEvent("Plan", ev.ToolName, ev.Args))
	case ProgressToolStart:
		a.CurrentTool = ev.ToolName
		a.Status = "Running tool " + ev.ToolName
		a.appendEvent(renderToolEvent("Run", ev.ToolName, ev.Args))
	case ProgressToolEnd:
		a.CurrentTool = ""
		if ev.OK {
			a.Status = "Continuing after tool"
			a.appendEvent(renderToolEvent("Done", ev.ToolName, ev.Detail))
		} else {
			a.Status = "Tool error"
			a.appendEvent(renderToolEvent("Error", ev.ToolName, ev.Detail))
		}
	case ProgressFinished:
		a.CurrentTool = ""
		a.Status = "Finalizing response"
	}
	return true
}

func (a *Activity) appendEvent(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	a.Events = append(a.Events, line)
	if len(a.Events) > 3 {
		a.Events = a.Events[len(a.Events)-3:]
	}
}

func (a *Activity) RenderLines() []string {
	if a == nil || !a.Running() {
		return nil
	}

	maxContent := term.TerminalRows() - 4
	if maxContent < 3 {
		maxContent = 3
	}
	maxTotal := maxContent + 2

	var lines []string
	lines = append(lines, previewContentLines(a.Content.String(), maxContent)...)
	lines = append(lines, tailStrings(a.Events, 2)...)
	if len(lines) > maxTotal {
		lines = lines[len(lines)-maxTotal:]
	}

	status := fmt.Sprintf("Working (%s", formatElapsed(time.Since(a.StartedAt)))
	switch {
	case a.CurrentTool != "":
		status += " · tool " + a.CurrentTool
	case a.Status != "":
		status += " · " + strings.ToLower(a.Status)
	}
	status += " · Esc to interrupt)"
	lines = append(lines, status)

	return lines
}

func (a *Activity) StatusLine() string {
	if a == nil || !a.Running() {
		return ""
	}
	status := fmt.Sprintf("Working (%s", formatElapsed(time.Since(a.StartedAt)))
	switch {
	case a.CurrentTool != "":
		status += " · tool " + a.CurrentTool
	case a.Status != "":
		status += " · " + strings.ToLower(a.Status)
	}
	status += " · Esc to interrupt)"
	return status
}

type LiveReporter struct {
	RunID   string
	Ch      chan<- ChatEvent
	Workdir string

	pendingSnap *diff.FileSnapshot
}

func (r *LiveReporter) emit(ev ProgressEvent) {
	if r == nil || r.Ch == nil {
		return
	}
	ev.RunID = r.RunID
	select {
	case r.Ch <- ChatEvent{RunID: r.RunID, Type: "progress", Progress: &ev}:
	default:
	}
}

func (r *LiveReporter) OnMessageStart(_ context.Context, _ *loop.Run, _ *loop.Turn) {
	r.emit(ProgressEvent{Kind: ProgressThinking})
}

func (r *LiveReporter) OnContentDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, text string) {
	if text == "" {
		return
	}
	r.emit(ProgressEvent{Kind: ProgressContent, Text: text})
}

func (r *LiveReporter) OnToolCallDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, argsPartial string) {
	if toolName == "" {
		return
	}
	r.emit(ProgressEvent{
		Kind:     ProgressToolPlan,
		ToolName: toolName,
		Args:     CompactPreview(argsPartial, 88),
	})
}

func (r *LiveReporter) OnMessageDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, _ json.RawMessage) {
}

func (r *LiveReporter) OnMessageEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, _ llm.Usage) {
	r.emit(ProgressEvent{Kind: ProgressFinished})
}

func (r *LiveReporter) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, input json.RawMessage) {
	r.pendingSnap = diff.SnapshotForTool(r.Workdir, toolName, input)

	r.emit(ProgressEvent{
		Kind:     ProgressToolStart,
		ToolName: toolName,
		Args:     CompactPreview(string(input), 88),
	})
}

func (r *LiveReporter) OnToolEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	var diffLines []string
	if r.pendingSnap != nil {
		diffLines = diff.BuildPostExecDiff(r.pendingSnap, 20)
	}
	r.pendingSnap = nil

	detail := ""
	if !ok && len(output) > 0 {
		detail = CompactPreview(string(output), 88)
		// 美化常见的 sandbox 受限错误
		if strings.Contains(detail, "restricted policy") {
			detail = "sandbox: command tried to modify files outside allowed scope (restricted mode)"
		} else if strings.Contains(detail, "sandbox escape") {
			detail = "sandbox: command tried to escape working directory"
		}
	}

	r.emit(ProgressEvent{
		Kind:         ProgressToolEnd,
		ToolName:     toolName,
		OK:           ok,
		Detail:       detail,
		PreviewLines: diffLines,
	})
}

func renderToolEvent(prefix, toolName, detail string) string {
	line := prefix + ": " + toolName
	if strings.TrimSpace(detail) != "" {
		line += " " + detail
	}
	return line
}

func previewContentLines(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, limit)
	for i := len(parts) - 1; i >= 0 && len(lines) < limit; i-- {
		line := strings.TrimRightFunc(parts[i], unicode.IsSpace)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

func tailStrings(items []string, limit int) []string {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) <= limit {
		out := make([]string, len(items))
		copy(out, items)
		return out
	}
	out := make([]string, limit)
	copy(out, items[len(items)-limit:])
	return out
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d/time.Second)%60)
}

func CompactPreview(s string, max int) string {
	if max <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	s = strings.Join(out, " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
