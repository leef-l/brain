package kernel

import (
	"encoding/json"
	"testing"

	"github.com/leef-l/brain/sdk/tool"
)

// ────────────────────────── 辅助函数 ──────────────────────────

// makeCall 创建一个带并发约束的 ToolCallNode。
func makeCall(index int, name string, accessMode string, resourceKeyTmpl string, args string) ToolCallNode {
	return ToolCallNode{
		Index:    index,
		ToolName: name,
		Args:     json.RawMessage(args),
		Spec: &tool.ToolConcurrencySpec{
			Capability:          "test.cap",
			ResourceKeyTemplate: resourceKeyTmpl,
			AccessMode:          accessMode,
			Scope:               "turn",
		},
	}
}

// makeCallNoSpec 创建一个无并发约束的 ToolCallNode。
func makeCallNoSpec(index int, name string) ToolCallNode {
	return ToolCallNode{
		Index:    index,
		ToolName: name,
		Args:     json.RawMessage(`{}`),
		Spec:     nil,
	}
}

// ────────────────────────── 测试用例 ──────────────────────────

// TestPlan_AllNoConflict 4 个无冲突的 SharedRead call 应该归入 1 个 batch。
func TestPlan_AllNoConflict(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "read_a", "shared-read", "res:{{id}}", `{"id":"1"}`),
		makeCall(1, "read_b", "shared-read", "res:{{id}}", `{"id":"2"}`),
		makeCall(2, "read_c", "shared-read", "res:{{id}}", `{"id":"3"}`),
		makeCall(3, "read_d", "shared-read", "res:{{id}}", `{"id":"4"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	// 4 个不同 ResourceKey 的 SharedRead，全部无冲突 → 1 个 batch
	if len(plan.Batches) != 1 {
		t.Fatalf("期望 1 个 batch，实际 %d 个", len(plan.Batches))
	}
	if len(plan.Batches[0].Calls) != 4 {
		t.Fatalf("期望 batch 内 4 个 call，实际 %d 个", len(plan.Batches[0].Calls))
	}
}

// TestPlan_SharedReadSameKey 同一 ResourceKey 的 SharedRead 不冲突 → 1 个 batch。
func TestPlan_SharedReadSameKey(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "read_a", "shared-read", "res:{{id}}", `{"id":"same"}`),
		makeCall(1, "read_b", "shared-read", "res:{{id}}", `{"id":"same"}`),
		makeCall(2, "read_c", "shared-read", "res:{{id}}", `{"id":"same"}`),
		makeCall(3, "read_d", "shared-read", "res:{{id}}", `{"id":"same"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	if len(plan.Batches) != 1 {
		t.Fatalf("期望 1 个 batch，实际 %d 个", len(plan.Batches))
	}
}

// TestPlan_TwoExclusiveSameKey 2 个 ExclusiveWrite 操作同一 ResourceKey → 2 个 batch。
func TestPlan_TwoExclusiveSameKey(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "write_a", "exclusive-write", "account:{{acct}}", `{"acct":"main"}`),
		makeCall(1, "write_b", "exclusive-write", "account:{{acct}}", `{"acct":"main"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	if len(plan.Batches) != 2 {
		t.Fatalf("期望 2 个 batch，实际 %d 个", len(plan.Batches))
	}
	// 每个 batch 只有 1 个 call
	for i, b := range plan.Batches {
		if len(b.Calls) != 1 {
			t.Fatalf("batch[%d] 期望 1 个 call，实际 %d 个", i, len(b.Calls))
		}
	}
}

// TestPlan_MixedSharedAndExclusive 3 个 SharedRead + 1 个 ExclusiveWrite
// 操作同一 ResourceKey → 2 个 batch。
func TestPlan_MixedSharedAndExclusive(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "read_1", "shared-read", "symbol:{{sym}}", `{"sym":"BTC"}`),
		makeCall(1, "read_2", "shared-read", "symbol:{{sym}}", `{"sym":"BTC"}`),
		makeCall(2, "read_3", "shared-read", "symbol:{{sym}}", `{"sym":"BTC"}`),
		makeCall(3, "write_1", "exclusive-write", "symbol:{{sym}}", `{"sym":"BTC"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	if len(plan.Batches) != 2 {
		t.Fatalf("期望 2 个 batch，实际 %d 个", len(plan.Batches))
	}

	// ExclusiveWrite 度数为 3（与每个 SharedRead 冲突），先着色为 color 0 → 独占一个 batch
	// 3 个 SharedRead 互不冲突，但都与 ExclusiveWrite 冲突 → color 1 → 一个 batch
	// 验证：一个 batch 有 1 个 call，另一个有 3 个 call
	sizes := map[int]bool{}
	for _, b := range plan.Batches {
		sizes[len(b.Calls)] = true
	}
	if !sizes[1] || !sizes[3] {
		t.Fatalf("期望 batch 大小为 {1, 3}，实际为 %v", sizes)
	}
}

// TestPlan_NoSpecCallsSerial 无 Spec 的 call 每个单独一个 batch（保守串行）。
func TestPlan_NoSpecCallsSerial(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCallNoSpec(0, "unknown_tool_a"),
		makeCallNoSpec(1, "unknown_tool_b"),
		makeCallNoSpec(2, "unknown_tool_c"),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	// 3 个无 Spec 的 call → 每个单独一个 batch → 3 个 batch
	if len(plan.Batches) != 3 {
		t.Fatalf("期望 3 个 batch，实际 %d 个", len(plan.Batches))
	}
	for i, b := range plan.Batches {
		if len(b.Calls) != 1 {
			t.Fatalf("batch[%d] 期望 1 个 call，实际 %d 个", i, len(b.Calls))
		}
	}
}

// TestPlan_IndexPreserved 验证结果按原始 Index 排序。
func TestPlan_IndexPreserved(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(3, "read_d", "shared-read", "res:{{id}}", `{"id":"1"}`),
		makeCall(1, "read_b", "shared-read", "res:{{id}}", `{"id":"2"}`),
		makeCall(0, "read_a", "shared-read", "res:{{id}}", `{"id":"3"}`),
		makeCall(2, "read_c", "shared-read", "res:{{id}}", `{"id":"4"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	if len(plan.Batches) != 1 {
		t.Fatalf("期望 1 个 batch，实际 %d 个", len(plan.Batches))
	}

	// 验证 batch 内的 call 按 Index 升序排列
	batch := plan.Batches[0]
	for i := 1; i < len(batch.Calls); i++ {
		if batch.Calls[i].Index < batch.Calls[i-1].Index {
			t.Fatalf("batch 内 call 未按 Index 排序: [%d].Index=%d < [%d].Index=%d",
				i, batch.Calls[i].Index, i-1, batch.Calls[i-1].Index)
		}
	}
}

// TestPlan_EmptyInput 空输入 → 返回空 BatchPlan。
func TestPlan_EmptyInput(t *testing.T) {
	bp := &BatchPlanner{}
	plan, err := bp.Plan(nil)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}
	if len(plan.Batches) != 0 {
		t.Fatalf("期望 0 个 batch，实际 %d 个", len(plan.Batches))
	}
}

// TestPlan_MixedSpecAndNoSpec 有 Spec 和无 Spec 混合场景。
func TestPlan_MixedSpecAndNoSpec(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "read_1", "shared-read", "res:{{id}}", `{"id":"1"}`),
		makeCallNoSpec(1, "unknown"),
		makeCall(2, "read_2", "shared-read", "res:{{id}}", `{"id":"2"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	// 2 个有 Spec 的 SharedRead（不同 key）→ 1 batch + 1 个无 Spec → 1 batch = 共 2 batch
	if len(plan.Batches) != 2 {
		t.Fatalf("期望 2 个 batch，实际 %d 个", len(plan.Batches))
	}
}

// TestPlan_DifferentKeyExclusive 不同 ResourceKey 的 ExclusiveWrite 不冲突 → 1 batch。
func TestPlan_DifferentKeyExclusive(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "write_a", "exclusive-write", "account:{{acct}}", `{"acct":"main"}`),
		makeCall(1, "write_b", "exclusive-write", "account:{{acct}}", `{"acct":"test"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	// 不同 ResourceKey 的 ExclusiveWrite 不冲突
	if len(plan.Batches) != 1 {
		t.Fatalf("期望 1 个 batch，实际 %d 个", len(plan.Batches))
	}
}

// TestPlan_LeaseRequests 验证 batch 中的 Leases 被正确填充。
func TestPlan_LeaseRequests(t *testing.T) {
	bp := &BatchPlanner{}
	calls := []ToolCallNode{
		makeCall(0, "write_a", "exclusive-write", "account:{{acct}}", `{"acct":"main"}`),
	}

	plan, err := bp.Plan(calls)
	if err != nil {
		t.Fatalf("Plan 返回错误: %v", err)
	}

	if len(plan.Batches) != 1 {
		t.Fatalf("期望 1 个 batch，实际 %d 个", len(plan.Batches))
	}
	if len(plan.Batches[0].Leases) != 1 {
		t.Fatalf("期望 1 个 lease，实际 %d 个", len(plan.Batches[0].Leases))
	}

	lease := plan.Batches[0].Leases[0]
	if lease.ResourceKey != "account:main" {
		t.Fatalf("期望 ResourceKey='account:main'，实际='%s'", lease.ResourceKey)
	}
	if lease.AccessMode != AccessExclusiveWrite {
		t.Fatalf("期望 AccessMode='exclusive-write'，实际='%s'", lease.AccessMode)
	}
}

// ────────────────────────── 内部函数测试 ──────────────────────────

// TestResolveResourceKey 测试模板解析。
func TestResolveResourceKey(t *testing.T) {
	tests := []struct {
		name     string
		tmpl     string
		cap      string
		args     string
		expected string
	}{
		{
			name:     "正常替换",
			tmpl:     "account:{{acct}}",
			cap:      "test",
			args:     `{"acct":"main"}`,
			expected: "account:main",
		},
		{
			name:     "空模板返回 capability",
			tmpl:     "",
			cap:      "test.cap",
			args:     `{}`,
			expected: "test.cap",
		},
		{
			name:     "缺失字段用通配符",
			tmpl:     "account:{{acct}}",
			cap:      "test",
			args:     `{}`,
			expected: "account:*",
		},
		{
			name:     "无效 JSON 用通配符",
			tmpl:     "account:{{acct}}",
			cap:      "test",
			args:     `not json`,
			expected: "account:*",
		},
		{
			name:     "多字段替换",
			tmpl:     "{{exchange}}:{{symbol}}",
			cap:      "test",
			args:     `{"exchange":"binance","symbol":"BTC-USDT"}`,
			expected: "binance:BTC-USDT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveResourceKey(tt.tmpl, tt.cap, json.RawMessage(tt.args))
			if result != tt.expected {
				t.Fatalf("期望 '%s'，实际 '%s'", tt.expected, result)
			}
		})
	}
}

// TestWelshPowellColor 测试着色算法基本正确性。
func TestWelshPowellColor(t *testing.T) {
	// 完全图 K3 → 需要 3 种颜色
	g := newConflictGraph(3)
	g.setEdge(0, 1)
	g.setEdge(0, 2)
	g.setEdge(1, 2)

	colors := welshPowellColor(g)
	if len(colors) != 3 {
		t.Fatalf("期望 3 个颜色，实际 %d 个", len(colors))
	}

	// 验证每条边两端颜色不同
	if colors[0] == colors[1] {
		t.Fatal("节点 0 和 1 颜色相同但有冲突边")
	}
	if colors[0] == colors[2] {
		t.Fatal("节点 0 和 2 颜色相同但有冲突边")
	}
	if colors[1] == colors[2] {
		t.Fatal("节点 1 和 2 颜色相同但有冲突边")
	}

	// 验证使用了 3 种不同颜色
	colorSet := map[int]bool{}
	for _, c := range colors {
		colorSet[c] = true
	}
	if len(colorSet) != 3 {
		t.Fatalf("期望 3 种颜色，实际 %d 种", len(colorSet))
	}
}

// TestWelshPowellColor_NoEdges 无冲突边 → 全部同色。
func TestWelshPowellColor_NoEdges(t *testing.T) {
	g := newConflictGraph(5)
	colors := welshPowellColor(g)

	for i, c := range colors {
		if c != 0 {
			t.Fatalf("节点 %d 期望颜色 0，实际 %d", i, c)
		}
	}
}
