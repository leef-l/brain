package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/diff"
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
	// ProgressWorkflowInit 表示 submit_workflow 工具刚刚提交了一组节点；
	// Args 字段携带 JSON 形式的 todos 列表（[{id,name,brain}...]）。
	ProgressWorkflowInit ProgressEventKind = "workflow_init"
	// ProgressWorkflowNode 表示某节点状态变化；ToolName=节点 ID，OK / Detail 表示状态。
	ProgressWorkflowNode ProgressEventKind = "workflow_node"
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
	// WorkflowState 仅在 ProgressWorkflowNode 时有意义：running / done / failed / skipped。
	WorkflowState string
}

// ChatEvent 是统一的事件通道类型，repl.go 通过单个 select 消费所有 run 的事件。
type ChatEvent struct {
	RunID    string
	Type     string // "progress" | "result"
	Progress *ProgressEvent
	Result   *RunResult
}

// activeChatChan 是当前活跃 chat session 的事件通道（chat 模式下一时刻只有一个 run 在跑，
// 所以全局变量足够）。LiveReporter 启动 LLM run 时会 SetActiveChatChan，结束时 Clear。
// workflow 工具 reporter 通过 EmitWorkflowEvent 把事件回传到这个通道，让 repl 的 progress
// ticker 看到 todo 列表更新。
var activeChatState struct {
	ch    chan<- ChatEvent
	runID string
}

// SetActiveChatChan 注册当前 chat run 的事件通道（每次 LLM run 启动时调用）。
func SetActiveChatChan(ch chan<- ChatEvent, runID string) {
	activeChatState.ch = ch
	activeChatState.runID = runID
}

// ClearActiveChatChan 清空当前 chat run（每次 LLM run 结束时调用）。
func ClearActiveChatChan() {
	activeChatState.ch = nil
	activeChatState.runID = ""
}

// EmitWorkflowNodeInit 在 submit_workflow 提交时为单个节点初始化 todo 项。
// 由 chat_aliases.go 中的 WorkflowProgressHook 钩子分节点回调。
func EmitWorkflowNodeInit(nodeID, name, brainKind string) {
	if activeChatState.ch == nil {
		return
	}
	// 编码为 JSON 节点列表（单节点）的 Args 字段，复用 ProgressWorkflowInit 的 Apply 逻辑
	nodesJSON := fmt.Sprintf(`[{"id":%q,"name":%q,"brain":%q}]`, nodeID, name, brainKind)
	select {
	case activeChatState.ch <- ChatEvent{
		RunID: activeChatState.runID,
		Type:  "progress",
		Progress: &ProgressEvent{
			RunID: activeChatState.runID,
			Kind:  ProgressWorkflowInit,
			Args:  nodesJSON,
		},
	}:
	default:
	}
}

// EmitWorkflowNodeState 由 submit_workflow 工具在节点状态变化时调用。
// state ∈ {running, done, failed, skipped}。
func EmitWorkflowNodeState(nodeID, state, errMsg string) {
	if activeChatState.ch == nil {
		return
	}
	select {
	case activeChatState.ch <- ChatEvent{
		RunID: activeChatState.runID,
		Type:  "progress",
		Progress: &ProgressEvent{
			RunID:         activeChatState.runID,
			Kind:          ProgressWorkflowNode,
			ToolName:      nodeID,
			WorkflowState: state,
			Detail:        errMsg,
		},
	}:
	default:
	}
}

// TodoState 是一个分发任务的状态枚举。
type TodoState string

const (
	TodoPending  TodoState = "pending"  // 待开始
	TodoRunning  TodoState = "running"  // 执行中
	TodoDone     TodoState = "done"     // 已完成
	TodoFailed   TodoState = "failed"   // 失败
	TodoSkipped  TodoState = "skipped"  // 跳过
)

// TodoItem 表示一个 workflow 节点 / 委派子任务的展示状态。
type TodoItem struct {
	ID         string
	Name       string    // 给用户看的名字（节点描述 / 任务摘要）
	BrainKind  string    // 委派给哪个大脑（code / browser / verifier / ...）
	State      TodoState
	StartedAt  time.Time
	FinishedAt time.Time
	ErrorMsg   string
}

