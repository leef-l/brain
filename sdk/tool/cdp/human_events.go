package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// human_events.go — Task #13 P3.3 真人接管期间的 DOM 操作录制。
//
// 目标:在 headless/headful 浏览器里把"真人点击/输入/提交"转成 Go 结构体事件流,
// 供 sdk/tool/human_takeover.go 写入 SequenceRecorder + HumanDemoSequence。
//
// 实现思路(为什么不直接订阅 Input.dispatchKeyEvent/MouseEvent):
//   - `Input.dispatchKeyEvent` / `Input.dispatchMouseEvent` 是"我们主动发给浏览器的 RPC",
//     不是浏览器上报的事件 —— CDP 里没有"被动 sniff 真人键鼠"的专门 API。
//   - 真人操作的可靠捕获方式是在页面注入 JS hook:给 document 挂
//     click / input / change / submit 监听,通过 `Runtime.addBinding` 把 payload
//     打回 Go 侧(`Runtime.bindingCalled` 事件订阅)。
//   - 这条路径正是 DevTools / Playwright "record" 功能使用的路径,跨域 iframe
//     不覆盖是已知限制,对 P3.3 的"单页真人演示"够用。
//
// EventSource 抽象层让测试可以不起真浏览器 —— 内存 channel 直接喂事件。
// CDPEventSource 是生产实现,基于 BrowserSession.Client() + Exec。

// HumanEventKind 枚举 hook 回传的事件类别。
type HumanEventKind string

const (
	HumanEventClick  HumanEventKind = "click"
	HumanEventInput  HumanEventKind = "input"
	HumanEventChange HumanEventKind = "change"
	HumanEventSubmit HumanEventKind = "submit"
)

