package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

// Task #16/#13 — 人类接管模式 + 真人 DOM 操作录制(P3.3)。
//
// 设计:任意 brain(典型:browser 遇 CAPTCHA / 登录过期)调用 human.request_takeover
// 工具 → Coordinator 把 TaskExecution 置 Paused → 发事件 → Agent 循环阻塞等待。
// 人类通过 WebUI / CLI 调 /v1/executions/{id}/resume|abort,Coordinator 唤醒 Agent。
//
// 录制分两层:
//   1) takeover 标记  —— 调用开始/结束在 SequenceRecorder 里各 push 一条,
//      给学习层一个"这一段有真人介入"的分界线(已有逻辑)。
//   2) 真人 DOM 操作 —— 在 takeover 期间启动 humanActionRecorder,订阅
//      cdp.HumanEventSubscriber 拿 click / input / change / submit 事件,
//      转 RecordedAction 追加进 SequenceRecorder(打 _human=true 标志),
//      同时把序列存到 HumanDemoSink(默认 HumanDemoSequence 表)供 ops
//      审批后再变成学习素材。不混进 source=learned 模式。

// HumanTakeoverOutcome 是人类处理后的结果枚举。
type HumanTakeoverOutcome string

const (
	HumanOutcomeResumed HumanTakeoverOutcome = "resumed"
	HumanOutcomeAborted HumanTakeoverOutcome = "aborted"
)

// HumanTakeoverCoordinator 是主机侧(cmd_serve)注入的协调器,负责:
//   - 请求接管时置 TaskExecution 为 Paused
//   - 发事件 "task.human.requested" 到 EventBus
//   - 阻塞直到人类 resume/abort,返回对应的 outcome 和可选 note
//
// 空实现等价于"没接 coordinator,工具直接返回 abort",让学习和离线测试
// 场景下不至于死等。
type HumanTakeoverCoordinator interface {
	RequestTakeover(ctx context.Context, req HumanTakeoverRequest) HumanTakeoverResponse
}

// HumanTakeoverRequest 描述 Agent 发起接管时携带的信息。
type HumanTakeoverRequest struct {
	RunID      string `json:"run_id"`
	BrainKind  string `json:"brain_kind"`
	Reason     string `json:"reason"`              // 典型:"captcha_detected" / "session_expired"
	Guidance   string `json:"guidance,omitempty"`  // Agent 给人的建议文本
	Screenshot string `json:"screenshot,omitempty"`// 可选 base64 PNG
	URL        string `json:"url,omitempty"`
	TimeoutSec int    `json:"timeout_sec,omitempty"` // 0 = 无超时
}

// HumanTakeoverResponse 是 coordinator 告诉工具的结果。
type HumanTakeoverResponse struct {
	Outcome HumanTakeoverOutcome `json:"outcome"`
	Note    string               `json:"note,omitempty"` // 人类留言,回传给 Agent
}

var (
	takeoverMu   sync.RWMutex
	takeoverImpl HumanTakeoverCoordinator
)

// SetHumanTakeoverCoordinator 由主机侧注入实现。多次调用取最后一次。
func SetHumanTakeoverCoordinator(c HumanTakeoverCoordinator) {
	takeoverMu.Lock()
	defer takeoverMu.Unlock()
	takeoverImpl = c
}

func currentTakeover() HumanTakeoverCoordinator {
	takeoverMu.RLock()
	defer takeoverMu.RUnlock()
	return takeoverImpl
}

// ---------------------------------------------------------------------------
// P3.3 — 真人 DOM 录制单例 + sink
// ---------------------------------------------------------------------------

// HumanDemoSink 把 takeover 期间录下的序列存到 HumanDemoSequence 表。
// 生产实现是 kernel.LearningEngine(调 store.SaveHumanDemoSequence),
// 测试注入 mock。和 InteractionSink 对齐的风格,不另造 sink 体系。
type HumanDemoSink interface {
	SaveHumanDemoSequence(ctx context.Context, seq *persistence.HumanDemoSequence) error
}

var (
	humanDemoMu   sync.RWMutex
	humanDemoSink HumanDemoSink
)

