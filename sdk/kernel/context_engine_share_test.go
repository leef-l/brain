package kernel

import (
	"context"
	"sync"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
)

func makeShareMsg(text string) llm.Message {
	return llm.Message{
		Role:    "user",
		Content: []llm.ContentBlock{{Type: "text", Text: text}},
	}
}

// Task #18: 不同 (from, to) 桶相互隔离,不会串
func TestShareBucketsIsolation(t *testing.T) {
	eng := NewDefaultContextEngine()
	ctx := context.Background()

	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("browser"), []llm.Message{makeShareMsg("B1")})
	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("code"), []llm.Message{makeShareMsg("C1")})

	bm := eng.SharedFor(agent.Kind("central"), agent.Kind("browser"))
	cm := eng.SharedFor(agent.Kind("central"), agent.Kind("code"))

	if len(bm) != 1 || bm[0].Content[0].Text != "B1" {
		t.Errorf("browser bucket corrupted: %+v", bm)
	}
	if len(cm) != 1 || cm[0].Content[0].Text != "C1" {
		t.Errorf("code bucket corrupted: %+v", cm)
	}
}

// Task #18: ClearShared 切断特定桶,不影响其他桶
func TestClearSharedTargeted(t *testing.T) {
	eng := NewDefaultContextEngine()
	ctx := context.Background()
	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("browser"), []llm.Message{makeShareMsg("B1")})
	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("code"), []llm.Message{makeShareMsg("C1")})

	eng.ClearShared(agent.Kind("central"), agent.Kind("browser"))

	if got := eng.SharedFor(agent.Kind("central"), agent.Kind("browser")); got != nil {
		t.Errorf("expected browser bucket cleared, got %+v", got)
	}
	if got := eng.SharedFor(agent.Kind("central"), agent.Kind("code")); len(got) != 1 {
		t.Errorf("code bucket should survive, got %+v", got)
	}
}

// Task #18: 空 key 清空全部
func TestClearSharedAll(t *testing.T) {
	eng := NewDefaultContextEngine()
	ctx := context.Background()
	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("browser"), []llm.Message{makeShareMsg("B1")})
	_ = eng.Share(ctx, agent.Kind("central"), agent.Kind("code"), []llm.Message{makeShareMsg("C1")})

	eng.ClearShared("", "")

	if got := eng.SharedFor(agent.Kind("central"), agent.Kind("browser")); got != nil {
		t.Errorf("browser bucket should be gone, got %+v", got)
	}
	if got := eng.SharedFor(agent.Kind("central"), agent.Kind("code")); got != nil {
		t.Errorf("code bucket should be gone, got %+v", got)
	}
	if eng.SharedMessages != nil {
		t.Errorf("legacy SharedMessages should also be reset")
	}
}

// Task #18: 多个 goroutine 并发 Share 不同 (from, to) 不会相互覆盖/竞态
func TestShareConcurrentBuckets(t *testing.T) {
	eng := NewDefaultContextEngine()
	ctx := context.Background()
	var wg sync.WaitGroup
	targets := []string{"browser", "code", "data", "verifier", "fault"}
	for _, t := range targets {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = eng.Share(ctx, agent.Kind("central"), agent.Kind(target),
					[]llm.Message{makeShareMsg(target + "-msg")})
			}
		}(t)
	}
	wg.Wait()

	for _, target := range targets {
		got := eng.SharedFor(agent.Kind("central"), agent.Kind(target))
		if len(got) == 0 {
			t.Errorf("bucket %s empty — concurrent writes clobbered", target)
			continue
		}
		if got[0].Content[0].Text != target+"-msg" {
			t.Errorf("bucket %s corrupted: %+v", target, got)
		}
	}
}