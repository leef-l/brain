package tool

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// Task #13 — 任务完成即学习:把 browser 交互序列通过 brain-v3 的 LearningStore
// 写入 SQLite,供 sdk/tool/ui_pattern_learn.go 聚类成 UIPattern。
//
// 不自造文件格式、不绕开 kernel 学习体系。sink 由 kernel 在启动时注入,见
// sdk/kernel/learning.go → LearningEngine.RecordInteractionSequence。
//
// 生命周期:
//   - sidecar loop 启动时 BindRecorder(ctx, runID, goal)
//   - anomalyInjectingTool 每次 Execute 后 recordInteractionForLearning
//   - sidecar loop 结束时 FinishRecorder(ctx, outcome) — 把序列交给 sink 持久化

// InteractionSink 是 SequenceRecorder 的持久化后端。kernel/LearningEngine
// 实现此接口;测试/独立调用可注入 mock。
type InteractionSink interface {
	RecordInteractionSequence(ctx context.Context, seq *persistence.InteractionSequence) error
}

var (
	sinkMu      sync.RWMutex
	globalSink  InteractionSink
)

// SetInteractionSink 在进程启动时被 kernel/sidecar 注入。多次调用取最后一次。
func SetInteractionSink(s InteractionSink) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	globalSink = s
}

// currentSink 给 FinishRecorder 取当前后端。
func currentSink() InteractionSink {
	sinkMu.RLock()
	defer sinkMu.RUnlock()
	return globalSink
}

// OutcomeSink 把一次工具执行结果回写给 AdaptiveToolPolicy,实现学习闭环
// (M6):anomalyInjectingTool 每次跑完就喂给策略层,让成功率低的工具按
// AdaptivePolicy.Evaluate 的规则自动降权 / 临时禁用。
//
// 进程级单例,参照 InteractionSink 的注入风格。sdk/toolpolicy.AdaptiveToolPolicy
// 已实现此接口(RecordOutcome 签名匹配),cmd/brain 启动时直接 Set。
type OutcomeSink interface {
	RecordOutcome(toolName string, taskType string, success bool)
}

var (
	outcomeMu   sync.RWMutex
	globalOutcome OutcomeSink
)

// SetOutcomeSink 在进程启动时被 kernel/cmd 注入。多次调用取最后一次。
// 传 nil 即清空(测试/关闭流程使用)。
func SetOutcomeSink(s OutcomeSink) {
	outcomeMu.Lock()
	defer outcomeMu.Unlock()
	globalOutcome = s
}

func currentOutcomeSink() OutcomeSink {
	outcomeMu.RLock()
	defer outcomeMu.RUnlock()
	return globalOutcome
}

// deriveTaskTypeFromCtx 给 OutcomeSink 提供相对稳定的 taskType key。
// 优先 brainKind(基数小、稳定),其次 goal 的首词,最后空字符串让 sink
// 自己归到 "_default"。
func deriveTaskTypeFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return ""
	}
	rec.mu.Lock()
	kind, goal := rec.brainKind, rec.goal
	rec.mu.Unlock()
	if kind != "" {
		return kind
	}
	if goal != "" {
		if i := strings.IndexAny(goal, " \t\n"); i > 0 {
			return goal[:i]
		}
		return goal
	}
	return ""
}

// ---------------------------------------------------------------------------
// per-context recorder
// ---------------------------------------------------------------------------

type activeRecorder struct {
	mu        sync.Mutex
	runID     string
	brainKind string
	goal      string
	startedAt time.Time
	site      string
	lastURL   string
	actions   []RecordedAction
	finished  bool

	// P3.5 信号,供 toolpolicy.DecideBrowserStage 读取。环形队列语义:超容
	// 量时丢最老的。
	//
	//   recentPatternScores —— 最近 N 次 pattern_match 的 top score
	//                          (≥ recentPatternScoresCap 丢头部)。
	//   recentTurnOutcomes  —— 最近 M 次 turn 的 outcome
	//                          ("ok"/"error",≥ recentTurnOutcomesCap 丢头部)。
	//
	// 大小以常量形式固定,不进入公开 API —— 避免 toolpolicy 因窗口大小
	// 和 recorder 耦合。决策器自己不关心窗口多大,只处理给到它的切片。
	recentPatternScores []float64
	recentTurnOutcomes  []string
}

const (
	recentPatternScoresCap = 10
	recentTurnOutcomesCap  = 3
)

func (r *activeRecorder) append(a RecordedAction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.actions = append(r.actions, a)
	if site, u := extractSiteFromParams(a.Params); site != "" {
		if r.site == "" {
			r.site = site
		}
		if u != "" {
			r.lastURL = u
		}
	}
}

var (
	recorderMu   sync.Mutex
	ctxRecorders = map[context.Context]*activeRecorder{}
)