// SetHumanDemoSink 在进程启动时被 kernel/cmd 注入。多次调用取最后一次。
// 传 nil 即清空(测试/关闭流程使用)。
func SetHumanDemoSink(s HumanDemoSink) {
	humanDemoMu.Lock()
	defer humanDemoMu.Unlock()
	humanDemoSink = s
}

func currentHumanDemoSink() HumanDemoSink {
	humanDemoMu.RLock()
	defer humanDemoMu.RUnlock()
	return humanDemoSink
}

// HumanEventSourceFactory 让 takeover 工具按需创建事件源。生产里 cmd/brain
// 启动时注入一个返回 cdp.CDPEventSource 的工厂;测试注入返回 MemoryEventSource
// 的工厂,不起浏览器。传 nil 工厂则 takeover 只录标记,不订阅 DOM。
type HumanEventSourceFactory func(ctx context.Context) (cdp.EventSource, error)

var (
	eventSourceMu      sync.RWMutex
	eventSourceFactory HumanEventSourceFactory
)

// SetHumanEventSourceFactory 注入事件源工厂。nil 表示禁用 DOM 录制。
func SetHumanEventSourceFactory(f HumanEventSourceFactory) {
	eventSourceMu.Lock()
	defer eventSourceMu.Unlock()
	eventSourceFactory = f
}

func currentEventSourceFactory() HumanEventSourceFactory {
	eventSourceMu.RLock()
	defer eventSourceMu.RUnlock()
	return eventSourceFactory
}

// inputMergeWindow 是"相同元素上连续输入合并"的时间窗。人类逐字敲键盘时
// 浏览器会连续抛 input 事件,我们把 500ms 内同 element 的 input 压成一条
// Type(取最后一次 value)。阈值参照 Playwright codegen 的默认合并窗。
const inputMergeWindow = 500 * time.Millisecond

// humanActionRecorder per-takeover 录制器。收到事件后:
//   - 过滤低信息量(scroll/hover 根本没订阅,这里再兜底 Kind 白名单)
//   - 输入聚合(500ms 内同元素的 input → 一条 Type)
//   - 转 RecordedAction 追加进 SequenceRecorder(带 _human=true)
//   - 同时留一份到 demoActions,takeover 结束时写 HumanDemoSequence
type humanActionRecorder struct {
	mu          sync.Mutex
	ctx         context.Context
	source      cdp.EventSource
	events      <-chan cdp.HumanEvent
	done        chan struct{}
	runID       string
	brainKind   string
	goal        string
	site        string
	lastURL     string
	// 输入聚合状态:按 brainID / css 识别同一元素。
	lastInputKey  string
	lastInputTime time.Time
	demoActions   []RecordedAction
}

// startHumanRecorder 在 takeover 开始时调。返回 nil 表示工厂未配置/启动
// 失败(不致命,调用方只会少录制 DOM)。
func startHumanRecorder(ctx context.Context, runID, brainKind, goal, url string) *humanActionRecorder {
	factory := currentEventSourceFactory()
	if factory == nil {
		return nil
	}
	src, err := factory(ctx)
	if err != nil || src == nil {
		return nil
	}
	ch, err := src.Start(ctx)
	if err != nil {
		_ = src.Stop()
		return nil
	}
	r := &humanActionRecorder{
		ctx:       ctx,
		source:    src,
		events:    ch,
		done:      make(chan struct{}),
		runID:     runID,
		brainKind: brainKind,
		goal:      goal,
		lastURL:   url,
	}
	go r.run()
	return r
}

// run 消费事件直到 channel 关闭。
func (r *humanActionRecorder) run() {
	defer close(r.done)
	for ev := range r.events {
		r.handle(ev)
	}
}

// stop 关事件源并 join goroutine。demo 写库在调用方做,本函数不落盘。
func (r *humanActionRecorder) stop() {
	if r == nil {
		return
	}
	_ = r.source.Stop()
	<-r.done
}

// snapshotDemo 拷一份已合并的 actions 给调用方写库。
func (r *humanActionRecorder) snapshotDemo() []RecordedAction {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecordedAction, len(r.demoActions))
	copy(out, r.demoActions)
	return out
}

