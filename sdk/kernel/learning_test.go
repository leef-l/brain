package kernel

import (
	"math"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// 1. EWMAScore.Update 正确性
// ---------------------------------------------------------------------------

func TestEWMAScoreUpdate(t *testing.T) {
	e := EWMAScore{Value: 0.5, Alpha: 0.2}
	e.Update(1.0)

	// V = 0.2*1.0 + 0.8*0.5 = 0.6
	want := 0.6
	if math.Abs(e.Value-want) > 1e-9 {
		t.Fatalf("EWMAScore.Update: got %f, want %f", e.Value, want)
	}
	if e.Updated.IsZero() {
		t.Fatal("EWMAScore.Update should set Updated time")
	}

	// 连续更新
	e.Update(1.0)
	// V = 0.2*1.0 + 0.8*0.6 = 0.68
	want = 0.68
	if math.Abs(e.Value-want) > 1e-9 {
		t.Fatalf("second update: got %f, want %f", e.Value, want)
	}
}

func TestEWMAScoreDefaultAlpha(t *testing.T) {
	e := EWMAScore{Value: 0.5, Alpha: 0} // 无效 alpha
	e.Update(1.0)
	// 应该使用默认 alpha=0.2
	want := 0.6
	if math.Abs(e.Value-want) > 1e-9 {
		t.Fatalf("default alpha: got %f, want %f", e.Value, want)
	}
}

// ---------------------------------------------------------------------------
// 2 & 3. ComputeWeights 归一化 + 各 priority 模式
// ---------------------------------------------------------------------------

func TestComputeWeightsNormalized(t *testing.T) {
	policies := []WeightPolicy{
		{},
		{LatencyPriority: true},
		{CostPriority: true},
		{QualityPriority: true},
		{LatencyPriority: true, CostPriority: true},
		{LatencyPriority: true, CostPriority: true, QualityPriority: true},
	}

	for _, p := range policies {
		a, s, c, st := ComputeWeights(p)
		total := a + s + c + st
		if math.Abs(total-1.0) > 1e-9 {
			t.Errorf("ComputeWeights(%+v) sum=%f, want 1.0", p, total)
		}
		// 每个权重都应 >= 0
		if a < 0 || s < 0 || c < 0 || st < 0 {
			t.Errorf("ComputeWeights(%+v) has negative weight", p)
		}
	}
}

func TestComputeWeightsLatencyBoost(t *testing.T) {
	_, sBase, _, _ := ComputeWeights(WeightPolicy{})
	_, sLatency, _, _ := ComputeWeights(WeightPolicy{LatencyPriority: true})
	if sLatency <= sBase {
		t.Errorf("LatencyPriority should boost speed weight: base=%f, latency=%f", sBase, sLatency)
	}
}

func TestComputeWeightsCostBoost(t *testing.T) {
	_, _, cBase, _ := ComputeWeights(WeightPolicy{})
	_, _, cCost, _ := ComputeWeights(WeightPolicy{CostPriority: true})
	if cCost <= cBase {
		t.Errorf("CostPriority should boost cost weight: base=%f, cost=%f", cBase, cCost)
	}
}

func TestComputeWeightsQualityBoost(t *testing.T) {
	aBase, _, _, _ := ComputeWeights(WeightPolicy{})
	aQuality, _, _, _ := ComputeWeights(WeightPolicy{QualityPriority: true})
	if aQuality <= aBase {
		t.Errorf("QualityPriority should boost accuracy weight: base=%f, quality=%f", aBase, aQuality)
	}
}

// ---------------------------------------------------------------------------
// 4. WilsonConfidence 不同样本量
// ---------------------------------------------------------------------------

func TestWilsonConfidence(t *testing.T) {
	// n=0 → 0
	if c := WilsonConfidence(0); c != 0 {
		t.Errorf("WilsonConfidence(0)=%f, want 0", c)
	}
	// n<0 → 0
	if c := WilsonConfidence(-1); c != 0 {
		t.Errorf("WilsonConfidence(-1)=%f, want 0", c)
	}

	// 单调递增
	prev := WilsonConfidence(1)
	for _, n := range []int{2, 5, 10, 50, 100, 1000} {
		c := WilsonConfidence(n)
		if c <= prev {
			t.Errorf("WilsonConfidence not monotonically increasing: n=%d c=%f <= prev=%f", n, c, prev)
		}
		if c < 0 || c > 1 {
			t.Errorf("WilsonConfidence(%d)=%f out of [0,1]", n, c)
		}
		prev = c
	}

	// 大样本趋近 1
	c := WilsonConfidence(10000)
	if c < 0.99 {
		t.Errorf("WilsonConfidence(10000)=%f, expected >= 0.99", c)
	}
}

// ---------------------------------------------------------------------------
// 5. RankBrains 冷启动 vs 正常
// ---------------------------------------------------------------------------

func TestRankBrainsColdStart(t *testing.T) {
	le := NewLearningEngine()

	// 只记录 2 次 → 冷启动
	le.RecordDelegateResult(agent.KindCode, "coding", 0.9, 0.8, 0.7, 0.9)
	le.RecordDelegateResult(agent.KindCode, "coding", 0.9, 0.8, 0.7, 0.9)

	ranks := le.RankBrains("coding", WeightPolicy{})
	if len(ranks) != 1 {
		t.Fatalf("expected 1 ranking, got %d", len(ranks))
	}
	if !ranks[0].IsColdStart {
		t.Error("expected cold start with only 2 samples")
	}

	// 补充到 5 次 → 非冷启动
	for i := 0; i < 3; i++ {
		le.RecordDelegateResult(agent.KindCode, "coding", 0.9, 0.8, 0.7, 0.9)
	}
	ranks = le.RankBrains("coding", WeightPolicy{})
	if ranks[0].IsColdStart {
		t.Error("expected non-cold-start with 5 samples")
	}
}

// ---------------------------------------------------------------------------
// 6. RecordDelegateResult 多次后排名变化
// ---------------------------------------------------------------------------

func TestRankBrainsMultipleBrains(t *testing.T) {
	le := NewLearningEngine()

	// Code brain: 高准确率
	for i := 0; i < 10; i++ {
		le.RecordDelegateResult(agent.KindCode, "coding", 0.95, 0.7, 0.6, 0.9)
	}
	// Verifier brain: 低准确率
	for i := 0; i < 10; i++ {
		le.RecordDelegateResult(agent.KindVerifier, "coding", 0.3, 0.5, 0.4, 0.5)
	}

	ranks := le.RankBrains("coding", WeightPolicy{QualityPriority: true})
	if len(ranks) < 2 {
		t.Fatalf("expected at least 2 rankings, got %d", len(ranks))
	}
	if ranks[0].BrainKind != agent.KindCode {
		t.Errorf("expected KindCode first, got %s", ranks[0].BrainKind)
	}
	if ranks[0].Score <= ranks[1].Score {
		t.Errorf("first rank score %f should > second %f", ranks[0].Score, ranks[1].Score)
	}
}

func TestRankBrainsNoData(t *testing.T) {
	le := NewLearningEngine()
	ranks := le.RankBrains("unknown", WeightPolicy{})
	if len(ranks) != 0 {
		t.Errorf("expected 0 rankings for unknown task, got %d", len(ranks))
	}
}

// ---------------------------------------------------------------------------
// 7. SequenceLearner 记录和推荐
// ---------------------------------------------------------------------------

func TestSequenceLearnerRecordAndRecommend(t *testing.T) {
	sl := &SequenceLearner{}

	// 记录: code 得分高, verifier 得分低
	sl.RecordSequence(TaskSequenceRecord{
		SequenceID: "seq-1",
		Steps: []TaskStep{
			{BrainKind: agent.KindCode, TaskType: "coding", Duration: time.Second, Score: 0.9},
			{BrainKind: agent.KindVerifier, TaskType: "verify", Duration: time.Second, Score: 0.3},
		},
		TotalScore: 0.6,
	})

	// 推荐排序: code(0.9) 应在 verifier(0.3) 前面
	steps := []TaskStep{
		{BrainKind: agent.KindVerifier, TaskType: "verify"},
		{BrainKind: agent.KindCode, TaskType: "coding"},
	}
	recommended := sl.RecommendOrder(steps)
	if len(recommended) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(recommended))
	}
	if recommended[0].BrainKind != agent.KindCode {
		t.Errorf("expected code first, got %s", recommended[0].BrainKind)
	}
}