type Activity struct {
	RunID       string
	Input       string
	StartedAt   time.Time
	Status      string
	CurrentTool string
	// PhaseStartedAt 当前 Status 阶段的开始时间，用于显示"X 阶段已 Ys"
	PhaseStartedAt time.Time
	// ToolCount 本 turn 已发起的工具调用数
	ToolCount int
	// TurnIndex 当前是第几轮 LLM 交互
	TurnIndex int
	// LastBrain 当前正在工作的专精大脑（central / code / browser ...），nil 时即中央
	LastBrain string
	// Todos 是当前正在执行的 workflow / delegate 任务列表（claude-code 风格的 todo 面板）
	Todos []TodoItem
	Content strings.Builder
	Events  []string
}

func (a *Activity) Start() {
	a.StartedAt = time.Now()
	a.Status = "思考中"
	a.PhaseStartedAt = a.StartedAt
	a.CurrentTool = ""
	a.ToolCount = 0
	a.TurnIndex = 1
	a.LastBrain = "central"
	a.Content.Reset()
	a.Events = nil
}

func (a *Activity) Stop() {
	a.StartedAt = time.Time{}
	a.Status = ""
	a.PhaseStartedAt = time.Time{}
	a.CurrentTool = ""
	a.ToolCount = 0
	a.TurnIndex = 0
	a.LastBrain = ""
	a.Todos = nil
	a.Content.Reset()
	a.Events = nil
}

// SetTodosFromWorkflow 用 workflow 节点列表初始化 todo 列表。
// 每次 submit_workflow 时调用，建立任务清单（全部 pending）。
func (a *Activity) SetTodosFromWorkflow(nodeNames map[string]string, brainKinds map[string]string) {
	if a == nil {
		return
	}
	a.Todos = a.Todos[:0]
	for id, name := range nodeNames {
		a.Todos = append(a.Todos, TodoItem{
			ID:        id,
			Name:      name,
			BrainKind: brainKinds[id],
			State:     TodoPending,
		})
	}
}

// UpdateTodoState 把指定 ID 的 todo 切换到新状态。
// reason 是失败时的简短原因（state==TodoFailed 才用）。
func (a *Activity) UpdateTodoState(id string, state TodoState, reason string) {
	if a == nil {
		return
	}
	for i := range a.Todos {
		if a.Todos[i].ID == id {
			a.Todos[i].State = state
			now := time.Now()
			switch state {
			case TodoRunning:
				a.Todos[i].StartedAt = now
			case TodoDone, TodoFailed, TodoSkipped:
				a.Todos[i].FinishedAt = now
				if state == TodoFailed {
					a.Todos[i].ErrorMsg = reason
				}
			}
			return
		}
	}
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
		a.setStatus("思考中")
	case ProgressContent:
		a.Content.WriteString(ev.Text)
		if strings.TrimSpace(a.Content.String()) != "" {
			a.setStatus("回答中")
		}
	case ProgressToolPlan:
		a.CurrentTool = ev.ToolName
		a.setStatus("准备 " + shortToolName(ev.ToolName))
		a.appendEvent(renderToolEvent("Plan", ev.ToolName, ev.Args))
	case ProgressToolStart:
		a.CurrentTool = ev.ToolName
		a.ToolCount++
		a.setStatus("执行 " + shortToolName(ev.ToolName))
		a.appendEvent(renderToolEvent("Run", ev.ToolName, ev.Args))
	case ProgressToolEnd:
		a.CurrentTool = ""
		if ev.OK {
			a.setStatus("处理结果")
			a.appendEvent(renderToolEvent("Done", ev.ToolName, ev.Detail))
		} else {
			a.setStatus("工具失败 " + shortToolName(ev.ToolName))
			a.appendEvent(renderToolEvent("Error", ev.ToolName, ev.Detail))
		}
	case ProgressFinished:
		a.CurrentTool = ""
		a.TurnIndex++
		a.setStatus("整理输出")
	case ProgressWorkflowInit:
		// Args 是 JSON: [{"id":"a","name":"...","brain":"code"},...]
		// append 模式：每次 init 事件添加节点（同 ID 已存在则跳过），
		// 这样 bridge 可以分节点连续 emit 多次，逐渐构建 todo 列表。
		var nodes []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Brain string `json:"brain"`
		}
		if err := json.Unmarshal([]byte(ev.Args), &nodes); err == nil {
			for _, n := range nodes {
				exists := false
				for _, td := range a.Todos {
					if td.ID == n.ID {
						exists = true
						break
					}
				}
				if !exists {
					a.Todos = append(a.Todos, TodoItem{
						ID:        n.ID,
						Name:      n.Name,
						BrainKind: n.Brain,
						State:     TodoPending,
					})
				}
			}
			a.setStatus("工作流已就绪")
		}
	case ProgressWorkflowNode:
		// ToolName=节点 ID，WorkflowState=新状态
		state := TodoState(ev.WorkflowState)
		a.UpdateTodoState(ev.ToolName, state, ev.Detail)
	}
	return true
}

