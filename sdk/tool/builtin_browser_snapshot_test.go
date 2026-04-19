package tool

import (
	"fmt"
	"testing"
)

// P3.4 — snapshot 增量合并的单元测试 + benchmark。
//
// collectIncremental / installSnapshotObserver 依赖真实 CDP 会话,在这里
// 不覆盖(跑在集成测试里)。本文件只验证合并算法本身:id 去重、removed
// 剔除、新增 append。这是增量路径正确性的核心,JS 侧出了问题回退全量不
// 会误数据。

func TestMergeIncrementalUpdate_Basic(t *testing.T) {
	prev := []brainElement{
		{ID: 1, Name: "login"},
		{ID: 2, Name: "password"},
		{ID: 3, Name: "submit"},
	}
	// 3 号元素被移除,2 号 Name 改了,4 号新增
	updated := []brainElement{
		{ID: 2, Name: "password-new"},
		{ID: 4, Name: "captcha"},
	}
	removed := []int{3}

	got := mergeIncrementalUpdate(prev, updated, removed)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (prev - 1 removed + 1 new)", len(got))
	}
	// 顺序:1, 2(更新), 4(新增)
	if got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 4 {
		t.Errorf("order wrong: %+v", got)
	}
	if got[1].Name != "password-new" {
		t.Errorf("id=2 should be updated, got %q", got[1].Name)
	}
}

func TestMergeIncrementalUpdate_EmptyIncrement(t *testing.T) {
	prev := []brainElement{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	got := mergeIncrementalUpdate(prev, nil, nil)
	// 空增量直接返回原切片
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestMergeIncrementalUpdate_RemoveOnly(t *testing.T) {
	prev := []brainElement{{ID: 1}, {ID: 2}, {ID: 3}}
	got := mergeIncrementalUpdate(prev, nil, []int{2})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, e := range got {
		if e.ID == 2 {
			t.Errorf("removed ID 2 still present")
		}
	}
}

func TestMergeIncrementalUpdate_AllReplaced(t *testing.T) {
	prev := []brainElement{{ID: 1, Name: "old"}}
	upd := []brainElement{{ID: 1, Name: "new"}}
	got := mergeIncrementalUpdate(prev, upd, nil)
	if len(got) != 1 || got[0].Name != "new" {
		t.Errorf("replace failed: %+v", got)
	}
}

// BenchmarkSnapshotIncrementalMerge 衡量 1000-元素页的增量合并开销。
// 模拟"大页"情形:1000 个已缓存元素 + 50 个变动元素 + 10 个删除。
func BenchmarkSnapshotIncrementalMerge(b *testing.B) {
	prev := makeBenchElements(1000)
	updated := makeBenchElements(50)
	// 把前 50 个的 ID 映射成已有 ID 的一部分(id 11..60),触发 in-place 替换
	for i := range updated {
		updated[i].ID = 10 + i + 1
		updated[i].Name = fmt.Sprintf("updated-%d", i)
	}
	removed := []int{5, 6, 7, 8, 9, 900, 901, 902, 903, 904}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mergeIncrementalUpdate(prev, updated, removed)
	}
}

// BenchmarkSnapshotFullScanSim 模拟全量路径的 slice 构造成本。不是真实
// 浏览器扫描(那要启 chrome),只给增量路径一个相对基线:全量至少要
// 把 N 个元素重新分配/填一遍。
func BenchmarkSnapshotFullScanSim(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = makeBenchElements(1000)
	}
}

func makeBenchElements(n int) []brainElement {
	out := make([]brainElement, n)
	for i := 0; i < n; i++ {
		out[i] = brainElement{
			ID:         i + 1,
			Tag:        "button",
			Role:       "button",
			Name:       fmt.Sprintf("btn-%d", i),
			X:          i * 3,
			Y:          i * 2,
			W:          32,
			H:          32,
			InViewport: i < 100,
		}
	}
	return out
}