// handle 把一个 HumanEvent 转 RecordedAction 入队。
func (r *humanActionRecorder) handle(ev cdp.HumanEvent) {
	// 白名单:只接受有意义的四类。其它(在 hook 脚本里其实就没订阅)兜底丢弃。
	switch ev.Kind {
	case cdp.HumanEventClick, cdp.HumanEventInput, cdp.HumanEventChange, cdp.HumanEventSubmit:
	default:
		return
	}

	if ev.URL != "" {
		r.lastURL = ev.URL
		if site := siteFromURL(ev.URL); site != "" {
			r.site = site
		}
	}

	// input 聚合:500ms 内同元素连续 input 只保留最后一条(覆盖上一条 value)。
	if ev.Kind == cdp.HumanEventInput {
		if r.tryMergeInput(ev) {
			return
		}
	} else {
		// 非 input 事件会中止当前的输入聚合窗口 —— 下次 input 事件起再开新窗口。
		r.mu.Lock()
		r.lastInputKey = ""
		r.mu.Unlock()
	}

	act := r.toAction(ev)
	r.appendBoth(act)
}

// tryMergeInput 判断是否应该把本条 input 合并进上一条。返回 true 表示
// 已合并(调用方不再 append),false 表示需要新建一条。
func (r *humanActionRecorder) tryMergeInput(ev cdp.HumanEvent) bool {
	key := inputKey(ev)
	now := ev.Timestamp
	if now.IsZero() {
		now = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.lastInputKey == key && now.Sub(r.lastInputTime) <= inputMergeWindow &&
		len(r.demoActions) > 0 {
		// 覆盖上一条 Type 的 text/value
		last := &r.demoActions[len(r.demoActions)-1]
		if last.Params == nil {
			last.Params = map[string]interface{}{}
		}
		last.Params["text"] = ev.Value
		last.Params["ts"] = now.Format(time.RFC3339Nano)
		r.lastInputTime = now
		return true
	}
	// 新窗口
	r.lastInputKey = key
	r.lastInputTime = now
	return false
}

// inputKey 识别"同一个输入元素":优先 brain_id(稳定),其次 css,最后
// 用 tag+name 兜底。空串表示无法识别 —— 每个事件都当新窗口。
func inputKey(ev cdp.HumanEvent) string {
	if ev.BrainID > 0 {
		return fmt.Sprintf("bid:%d", ev.BrainID)
	}
	if ev.CSS != "" {
		return "css:" + ev.CSS
	}
	if ev.Tag != "" || ev.Name != "" {
		return "t:" + ev.Tag + "/n:" + ev.Name
	}
	return ""
}

// toAction 把一条事件转换成 RecordedAction。Tool 命名对齐 browser.*
// 工具,下游 ui_pattern_learn 直接能匹配:click → browser.click,
// input / change → browser.type,submit → browser.click(on submit button)。
func (r *humanActionRecorder) toAction(ev cdp.HumanEvent) RecordedAction {
	act := RecordedAction{
		ElementRole: ev.Role,
		ElementName: ev.Name,
		ElementType: ev.Type,
	}
	params := map[string]interface{}{
		"_human":   true, // 下游聚类用这个旗标排除或专门学习
		"kind":     string(ev.Kind),
		"ts":       ev.Timestamp.Format(time.RFC3339Nano),
	}
	if ev.BrainID > 0 {
		params["brain_id"] = ev.BrainID
		params["id"] = ev.BrainID // 对齐 browser.click 参数名
	}
	if ev.Tag != "" {
		params["tag"] = ev.Tag
	}
	if ev.CSS != "" {
		params["css"] = ev.CSS
	}
	if ev.URL != "" {
		params["url"] = ev.URL
	}
	switch ev.Kind {
	case cdp.HumanEventClick:
		act.Tool = "browser.click"
	case cdp.HumanEventInput, cdp.HumanEventChange:
		act.Tool = "browser.type"
		if ev.Value != "" {
			params["text"] = ev.Value
		}
	case cdp.HumanEventSubmit:
		act.Tool = "browser.click" // submit 按钮点击
		params["submit"] = true
	}
	act.Params = params
	act.Result = "human"
	return act
}

// appendBoth 同时把 action 写进 SequenceRecorder(学习管道)和
// demoActions(写 HumanDemoSequence)。
func (r *humanActionRecorder) appendBoth(act RecordedAction) {
	// demo 副本
	r.mu.Lock()
	r.demoActions = append(r.demoActions, act)
	r.mu.Unlock()

	// SequenceRecorder(ctx 复用 startHumanRecorder 传入的 ctx)
	recorderMu.Lock()
	rec := ctxRecorders[r.ctx]
	recorderMu.Unlock()
	if rec != nil {
		rec.append(act)
	}
}

// siteFromURL 是 extractSiteFromParams 的简化版:只取 scheme+host。
// 写这里而不是复用 extractSiteFromParams,是因为后者吃 params map,
// 调用方还要包一层。
func siteFromURL(raw string) string {
	s, _ := extractSiteFromParams(map[string]interface{}{"url": raw})
	return s
}

// persistDemoSequence 把 takeover 期间收集的 actions 存到 HumanDemoSink。
// Approved 默认 false,ops 审批后改 true。空序列不落盘。
func persistDemoSequence(ctx context.Context, r *humanActionRecorder) {
	if r == nil {
		return
	}
	sink := currentHumanDemoSink()
	actions := r.snapshotDemo()
	if sink == nil || len(actions) == 0 {
		return
	}
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return
	}
	seq := &persistence.HumanDemoSequence{
		RunID:      r.runID,
		BrainKind:  r.brainKind,
		Goal:       r.goal,
		Site:       r.site,
		URL:        r.lastURL,
		Actions:    actionsJSON,
		Approved:   false,
		RecordedAt: time.Now().UTC(),
	}
	// 错误不 fail 工具 —— takeover 已经走完,写库失败只是学习素材丢一条。
	_ = sink.SaveHumanDemoSequence(ctx, seq)
}