// BindRecorder 把一次 run 关联到 ctx。brainKind 填 "browser" / "code" 等。
// ctx 不能为 nil。多次调用相同 ctx 会覆盖旧 recorder(罕见场景)。
func BindRecorder(ctx context.Context, runID, brainKind, goal string) {
	if ctx == nil {
		return
	}
	rec := &activeRecorder{
		runID:     runID,
		brainKind: brainKind,
		goal:      goal,
		startedAt: time.Now().UTC(),
	}
	recorderMu.Lock()
	ctxRecorders[ctx] = rec
	recorderMu.Unlock()
}

// FinishRecorder 把当前 ctx 绑定的序列交给 sink 持久化,然后解绑。
// outcome 建议 "success" / "failure",与 ui_pattern_learn.go 约定一致。
// ctx 没绑、序列为空、没配 sink 均视为 no-op 返回 nil。
func FinishRecorder(ctx context.Context, outcome string) error {
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	delete(ctxRecorders, ctx)
	recorderMu.Unlock()
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	rec.finished = true
	actions := append([]RecordedAction(nil), rec.actions...)
	runID, brainKind, goal := rec.runID, rec.brainKind, rec.goal
	site, lastURL, started := rec.site, rec.lastURL, rec.startedAt
	rec.mu.Unlock()

	if len(actions) == 0 {
		return nil
	}
	sink := currentSink()
	if sink == nil {
		return nil
	}

	persistActions := make([]persistence.InteractionAction, 0, len(actions))
	for _, a := range actions {
		paramsJSON := ""
		if a.Params != nil {
			if buf, err := json.Marshal(a.Params); err == nil {
				paramsJSON = string(buf)
			}
		}
		persistActions = append(persistActions, persistence.InteractionAction{
			Tool:        a.Tool,
			Params:      paramsJSON,
			ElementRole: a.ElementRole,
			ElementName: a.ElementName,
			ElementType: a.ElementType,
			Result:      a.Result,
		})
	}

	seq := &persistence.InteractionSequence{
		RunID:      runID,
		BrainKind:  brainKind,
		Goal:       goal,
		Site:       site,
		URL:        lastURL,
		Outcome:    outcome,
		DurationMs: time.Since(started).Milliseconds(),
		StartedAt:  started,
		Actions:    persistActions,
	}
	return sink.RecordInteractionSequence(ctx, seq)
}

// RecordPatternMatchScore 记一次 pattern_match 的 top 候选 score。
// 由 sdk/tool/builtin_browser_pattern.go 在 MatchPatterns 成功后调用。
// ctx 没绑 recorder 或 score 非法(NaN/负)直接丢弃。
func RecordPatternMatchScore(ctx context.Context, score float64) {
	if ctx == nil || score < 0 || score != score { // NaN 检测
		return
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.recentPatternScores = append(rec.recentPatternScores, score)
	if n := len(rec.recentPatternScores); n > recentPatternScoresCap {
		rec.recentPatternScores = rec.recentPatternScores[n-recentPatternScoresCap:]
	}
}

// RecordTurnOutcome 记一次 turn 的结果。outcome 建议 "ok" / "error",其他
// 值也会被存下,由决策器自行解读(避免此处搞字符串枚举的强依赖)。
func RecordTurnOutcome(ctx context.Context, outcome string) {
	if ctx == nil || outcome == "" {
		return
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.recentTurnOutcomes = append(rec.recentTurnOutcomes, outcome)
	if n := len(rec.recentTurnOutcomes); n > recentTurnOutcomesCap {
		rec.recentTurnOutcomes = rec.recentTurnOutcomes[n-recentTurnOutcomesCap:]
	}
}

// RecentPatternScores 读 ctx 绑定 recorder 的 pattern_match 最近 score 窗口
// 拷贝。ctx 没绑返回 nil。决策器用这个做 max/avg,不触碰内部锁。
func RecentPatternScores(ctx context.Context) []float64 {
	if ctx == nil {
		return nil
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]float64, len(rec.recentPatternScores))
	copy(out, rec.recentPatternScores)
	return out
}

// RecentTurnOutcomes 读 ctx 绑定 recorder 的最近 turn 结果窗口拷贝。
func RecentTurnOutcomes(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.recentTurnOutcomes))
	copy(out, rec.recentTurnOutcomes)
	return out
}

// recordInteractionForLearning 由 anomalyInjectingTool 调用。
func recordInteractionForLearning(ctx context.Context, toolName string, args json.RawMessage, res *Result) {
	recorderMu.Lock()
	rec := ctxRecorders[ctx]
	recorderMu.Unlock()
	if rec == nil {
		return
	}
	var params map[string]interface{}
	if len(args) > 0 && string(args) != "null" {
		_ = json.Unmarshal(args, &params)
	}
	outcome := "ok"
	if res != nil && res.IsError {
		outcome = "error"
	}
	rec.append(RecordedAction{Tool: toolName, Params: params, Result: outcome})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func extractSiteFromParams(params map[string]interface{}) (site, fullURL string) {
	raw, ok := params["url"].(string)
	if !ok || raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", raw
	}
	return u.Scheme + "://" + u.Host, raw
}