// HumanEvent 是一条真人 DOM 操作。字段尽量对齐 sdk/tool.RecordedAction
// 的上游结构(ElementRole / ElementName / 值字段),让 recorder 转换简单。
type HumanEvent struct {
	Kind        HumanEventKind `json:"kind"`
	BrainID     int            `json:"brain_id,omitempty"`   // data-brain-id(snapshot 分配),0 表示未覆盖
	Tag         string         `json:"tag,omitempty"`        // input / button / a / ...
	Type        string         `json:"type,omitempty"`       // input type / button type
	Role        string         `json:"role,omitempty"`
	Name        string         `json:"name,omitempty"`       // aria-label / innerText 截短
	Value       string         `json:"value,omitempty"`      // input 值 / change 后 value
	CSS         string         `json:"css,omitempty"`        // DOM path 粗略 selector(稳定性中)
	URL         string         `json:"url,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
}

// EventSource 是 HumanEventSubscriber 的下游抽象。Start 返回一个 Events
// channel,Subscriber 持续读;Stop 关闭 channel 释放资源。
//
// 生产实现 CDPEventSource:注入 JS hook + 监听 Runtime.bindingCalled。
// 测试实现 MemoryEventSource:直接把 HumanEvent push 进 channel。
type EventSource interface {
	Start(ctx context.Context) (<-chan HumanEvent, error)
	Stop() error
}

// MemoryEventSource 测试用:单测直接往 Events 里 push 事件,验证
// Subscriber 的过滤/聚合/转换逻辑,不起浏览器。
type MemoryEventSource struct {
	ch      chan HumanEvent
	mu      sync.Mutex
	started bool
	stopped bool
}

// NewMemoryEventSource 构造一个内存事件源。buffer 建议 ≥ 预期事件数,
// 单测里我们会先把所有事件 push 完再关。
func NewMemoryEventSource(buffer int) *MemoryEventSource {
	if buffer <= 0 {
		buffer = 16
	}
	return &MemoryEventSource{ch: make(chan HumanEvent, buffer)}
}

// Push 往事件源塞一条事件。已 Stop 则丢弃,不阻塞测试。
func (m *MemoryEventSource) Push(ev HumanEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	select {
	case m.ch <- ev:
	default:
		// 缓冲满丢弃(测试场景不应发生;生产 CDPEventSource 也会丢,避免
		// 把真人操作阻塞在 hook 里)。
	}
}

// Start 返回已建好的 channel。多次 Start 只有第一次生效。
func (m *MemoryEventSource) Start(_ context.Context) (<-chan HumanEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, fmt.Errorf("event source already stopped")
	}
	m.started = true
	return m.ch, nil
}

// Stop 关闭 channel。幂等。
func (m *MemoryEventSource) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}
	m.stopped = true
	close(m.ch)
	return nil
}

// ---------------------------------------------------------------------------
// CDPEventSource — 生产实现,注入 JS hook + 订阅 Runtime.bindingCalled
// ---------------------------------------------------------------------------

// humanHookBindingName 是注入脚本使用的 Runtime.addBinding 名。
// 前缀 "__brain" 降低和业务站脚本冲突的风险。
const humanHookBindingName = "__brainHumanEvent"

// humanHookScript 是在每个新文档上自动运行的 hook 脚本。它挂
// document 级别的 click / input / change / submit 监听,把关键字段
// JSON 打包后通过 window.__brainHumanEvent(payload) 打回 Go 侧。
//
// 设计取舍:
//   - 只覆盖 document 级,iframe 内事件是已知限制(和 Playwright 相同策略)。
//   - 不订阅 mousemove / scroll / hover —— 对学习无用、噪声大。
//   - 对 input 的聚合在 Go 侧做(humanActionRecorder),hook 里每次 keystroke
//     都上报是可接受的(500ms 合并窗口会把连续输入压缩成一条)。
const humanHookScript = `
(() => {
  if (window.__brainHumanHookInstalled) return;
  window.__brainHumanHookInstalled = true;
  const emit = (ev) => {
    if (!window.__brainHumanEvent) return;
    try { window.__brainHumanEvent(JSON.stringify(ev)); } catch (_) {}
  };
  const describe = (el) => {
    if (!el || !(el instanceof Element)) return {};
    const ariaLabel = el.getAttribute && el.getAttribute("aria-label");
    const name = (el.innerText || el.value || ariaLabel || "").toString().slice(0, 120);
    return {
      brain_id: parseInt(el.getAttribute && el.getAttribute("data-brain-id")) || 0,
      tag: (el.tagName || "").toLowerCase(),
      type: (el.getAttribute && el.getAttribute("type")) || "",
      role: (el.getAttribute && el.getAttribute("role")) || "",
      name: name.trim(),
      css: el.id ? "#" + el.id : (el.tagName || "").toLowerCase(),
      url: location && location.href,
    };
  };
  document.addEventListener("click",  (e) => emit({ kind: "click",  ...describe(e.target) }), true);
  document.addEventListener("input",  (e) => {
    const d = describe(e.target);
    d.value = (e.target && e.target.value != null) ? String(e.target.value).slice(0, 240) : "";
    emit({ kind: "input", ...d });
  }, true);
  document.addEventListener("change", (e) => {
    const d = describe(e.target);
    d.value = (e.target && e.target.value != null) ? String(e.target.value).slice(0, 240) : "";
    emit({ kind: "change", ...d });
  }, true);
  document.addEventListener("submit", (e) => emit({ kind: "submit", ...describe(e.target) }), true);
})();
`

// CDPEventSource 基于 BrowserSession 的 CDP 连接订阅真人事件。
//
// 流程:
//   Start:
//     1) Runtime.addBinding({name: __brainHumanEvent}) 注入 binding
//     2) Page.addScriptToEvaluateOnNewDocument 注入 hook 脚本(自动跨导航生效)
//     3) Runtime.evaluate 把 hook 脚本在当前页也跑一次(首次订阅不丢失)
//     4) client.On("Runtime.bindingCalled", fn) 把 payload 解成 HumanEvent
//   Stop:
//     1) Page.removeScriptToEvaluateOnNewDocument(scriptId)(存在就 best-effort 调)
//     2) 关闭 Events channel
//
// 注意:client.On 没有 off,但我们用 stopped 标志位 + close(ch) 保证停止后
// listener 回调里不会往关闭的 channel 写,避免 panic。
type CDPEventSource struct {
	sess    sessionLike
	ch      chan HumanEvent
	mu      sync.Mutex
	stopped bool
	scriptID string // Page.addScriptToEvaluateOnNewDocument 返回的 id
}

// sessionLike 是 CDPEventSource 依赖的最小接口,方便单测 mock。
// 生产里 *BrowserSession 自动满足(Exec + Client 方法)。
type sessionLike interface {
	Exec(ctx context.Context, method string, params interface{}, result interface{}) error
	Client() *Client
}

// NewCDPEventSource 绑定一个 BrowserSession。调用方管 session 生命周期。
func NewCDPEventSource(sess sessionLike) *CDPEventSource {
	return &CDPEventSource{
		sess: sess,
		ch:   make(chan HumanEvent, 256),
	}
}

// Start 注入 binding + hook 脚本,并监听 bindingCalled。返回事件 channel。
func (s *CDPEventSource) Start(ctx context.Context) (<-chan HumanEvent, error) {
	if s.sess == nil {
		return nil, fmt.Errorf("nil session")
	}
	// 1) addBinding
	if err := s.sess.Exec(ctx, "Runtime.addBinding",
		map[string]interface{}{"name": humanHookBindingName}, nil); err != nil {
		return nil, fmt.Errorf("addBinding: %w", err)
	}
	// 2) addScriptToEvaluateOnNewDocument 跨导航生效
	var addOut struct {
		Identifier string `json:"identifier"`
	}
	if err := s.sess.Exec(ctx, "Page.addScriptToEvaluateOnNewDocument",
		map[string]interface{}{"source": humanHookScript}, &addOut); err != nil {
		return nil, fmt.Errorf("addScriptToEvaluateOnNewDocument: %w", err)
	}
	s.scriptID = addOut.Identifier
	// 3) 当前页也跑一次
	_ = s.sess.Exec(ctx, "Runtime.evaluate",
		map[string]interface{}{"expression": humanHookScript}, nil)
	// 4) 订阅 bindingCalled
	client := s.sess.Client()
	if client == nil {
		return nil, fmt.Errorf("nil CDP client")
	}
	client.On("Runtime.bindingCalled", func(raw json.RawMessage) {
		var p struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return
		}
		if p.Name != humanHookBindingName || p.Payload == "" {
			return
		}
		var ev HumanEvent
		if err := json.Unmarshal([]byte(p.Payload), &ev); err != nil {
			return
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		// 非阻塞 send —— buffer 满时丢弃,避免 hook 回调阻塞 CDP 读循环。
		select {
		case s.ch <- ev:
		default:
		}
	})
	return s.ch, nil
}

// Stop 停止订阅并关闭 channel。best-effort:removeScript 失败不报错。
func (s *CDPEventSource) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	scriptID := s.scriptID
	s.mu.Unlock()

	if scriptID != "" && s.sess != nil {
		_ = s.sess.Exec(context.Background(),
			"Page.removeScriptToEvaluateOnNewDocument",
			map[string]interface{}{"identifier": scriptID}, nil)
	}
	close(s.ch)
	return nil
}