// ---------------------------------------------------------------------------
// Tool: human.request_takeover
// ---------------------------------------------------------------------------

// HumanRequestTakeoverTool 暴露给 LLM。
type HumanRequestTakeoverTool struct{}

// NewHumanRequestTakeoverTool 构造器,供 registry 统一注册。
func NewHumanRequestTakeoverTool() *HumanRequestTakeoverTool { return &HumanRequestTakeoverTool{} }

func (t *HumanRequestTakeoverTool) Name() string { return "human.request_takeover" }
func (t *HumanRequestTakeoverTool) Risk() Risk   { return RiskMedium }

func (t *HumanRequestTakeoverTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Hand control to a human when you hit a blocker that can't be automated:
CAPTCHA, phone verification, payment confirmation, or repeated low-confidence
turns. The agent loop pauses until the human responds via WebUI / CLI.

When to use:
  - browser.check_anomaly reported severity=blocker (CAPTCHA, human verification)
  - You've tried 3+ different strategies on the same page with no progress
  - About to perform an irreversible action with risk_level=destructive AND
    you don't have explicit approval in the task

When NOT to use:
  - Transient failures — let retry-wrapped tools handle them
  - Low-confidence reasoning where re-reading the page would help
  - Before the first snapshot/understand — you don't have enough info to ask`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "required": ["reason"],
  "properties": {
    "reason":      { "type": "string", "description": "Short machine-readable cause: captcha / session_expired / payment / low_confidence / other" },
    "guidance":    { "type": "string", "description": "Free-text note telling the human what to do" },
    "screenshot":  { "type": "string", "description": "Optional base64-encoded PNG (from browser.screenshot)" },
    "url":         { "type": "string" },
    "timeout_sec": { "type": "integer", "description": "Wait at most N seconds before auto-aborting (default: unlimited)" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "outcome": { "type": "string", "description": "resumed | aborted | no_coordinator" },
    "note":    { "type": "string", "description": "Human-provided note, may be empty" }
  }
}`),
		Brain: "", // 所有 brain 可用
		Concurrency: &ToolConcurrencySpec{
			Capability:          "human.interactive",
			ResourceKeyTemplate: "human:session",
			AccessMode:          "exclusive",
			Scope:               "run",
			ApprovalClass:       "safe",
		},
	}
}

type humanTakeoverInput struct {
	Reason     string `json:"reason"`
	Guidance   string `json:"guidance"`
	Screenshot string `json:"screenshot"`
	URL        string `json:"url"`
	TimeoutSec int    `json:"timeout_sec"`
}

func (t *HumanRequestTakeoverTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var in humanTakeoverInput
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if in.Reason == "" {
		return errResult("reason is required"), nil
	}

	// 追加录制标记(开始)
	recordTakeoverMarker(ctx, "start", in.Reason)

	coord := currentTakeover()
	if coord == nil {
		// 无主机协调器:返回 no_coordinator,让 Agent 自行决策
		recordTakeoverMarker(ctx, "no_coordinator", in.Reason)
		return okResult(map[string]interface{}{
			"outcome": "no_coordinator",
			"note":    "no human coordinator configured; cannot hand off",
		}), nil
	}

	runID, brainKind := currentTakeoverRecorderContext(ctx)
	goal := currentTakeoverGoal(ctx)
	req := HumanTakeoverRequest{
		RunID:      runID,
		BrainKind:  brainKind,
		Reason:     in.Reason,
		Guidance:   in.Guidance,
		Screenshot: in.Screenshot,
		URL:        in.URL,
		TimeoutSec: in.TimeoutSec,
	}

	// 启动 DOM 事件录制(工厂缺失 → 返回 nil,只记标记)。
	humanRec := startHumanRecorder(ctx, runID, brainKind, goal, in.URL)

	// 阻塞等待,尊重 ctx 取消
	resp := coord.RequestTakeover(ctx, req)

	// 停止录制并落盘(best-effort,不影响 Agent 拿到 outcome)。
	if humanRec != nil {
		humanRec.stop()
		persistDemoSequence(ctx, humanRec)
	}

	// 追加录制标记(结束)
	recordTakeoverMarker(ctx, string(resp.Outcome), resp.Note)

	return okResult(map[string]interface{}{
		"outcome": string(resp.Outcome),
		"note":    resp.Note,
	}), nil
}

// recordTakeoverMarker 给当前 run 的录制追加一条 human.takeover 标记,供后续
// 聚类识别"这一段中间插入了人类操作",避免把人工动作当自动化模式学。
// 复用 sequence_recorder.go 里的 ctxRecorders 表,不引入新路径。
func recordTakeoverMarker(ctx context.Context, phase, note string) {
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return
	}
	params := map[string]interface{}{
		"phase": phase,
		"ts":    time.Now().UTC().Format(time.RFC3339),
	}
	if note != "" {
		params["note"] = note
	}
	rec.append(RecordedAction{
		Tool:   "human.takeover",
		Params: params,
		Result: phase,
	})
}

// currentTakeoverRecorderContext 拿 ctx 绑定 recorder 的 runID/brainKind,
// 方便给 coordinator。无 recorder 返回空串。
func currentTakeoverRecorderContext(ctx context.Context) (runID, brainKind string) {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	if rec := ctxRecorders[ctx]; rec != nil {
		return rec.runID, rec.brainKind
	}
	return "", ""
}

// currentTakeoverGoal 拿 ctx 绑定 recorder 的 goal。无 recorder 返回空串。
func currentTakeoverGoal(ctx context.Context) string {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	if rec := ctxRecorders[ctx]; rec != nil {
		return rec.goal
	}
	return ""
}

// ---------------------------------------------------------------------------
// NoopTakeoverCoordinator — 测试/本地调用用
// ---------------------------------------------------------------------------

// NoopTakeoverCoordinator 总是返回 aborted,方便测试不需要真人。
type NoopTakeoverCoordinator struct{}

func (NoopTakeoverCoordinator) RequestTakeover(_ context.Context, _ HumanTakeoverRequest) HumanTakeoverResponse {
	return HumanTakeoverResponse{Outcome: HumanOutcomeAborted, Note: "no interactive session"}
}

// 辅助:tool 包外需要格式化信息时
func (r HumanTakeoverRequest) String() string {
	return fmt.Sprintf("takeover(run=%s brain=%s reason=%s)", r.RunID, r.BrainKind, r.Reason)
}
