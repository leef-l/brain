package tool

import (
	"context"
	"math"
	"testing"
)

// P3.5:recorder 里的 pattern_score / turn_outcome 环形窗口契约。

func TestRecordPatternMatchScoreRingBuffer(t *testing.T) {
	ctx := context.Background()
	BindRecorder(ctx, "run-p3-5-score", "browser", "goal")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	for i := 0; i < recentPatternScoresCap+5; i++ {
		RecordPatternMatchScore(ctx, float64(i)/100.0)
	}
	got := RecentPatternScores(ctx)
	if len(got) != recentPatternScoresCap {
		t.Fatalf("cap not enforced: len=%d want %d", len(got), recentPatternScoresCap)
	}
	// 最老的应被丢弃,第 0 个是 5/100=0.05
	if got[0] != 0.05 {
		t.Errorf("oldest not evicted: got[0]=%v want 0.05", got[0])
	}
}

func TestRecordPatternMatchScoreRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	BindRecorder(ctx, "run-p3-5-reject", "browser", "goal")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	RecordPatternMatchScore(ctx, -1.0)
	RecordPatternMatchScore(ctx, math.NaN())
	RecordPatternMatchScore(ctx, 0.5)
	got := RecentPatternScores(ctx)
	if len(got) != 1 || got[0] != 0.5 {
		t.Errorf("invalid scores leaked through: %+v", got)
	}
}

func TestRecordTurnOutcomeRingBuffer(t *testing.T) {
	ctx := context.Background()
	BindRecorder(ctx, "run-p3-5-outcome", "browser", "goal")
	t.Cleanup(func() { _ = FinishRecorder(ctx, "success") })

	RecordTurnOutcome(ctx, "ok")
	RecordTurnOutcome(ctx, "error")
	RecordTurnOutcome(ctx, "ok")
	RecordTurnOutcome(ctx, "error") // should evict oldest "ok"
	got := RecentTurnOutcomes(ctx)
	if len(got) != recentTurnOutcomesCap {
		t.Fatalf("cap not enforced: len=%d want %d", len(got), recentTurnOutcomesCap)
	}
	if got[0] != "error" || got[2] != "error" {
		t.Errorf("ring eviction wrong: %+v", got)
	}
}

func TestRecordXOnNilRecorderNoPanic(t *testing.T) {
	// 没 BindRecorder 的 ctx,写入应安全 no-op,读返回 nil。
	ctx := context.Background()
	RecordPatternMatchScore(ctx, 0.9)
	RecordTurnOutcome(ctx, "ok")
	if s := RecentPatternScores(ctx); s != nil {
		t.Errorf("unbound ctx returned scores: %+v", s)
	}
	if o := RecentTurnOutcomes(ctx); o != nil {
		t.Errorf("unbound ctx returned outcomes: %+v", o)
	}
}