// setStatus 在状态变化时同时重置 PhaseStartedAt，让 status line 能显示"该状态已经多久"。
func (a *Activity) setStatus(s string) {
	if a.Status != s {
		a.Status = s
		a.PhaseStartedAt = time.Now()
	}
}

// shortToolName 把 "central.shell_exec" 这种工具名截短为 "shell_exec"，便于状态行显示。
func shortToolName(full string) string {
	if i := strings.LastIndex(full, "."); i >= 0 {
		return full[i+1:]
	}
	return full
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

// ANSI 颜色：claude-code 风格的多色状态 + todo 面板。
const (
	ansiReset   = "\033[0m"
	ansiDim     = "\033[2m"     // 灰
	ansiCyan    = "\033[36m"    // 青色（思考中 / 当前活跃 spinner）
	ansiGreen   = "\033[32m"    // 绿（完成）
	ansiYellow  = "\033[33m"    // 黄（执行中 / 警告）
	ansiRed     = "\033[31m"    // 红（失败）
	ansiBlue    = "\033[34m"    // 蓝（pending / 待开始）
	ansiMagenta = "\033[35m"    // 紫（专精大脑工作）
	ansiBold    = "\033[1m"
)

func (a *Activity) RenderLines() []string {
	if a == nil || !a.Running() {
		return nil
	}

	var lines []string

	// 1. Todo 面板：claude-code 风格，每个任务一行带状态图标 + 颜色
	if len(a.Todos) > 0 {
		lines = append(lines, a.renderTodoLines()...)
	}

	// LLM 流式文本不在 spinner 区预览：StreamProgressEvent 已经直接 fmt.Print
	// 到 stdout，再放到 frame queue 里会导致每次 frame 重画都把累积文本重打
	// 一遍，造成"<token><完整历史>"反复堆积的渲染 bug。spinner 区只负责
	// 显示状态/工具事件/todo，LLM 文本走自己的流式通道。
	lines = append(lines, tailStrings(a.Events, 2)...)

	// 3. 最后一行：动态 status line（spinner + 动词 + 时间 + 工具计数）
	lines = append(lines, ansiCyan+a.StatusLine()+ansiReset)

	return lines
}

// renderTodoLines 渲染 todo 面板。每行格式：
//   ✓ 1. 初始化项目骨架        [code]
//   ⟳ 2. 写游戏引擎            [code] 12s
//   ○ 3. 写控制器              [code]
//   ✗ 4. 测试                  [verifier] error: xxx
func (a *Activity) renderTodoLines() []string {
	lines := make([]string, 0, len(a.Todos)+1)
	// 标题行：todo 面板的小标题 + 完成数 / 总数
	doneCount := 0
	for _, td := range a.Todos {
		if td.State == TodoDone || td.State == TodoSkipped {
			doneCount++
		}
	}
	header := fmt.Sprintf("%s任务 %d/%d%s", ansiBold, doneCount, len(a.Todos), ansiReset)
	lines = append(lines, header)

	for i, td := range a.Todos {
		var icon, color string
		switch td.State {
		case TodoDone:
			icon, color = "✓", ansiGreen
		case TodoRunning:
			icon, color = "⟳", ansiYellow
		case TodoFailed:
			icon, color = "✗", ansiRed
		case TodoSkipped:
			icon, color = "⊘", ansiDim
		default: // pending
			icon, color = "○", ansiBlue
		}

		// 主体：图标 + 序号 + 名称
		main := fmt.Sprintf("%s%s %d. %s%s", color, icon, i+1, td.Name, ansiReset)

		// 副信息：[brain] / 已用时长 / 错误
		var suffix string
		if td.BrainKind != "" {
			suffix = fmt.Sprintf(" %s[%s]%s", ansiMagenta, td.BrainKind, ansiReset)
		}
		switch td.State {
		case TodoRunning:
			if !td.StartedAt.IsZero() {
				suffix += fmt.Sprintf(" %s%s%s", ansiDim, formatElapsed(time.Since(td.StartedAt)), ansiReset)
			}
		case TodoFailed:
			if td.ErrorMsg != "" {
				short := td.ErrorMsg
				if len(short) > 60 {
					short = short[:60] + "…"
				}
				suffix += fmt.Sprintf(" %s%s%s", ansiRed, short, ansiReset)
			}
		case TodoDone:
			if !td.StartedAt.IsZero() && !td.FinishedAt.IsZero() {
				suffix += fmt.Sprintf(" %s%s%s", ansiDim, formatElapsed(td.FinishedAt.Sub(td.StartedAt)), ansiReset)
			}
		}

		lines = append(lines, main+suffix)
	}
	return lines
}

// spinnerFrames 是状态行最前面的旋转字符。每秒切一帧，让 LLM 长时间思考时有"在动"的视觉信号。
// 用 braille 8 帧风格（claude code 也是这种风格）。
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// StatusLine 返回类 claude-code 风格的一行状态条。
// 格式：<spinner> <动词> · <total elapsed>[·<phase elapsed>][·tools][·turn N][·brain X] · esc 取消
//
// 例：⠋ 思考中 · 12s · turn 1 · esc 取消
//     ⠹ 执行 shell_exec · 18s · 工具#1 5s · turn 2 · esc 取消
//     ⠧ 处理结果 · 24s · 工具#1 完成 · turn 2 · esc 取消
func (a *Activity) StatusLine() string {
	if a == nil || !a.Running() {
		return ""
	}
	totalElapsed := time.Since(a.StartedAt)

	// 选 spinner 帧：按"总秒数 mod len"轮转，每秒进一帧。
	frame := spinnerFrames[int(totalElapsed/time.Second)%len(spinnerFrames)]

	verb := a.Status
	if verb == "" {
		verb = "工作中"
	}

	parts := []string{}
	parts = append(parts, fmt.Sprintf("%c %s", frame, verb))
	parts = append(parts, formatElapsed(totalElapsed))

	// 当前阶段已经持续多久（如果跟总时间不一样，说明有过状态切换 → 单独显示）
	if !a.PhaseStartedAt.IsZero() {
		phaseElapsed := time.Since(a.PhaseStartedAt)
		// 阶段时间 < 总时间一半 才单独显示，否则没意义
		if phaseElapsed < totalElapsed-2*time.Second && phaseElapsed > 1*time.Second {
			parts = append(parts, "本阶段 "+formatElapsed(phaseElapsed))
		}
	}

	// 工具调用累计数
	if a.ToolCount > 0 {
		parts = append(parts, fmt.Sprintf("工具×%d", a.ToolCount))
	}

	// 多轮 turn
	if a.TurnIndex > 1 {
		parts = append(parts, fmt.Sprintf("第%d轮", a.TurnIndex))
	}

	// 当前正在哪个专精大脑（central 是默认，不显示）
	if a.LastBrain != "" && a.LastBrain != "central" {
		parts = append(parts, "在 "+a.LastBrain+" 大脑")
	}

	parts = append(parts, "esc 取消")

	return strings.Join(parts, " · ")
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