func TestSequenceLearnerEmpty(t *testing.T) {
	sl := &SequenceLearner{}
	steps := []TaskStep{
		{BrainKind: agent.KindCode, TaskType: "coding"},
	}
	// 没有历史记录，应该返回原始顺序
	recommended := sl.RecommendOrder(steps)
	if len(recommended) != 1 {
		t.Fatalf("expected 1 step, got %d", len(recommended))
	}
}

// ---------------------------------------------------------------------------
// 8. PreferenceLearner 记录和查询
// ---------------------------------------------------------------------------

func TestPreferenceLearnerRecordAndGet(t *testing.T) {
	pl := &PreferenceLearner{preferences: make(map[string]*UserPreference)}

	// 不存在时返回 nil
	if p := pl.GetPreference("verbosity"); p != nil {
		t.Error("expected nil for non-existent preference")
	}

	// 记录偏好
	pl.RecordFeedback("verbosity", "concise", 0.8)
	p := pl.GetPreference("verbosity")
	if p == nil {
		t.Fatal("expected preference after recording")
	}
	if p.Value != "concise" || p.Weight != 0.8 {
		t.Errorf("unexpected preference: %+v", p)
	}

	// 更新偏好
	pl.RecordFeedback("verbosity", "detailed", 1.0)
	p = pl.GetPreference("verbosity")
	if p.Value != "detailed" {
		t.Errorf("expected updated value 'detailed', got '%s'", p.Value)
	}
	// 权重应该是 EWMA: 0.3*1.0 + 0.7*0.8 = 0.86
	wantWeight := 0.86
	if math.Abs(p.Weight-wantWeight) > 1e-9 {
		t.Errorf("expected weight %f, got %f", wantWeight, p.Weight)
	}
}

