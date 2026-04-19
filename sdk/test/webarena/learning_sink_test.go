package webarena

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/persistence"
)

// fakeSink 只实现 interactionSequenceSink —— 证明 sink 不依赖真 SQLite。
type fakeSink struct {
	saved  []*persistence.InteractionSequence
	errAt  int // saved 长度等于 errAt 时下一次 SaveInteractionSequence 返回 err
	errSet bool
}

func (f *fakeSink) SaveInteractionSequence(_ context.Context, seq *persistence.InteractionSequence) error {
	if f.errSet && len(f.saved) == f.errAt {
		f.errSet = false
		return errors.New("boom")
	}
	f.saved = append(f.saved, seq)
	return nil
}

func TestSaveResultsMapsFieldsAndOutcome(t *testing.T) {
	results := []*TaskResult{
		{
			Task:     &Task{ID: "reddit-browse-subreddit", Category: "reddit", Site: "https://old.reddit.com", Goal: "browse sub", MaxTurns: 4},
			TaskID:   "reddit-browse-subreddit",
			Success:  true,
			Turns:    3,
			Duration: 250 * time.Millisecond,
		},
		{
			Task:       &Task{ID: "gitlab-open-issue-detail", Category: "gitlab", Site: "https://gitlab.com", Goal: "open issue", MaxTurns: 6},
			TaskID:     "gitlab-open-issue-detail",
			Success:    false,
			Turns:      6,
			Duration:   800 * time.Millisecond,
			FailReason: "post-condition missing",
		},
	}
	sink := &fakeSink{}
	saved, err := SaveResultsToLearningStore(context.Background(), sink, "runA", results)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if saved != 2 {
		t.Errorf("saved = %d, want 2", saved)
	}
	if len(sink.saved) != 2 {
		t.Fatalf("sink len = %d, want 2", len(sink.saved))
	}

	// 成功条:outcome=success,brain_kind=browser,site/goal 透传。
	s0 := sink.saved[0]
	if s0.Outcome != "success" {
		t.Errorf("seq[0] Outcome=%q, want success", s0.Outcome)
	}
	if s0.BrainKind != "browser" {
		t.Errorf("seq[0] BrainKind=%q, want browser", s0.BrainKind)
	}
	if s0.Site != "https://old.reddit.com" {
		t.Errorf("seq[0] Site=%q, want reddit url", s0.Site)
	}
	if s0.RunID != "runA" {
		t.Errorf("seq[0] RunID=%q, want runA", s0.RunID)
	}
	if s0.DurationMs != 250 {
		t.Errorf("seq[0] DurationMs=%d, want 250", s0.DurationMs)
	}
	if len(s0.Actions) != 1 || s0.Actions[0].Tool != "webarena.run_task" {
		t.Errorf("seq[0] Actions missing webarena.run_task: %+v", s0.Actions)
	}
	// 成功态 result=ok
	if s0.Actions[0].Result != "ok" {
		t.Errorf("seq[0] Action.Result=%q, want ok", s0.Actions[0].Result)
	}
	// Params 是合法 JSON 且含 task_id / category
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(s0.Actions[0].Params), &meta); err != nil {
		t.Fatalf("seq[0] Params not JSON: %v", err)
	}
	if meta["task_id"] != "reddit-browse-subreddit" {
		t.Errorf("seq[0] meta.task_id=%v", meta["task_id"])
	}
	if meta["category"] != "reddit" {
		t.Errorf("seq[0] meta.category=%v", meta["category"])
	}

	// 失败条:outcome=failure,action.Result 带 fail_reason。
	s1 := sink.saved[1]
	if s1.Outcome != "failure" {
		t.Errorf("seq[1] Outcome=%q, want failure", s1.Outcome)
	}
	if s1.Actions[0].Result != "post-condition missing" {
		t.Errorf("seq[1] Action.Result=%q, want fail_reason pass-through", s1.Actions[0].Result)
	}
}

func TestSaveResultsAutoGeneratesRunID(t *testing.T) {
	results := []*TaskResult{{
		Task: &Task{ID: "x", Category: "map", Site: "https://osm.org"}, TaskID: "x", Success: true, Duration: 10 * time.Millisecond,
	}}
	sink := &fakeSink{}
	if _, err := SaveResultsToLearningStore(context.Background(), sink, "", results); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(sink.saved) != 1 {
		t.Fatalf("want 1 saved")
	}
	// 自动生成的 runID 形如 webarena-<unix>
	rid := sink.saved[0].RunID
	if len(rid) < len("webarena-") || rid[:len("webarena-")] != "webarena-" {
		t.Errorf("auto runID prefix missing: %q", rid)
	}
}

func TestSaveResultsNilStoreNoop(t *testing.T) {
	// 空 store 必须安全:CI 不配置持久化时不应崩。
	saved, err := SaveResultsToLearningStore(context.Background(), nil, "r", []*TaskResult{{TaskID: "x"}})
	if err != nil {
		t.Errorf("nil store should noop, got err=%v", err)
	}
	if saved != 0 {
		t.Errorf("nil store saved=%d, want 0", saved)
	}
}

func TestSaveResultsContinuesOnError(t *testing.T) {
	// 中间某条 Save 报错,后续仍应继续;返回第一个 err + 成功计数不含报错条。
	results := []*TaskResult{
		{Task: &Task{ID: "a"}, TaskID: "a", Success: true},
		{Task: &Task{ID: "b"}, TaskID: "b", Success: true},
		{Task: &Task{ID: "c"}, TaskID: "c", Success: true},
	}
	sink := &fakeSink{errAt: 1, errSet: true} // 第二条失败
	saved, err := SaveResultsToLearningStore(context.Background(), sink, "r", results)
	if err == nil || err.Error() != "boom" {
		t.Errorf("want err=boom, got %v", err)
	}
	if saved != 2 {
		t.Errorf("saved=%d, want 2 (a + c)", saved)
	}
	if len(sink.saved) != 2 {
		t.Errorf("sink saved len=%d, want 2", len(sink.saved))
	}
}

// TestSinkSatisfiedByLearningStore —— 编译期防御:确保 interactionSequenceSink
// 能被 persistence.LearningStore 赋值(接口兼容)。任何对 LearningStore
// SaveInteractionSequence 签名的破坏性改动都会在此 fail。
func TestSinkSatisfiedByLearningStore(t *testing.T) {
	var _ interactionSequenceSink = (persistence.LearningStore)(nil)
}

// TestP2CategoryCoverage —— P2.1 验收:tasks/ 里必须覆盖 4 大类
// (reddit / shopping / map / gitlab),每类 ≥ 7 条,总数在 30-50 区间。
func TestP2CategoryCoverage(t *testing.T) {
	tasks, err := LoadTasks("tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) < 30 || len(tasks) > 50 {
		t.Errorf("task count = %d, want 30-50", len(tasks))
	}
	want := []string{"reddit", "shopping", "map", "gitlab"}
	byCat := map[string]int{}
	for _, task := range tasks {
		byCat[task.Category]++
	}
	for _, cat := range want {
		if n := byCat[cat]; n < 7 {
			t.Errorf("category %q has only %d tasks, want >= 7", cat, n)
		}
	}
}
