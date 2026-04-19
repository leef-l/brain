package webarena

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// learning_sink 把 WebArena TaskResult 桥接到既有
// persistence.LearningStore.SaveInteractionSequence 上,供 CI 夜间跑完后
// 持久化。
//
// 设计原则:
//   - 不改 runner.go(保持"不改框架"铁律)。
//   - 不再建新表:复用 persistence.InteractionSequence + 已有
//     interaction_sequences 表(#13)。
//   - 依赖缩到最小接口 interactionSequenceSink,只要一个
//     SaveInteractionSequence 方法;传 persistence.LearningStore
//     天然满足。这样单测用轻量 stub 即可,不拉 SQLite。
//   - brain_kind 固定填 "browser"(WebArena 100% 浏览器任务),让
//     dashboard 的 /v1/dashboard/learning 的 InteractionStat 能按
//     "browser" 做同一维度聚合。Goal/Site 原样透传,便于后续
//     按 site 做趋势切片。

// interactionSequenceSink 是 persistence.LearningStore 里唯一被本模块
// 使用的方法子集,单测里实现它就行。
type interactionSequenceSink interface {
	SaveInteractionSequence(ctx context.Context, seq *persistence.InteractionSequence) error
}

// SaveResultsToLearningStore 把一次 Run 的全部结果写入 LearningStore。
// runID 用来把同一次批次的多条序列聚合起来(dashboard 历史成功率趋势
// 按 runID 分桶)。空 runID 会被自动填一个 "webarena-<unix>" 的值。
//
// 返回写入成功的条数和第一个遇到的错误(错误不中断整个批次——持久化
// 是尽力而为,CI 即使 store 挂了也要把本地 report.md 产生出来)。
func SaveResultsToLearningStore(ctx context.Context, store interactionSequenceSink, runID string, results []*TaskResult) (int, error) {
	if store == nil || len(results) == 0 {
		return 0, nil
	}
	if runID == "" {
		runID = fmt.Sprintf("webarena-%d", time.Now().UTC().Unix())
	}
	var firstErr error
	saved := 0
	for _, r := range results {
		seq := taskResultToInteractionSequence(runID, r)
		if err := store.SaveInteractionSequence(ctx, seq); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		saved++
	}
	return saved, firstErr
}

// taskResultToInteractionSequence 把单条 TaskResult 翻译成 InteractionSequence。
// 策略:
//   - Outcome:success → "success",否则 "failure"(InteractionSequence
//     只支持这两种值,见 learning_store.go:68)。
//   - Actions:WebArena runner 不记录具体 tool-call,只有聚合 Turns。
//     把 turns + success checks + fail_reason 压成一条 Action 占位,
//     保证 dashboard InteractionStat 里能按 brain 聚合到数字;未来真
//     Executor 如果记录逐步 action,可在 Executor.Execute 内自行填充
//     到 TaskResult(未来扩展)。
//   - Site / Goal 直接从 task 取;Task 为 nil 时留空,SaveInteractionSequence
//     能接受空字段。
func taskResultToInteractionSequence(runID string, r *TaskResult) *persistence.InteractionSequence {
	outcome := "failure"
	if r.Success {
		outcome = "success"
	}
	seq := &persistence.InteractionSequence{
		RunID:      runID,
		BrainKind:  "browser",
		Outcome:    outcome,
		DurationMs: r.Duration.Milliseconds(),
		StartedAt:  time.Now().UTC().Add(-r.Duration),
	}
	if r.Task != nil {
		seq.Goal = r.Task.Goal
		seq.Site = r.Task.Site
	}
	// 把聚合信息打包成单条 Action,便于未来按 element_role 汇总——run 级
	// 元数据用 element_role="webarena_summary" 占位,Result 是 fail_reason
	// 或 success 标记,Params 是 JSON 化的附加字段。
	meta := map[string]interface{}{
		"task_id":   r.TaskID,
		"turns":     r.Turns,
		"max_turns": maxTurns(r),
		"category":  category(r),
	}
	rawMeta, _ := json.Marshal(meta)
	result := "ok"
	if !r.Success {
		result = r.FailReason
		if result == "" {
			result = "failed"
		}
	}
	seq.Actions = []persistence.InteractionAction{{
		Tool:        "webarena.run_task",
		Params:      string(rawMeta),
		ElementRole: "webarena_summary",
		Result:      result,
	}}
	return seq
}

func maxTurns(r *TaskResult) int {
	if r.Task == nil {
		return 0
	}
	return r.Task.MaxTurns
}

func category(r *TaskResult) string {
	if r.Task == nil {
		return ""
	}
	return r.Task.Category
}