func TestPreferenceLearnerAllPreferences(t *testing.T) {
	pl := &PreferenceLearner{preferences: make(map[string]*UserPreference)}
	pl.RecordFeedback("format", "markdown", 0.7)
	pl.RecordFeedback("risk", "low", 0.9)

	all := pl.AllPreferences()
	if len(all) != 2 {
		t.Errorf("expected 2 preferences, got %d", len(all))
	}
	if all["format"].Value != "markdown" {
		t.Errorf("unexpected format value: %s", all["format"].Value)
	}
}

func TestPreferenceLearnerWeightClamping(t *testing.T) {
	pl := &PreferenceLearner{preferences: make(map[string]*UserPreference)}
	pl.RecordFeedback("test", "val", -0.5)
	p := pl.GetPreference("test")
	if p.Weight != 0 {
		t.Errorf("negative weight should be clamped to 0, got %f", p.Weight)
	}

	pl.RecordFeedback("test2", "val", 1.5)
	p = pl.GetPreference("test2")
	if p.Weight != 1.0 {
		t.Errorf("weight > 1 should be clamped to 1, got %f", p.Weight)
	}
}

// ---------------------------------------------------------------------------
// 9. LearningEngine 整合测试
// ---------------------------------------------------------------------------

func TestLearningEngineIntegration(t *testing.T) {
	le := NewLearningEngine()

	// L1: 记录多个 brain 的多次执行结果
	for i := 0; i < 10; i++ {
		le.RecordDelegateResult(agent.KindCode, "coding", 0.9, 0.8, 0.7, 0.85)
		le.RecordDelegateResult(agent.KindCentral, "coding", 0.6, 0.5, 0.9, 0.5)
	}

	ranks := le.RankBrains("coding", WeightPolicy{})
	if len(ranks) < 2 {
		t.Fatalf("expected >= 2 rankings, got %d", len(ranks))
	}
	// Code 应该排在 Central 前面（综合得分更高）
	if ranks[0].BrainKind != agent.KindCode {
		t.Errorf("expected code brain ranked first, got %s", ranks[0].BrainKind)
	}

	// L2: 记录序列
	le.RecordSequence(TaskSequenceRecord{
		SequenceID: "s1",
		Steps: []TaskStep{
			{BrainKind: agent.KindCode, TaskType: "coding", Score: 0.9},
			{BrainKind: agent.KindVerifier, TaskType: "verify", Score: 0.4},
		},
		TotalScore: 0.65,
	})

	recommended := le.RecommendOrder([]TaskStep{
		{BrainKind: agent.KindVerifier, TaskType: "verify"},
		{BrainKind: agent.KindCode, TaskType: "coding"},
	})
	if recommended[0].BrainKind != agent.KindCode {
		t.Errorf("L2: expected code first in recommendation")
	}

	// L3: 用户偏好
	le.RecordUserFeedback("output_format", "json", 0.9)
	pref := le.GetPreference("output_format")
	if pref == nil || pref.Value != "json" {
		t.Error("L3: expected preference for output_format=json")
	}
}
