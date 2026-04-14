package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

// ---------------------------------------------------------------------------
// Progress event types
// ---------------------------------------------------------------------------

type chatProgressEventKind string

const (
	progressContent   chatProgressEventKind = "content"
	progressThinking  chatProgressEventKind = "thinking"
	progressToolPlan  chatProgressEventKind = "tool_plan"
	progressToolStart chatProgressEventKind = "tool_start"
	progressToolEnd   chatProgressEventKind = "tool_end"
	progressFinished  chatProgressEventKind = "finished"
)

type chatProgressEvent struct {
	kind         chatProgressEventKind
	text         string
	toolName     string
	args         string
	detail       string
	ok           bool
	previewLines []string
}

// ---------------------------------------------------------------------------
// chatActivity — accumulates activity state for display
// ---------------------------------------------------------------------------

type chatActivity struct {
	startedAt   time.Time
	status      string
	currentTool string
	content     strings.Builder
	events      []string
}

func (a *chatActivity) start() {
	a.startedAt = time.Now()
	a.status = "Thinking"
	a.currentTool = ""
	a.content.Reset()
	a.events = nil
}

func (a *chatActivity) stop() {
	a.startedAt = time.Time{}
	a.status = ""
	a.currentTool = ""
	a.content.Reset()
	a.events = nil
}

func (a *chatActivity) running() bool {
	return !a.startedAt.IsZero()
}

func (a *chatActivity) apply(ev chatProgressEvent) bool {
	if a == nil {
		return false
	}
	if !a.running() {
		a.start()
	}

	switch ev.kind {
	case progressThinking:
		a.status = "Thinking"
	case progressContent:
		a.content.WriteString(ev.text)
		if strings.TrimSpace(a.content.String()) != "" {
			a.status = "Drafting response"
		}
	case progressToolPlan:
		a.currentTool = ev.toolName
		a.status = "Preparing tool " + ev.toolName
		a.appendEvent(renderToolEvent("Plan", ev.toolName, ev.args))
	case progressToolStart:
		a.currentTool = ev.toolName
		a.status = "Running tool " + ev.toolName
		a.appendEvent(renderToolEvent("Run", ev.toolName, ev.args))
	case progressToolEnd:
		a.currentTool = ""
		if ev.ok {
			a.status = "Continuing after tool"
			a.appendEvent(renderToolEvent("Done", ev.toolName, ev.detail))
		} else {
			a.status = "Tool error"
			a.appendEvent(renderToolEvent("Error", ev.toolName, ev.detail))
		}
	case progressFinished:
		a.currentTool = ""
		a.status = "Finalizing response"
	}
	return true
}

func (a *chatActivity) appendEvent(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	a.events = append(a.events, line)
	if len(a.events) > 3 {
		a.events = a.events[len(a.events)-3:]
	}
}

func (a *chatActivity) renderLines() []string {
	if a == nil || !a.running() {
		return nil
	}

	var lines []string
	lines = append(lines, previewContentLines(a.content.String(), 3)...)
	lines = append(lines, tailStrings(a.events, 2)...)
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}

	status := fmt.Sprintf("Working (%s", formatElapsed(time.Since(a.startedAt)))
	switch {
	case a.currentTool != "":
		status += " · tool " + a.currentTool
	case a.status != "":
		status += " · " + strings.ToLower(a.status)
	}
	status += " · Esc to interrupt)"
	lines = append(lines, status)

	return lines
}

// ---------------------------------------------------------------------------
// chatLiveReporter — implements StreamConsumer + ToolObserver
// ---------------------------------------------------------------------------

type chatLiveReporter struct {
	ch      chan<- chatProgressEvent
	workdir string

	// Snapshot for post-execution diff: tools execute serially in Runner,
	// so a single pending snapshot is sufficient.
	pendingSnap *fileSnapshot
}

// fileSnapshot holds the pre-execution state of a file for diff generation.
type fileSnapshot struct {
	path       string // absolute path
	oldContent string
	oldExists  bool
}

func (r *chatLiveReporter) emit(ev chatProgressEvent) {
	if r == nil || r.ch == nil {
		return
	}
	select {
	case r.ch <- ev:
	default:
	}
}

func (r *chatLiveReporter) OnMessageStart(_ context.Context, _ *loop.Run, _ *loop.Turn) {
	r.emit(chatProgressEvent{kind: progressThinking})
}

func (r *chatLiveReporter) OnContentDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, text string) {
	if text == "" {
		return
	}
	r.emit(chatProgressEvent{kind: progressContent, text: text})
}

func (r *chatLiveReporter) OnToolCallDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, argsPartial string) {
	if toolName == "" {
		return
	}
	r.emit(chatProgressEvent{
		kind:     progressToolPlan,
		toolName: toolName,
		args:     compactPreview(argsPartial, 88),
	})
}

func (r *chatLiveReporter) OnMessageDelta(_ context.Context, _ *loop.Run, _ *loop.Turn, _ json.RawMessage) {
}

func (r *chatLiveReporter) OnMessageEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, _ llm.Usage) {
	r.emit(chatProgressEvent{kind: progressFinished})
}

func (r *chatLiveReporter) OnToolStart(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, input json.RawMessage) {
	// Snapshot file before execution for post-execution diff.
	r.pendingSnap = snapshotForTool(r.workdir, toolName, input)

	r.emit(chatProgressEvent{
		kind:     progressToolStart,
		toolName: toolName,
		args:     compactPreview(string(input), 88),
	})
}

func (r *chatLiveReporter) OnToolEnd(_ context.Context, _ *loop.Run, _ *loop.Turn, toolName string, ok bool, output json.RawMessage) {
	var diffLines []string
	if r.pendingSnap != nil {
		diffLines = buildPostExecDiff(r.pendingSnap, 20)
	}
	r.pendingSnap = nil

	detail := ""
	if !ok && len(output) > 0 {
		detail = compactPreview(string(output), 88)
	}

	r.emit(chatProgressEvent{
		kind:         progressToolEnd,
		toolName:     toolName,
		ok:           ok,
		detail:       detail,
		previewLines: diffLines,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
