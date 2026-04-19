package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/persistence"
)

// Task #15 — 每日对话总结 daemon。
//
// 订阅 task.state.completed / task.state.failed 事件(Task #19 EventBus),
// 按 UTC 日期累积计数。每跨零点触发一次 summarize:读当日 RunStore + 累积计数,
// 调 LLM 总结成一段 Markdown 写进 LearningStore.SaveDailySummary。
//
// 设计:
//   - 累积逻辑轻:只记数(brain_id → count, 失败数),不拉明细
//   - 摘要阶段才读 RunStore 拉 prompt/error 样本(限制条数)
//   - summarizer 为 nil 时仍然落结构化计数(summary_text 留空),不阻塞
//   - 不另起 goroutine 池,单个 ticker+subscriber 即可

// SummaryDaemon 是每日总结守护。复用 bus + runs + learning 三家已有设施。
type SummaryDaemon struct {
	bus           events.Subscriber
	runs          persistence.RunStore
	store         persistence.LearningStore
	summarizer    llm.Provider
	summaryModel  string
	logger        func(string, ...interface{})
	now           func() time.Time
	tickInterval  time.Duration

	mu       sync.Mutex
	today    string             // YYYY-MM-DD
	counts   map[string]int     // brain_id → count
	failed   int
	total    int
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// SummaryDaemonConfig 配置。summaryModel 空时用 claude-haiku-4-5-20251001。
// tickInterval 空时用 30min;测试可以调小。
type SummaryDaemonConfig struct {
	Bus          events.Subscriber
	Runs         persistence.RunStore
	Store        persistence.LearningStore
	Summarizer   llm.Provider
	SummaryModel string
	TickInterval time.Duration
	Logger       func(string, ...interface{})
	Now          func() time.Time
}

// NewSummaryDaemon 创建一个未启动的 daemon。
func NewSummaryDaemon(cfg SummaryDaemonConfig) *SummaryDaemon {
	if cfg.Logger == nil {
		cfg.Logger = func(f string, args ...interface{}) {
			fmt.Fprintf(os.Stderr, "[summary-daemon] "+f+"\n", args...)
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 30 * time.Minute
	}
	return &SummaryDaemon{
		bus:          cfg.Bus,
		runs:         cfg.Runs,
		store:        cfg.Store,
		summarizer:   cfg.Summarizer,
		summaryModel: cfg.SummaryModel,
		logger:       cfg.Logger,
		now:          cfg.Now,
		tickInterval: cfg.TickInterval,
		today:        cfg.Now().UTC().Format("2006-01-02"),
		counts:       map[string]int{},
	}
}

// Start 启动订阅 + ticker。重复调用 no-op。
func (d *SummaryDaemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.cancel != nil {
		d.mu.Unlock()
		return nil
	}
	if d.bus == nil || d.store == nil {
		d.mu.Unlock()
		return nil // 无 bus/store 视为禁用
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	ch, unsub := d.bus.Subscribe(runCtx, "")
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer unsub()
		ticker := time.NewTicker(d.tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				d.ingest(ev)
			case <-ticker.C:
				d.checkRollover(runCtx)
			}
		}
	}()
	return nil
}

// Stop 关闭订阅。不强制 flush(防止阻塞关机)。
func (d *SummaryDaemon) Stop() {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
}

// ingest 把 task.state.* 事件转成计数。
func (d *SummaryDaemon) ingest(ev events.Event) {
	if !strings.HasPrefix(ev.Type, "task.state.") {
		return
	}
	state := strings.TrimPrefix(ev.Type, "task.state.")
	// 只计终态
	switch state {
	case "completed", "failed", "canceled", "crashed":
	default:
		return
	}

	var data struct {
		BrainID string `json:"brain_id"`
	}
	_ = json.Unmarshal(ev.Data, &data)

	d.mu.Lock()
	defer d.mu.Unlock()
	today := d.now().UTC().Format("2006-01-02")
	if today != d.today {
		// 跨天了:先 rollover 旧日
		prev := d.snapshot()
		d.today = today
		d.counts = map[string]int{}
		d.failed = 0
		d.total = 0
		go d.writeSummary(context.Background(), prev) // fire-and-forget
	}

	d.total++
	if state == "failed" || state == "crashed" {
		d.failed++
	}
	if data.BrainID != "" {
		d.counts[data.BrainID]++
	}
}

// checkRollover 定期检查是否跨天(防止空 tick 日永不 summarize)。
func (d *SummaryDaemon) checkRollover(ctx context.Context) {
	d.mu.Lock()
	today := d.now().UTC().Format("2006-01-02")
	if today == d.today {
		d.mu.Unlock()
		return
	}
	prev := d.snapshot()
	d.today = today
	d.counts = map[string]int{}
	d.failed = 0
	d.total = 0
	d.mu.Unlock()
	d.writeSummary(ctx, prev)
}

// dailySnapshot 是 ingest / checkRollover 之间传递计数的快照。
type dailySnapshot struct {
	Date   string
	Counts map[string]int
	Total  int
	Failed int
}

// snapshot 要在 d.mu 持有时调用。
func (d *SummaryDaemon) snapshot() dailySnapshot {
	c := make(map[string]int, len(d.counts))
	for k, v := range d.counts {
		c[k] = v
	}
	return dailySnapshot{
		Date:   d.today,
		Counts: c,
		Total:  d.total,
		Failed: d.failed,
	}
}

// writeSummary 把一天的计数 + LLM 摘要写入 LearningStore。
func (d *SummaryDaemon) writeSummary(ctx context.Context, snap dailySnapshot) {
	if snap.Total == 0 {
		return
	}
	countsJSON, _ := json.Marshal(snap.Counts)
	summary := &persistence.DailySummary{
		Date:        snap.Date,
		BrainCounts: string(countsJSON),
		RunsTotal:   snap.Total,
		RunsFailed:  snap.Failed,
		UpdatedAt:   d.now().UTC(),
	}

	if d.summarizer != nil && d.runs != nil {
		if text, err := d.llmSummary(ctx, snap); err == nil {
			summary.SummaryText = text
		} else {
			d.logger("llm summary failed for %s: %v", snap.Date, err)
		}
	}

	if err := d.store.SaveDailySummary(ctx, summary); err != nil {
		d.logger("save summary %s: %v", snap.Date, err)
	}
}

// llmSummary 拉当日样本 runs 组 prompt 让 LLM 总结。
func (d *SummaryDaemon) llmSummary(ctx context.Context, snap dailySnapshot) (string, error) {
	// 只拉最近 20 条,避免 prompt 过大;精细化的样本选择交给后续迭代
	runs, err := d.runs.List(ctx, 20, "")
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("日期:%s\n总任务:%d(失败 %d)\n各大脑任务数:", snap.Date, snap.Total, snap.Failed))
	for k, v := range snap.Counts {
		sb.WriteString(fmt.Sprintf(" %s=%d", k, v))
	}
	sb.WriteString("\n\n最近任务样本(最多 20 条):\n")
	for i, r := range runs {
		if i >= 20 {
			break
		}
		prompt := r.Prompt
		if len(prompt) > 200 {
			prompt = prompt[:200] + "…"
		}
		sb.WriteString(fmt.Sprintf("- [%s/%s] %s", r.BrainID, r.Status, prompt))
		if r.Error != "" {
			errMsg := r.Error
			if len(errMsg) > 80 {
				errMsg = errMsg[:80] + "…"
			}
			sb.WriteString("  err=" + errMsg)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n请用中文给出:1) 今日整体趋势 2-3 句;2) 值得关注的失败模式;3) 建议。Markdown,总长 ≤ 400 字。")

	model := d.summaryModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	resp, err := d.summarizer.Complete(ctx, &llm.ChatRequest{
		Model:     model,
		MaxTokens: 1024,
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: sb.String()}},
		}},
	})
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Content) == 0 {
		return "", nil
	}
	return resp.Content[0].Text, nil
}

// ForceFlush 立即把当前累积作为"今天"落盘,主要给关机/测试用。
func (d *SummaryDaemon) ForceFlush(ctx context.Context) {
	d.mu.Lock()
	snap := d.snapshot()
	d.mu.Unlock()
	d.writeSummary(ctx, snap)
}
