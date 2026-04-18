// dispatch.go — BatchPlanner + 冲突图 + Welsh-Powell 着色分组
//
// 将一组 tool_call 按资源冲突关系分组为可并行执行的 batch。
// 核心算法：构建冲突图 → Welsh-Powell 贪心着色 → 同颜色即同 batch。
//
// 设计参考：sdk/docs/35-Dispatch-Policy-冲突图与Batch分组算法.md
package kernel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/leef-l/brain/sdk/tool"
)

// ────────────────────────── 核心类型 ──────────────────────────

// ToolCallNode 代表一个待执行的工具调用（冲突图中的节点）。
type ToolCallNode struct {
	Index    int             // 原始 tool_call 数组中的位置（0-based），用于结果回填排序
	ToolName string          // 工具名称，如 "quant.place_order"
	Args     json.RawMessage // LLM 传入的 JSON 参数（原始 bytes）
	Spec     *tool.ToolConcurrencySpec // 工具并发约束声明，可能为 nil
}

// ToolBatch 代表一组可并行执行的工具调用。
type ToolBatch struct {
	Calls  []ToolCallNode // 该 batch 内的所有工具调用
	Leases []LeaseRequest // 该 batch 需要的租约请求
}

// ErrorStrategy 定义 batch 内工具执行失败时的处理策略。
type ErrorStrategy string

const (
	// ErrorContinueBatch 失败的工具调用标记为 IsError=true，不影响其他工具。
	ErrorContinueBatch ErrorStrategy = "continue_batch"
	// ErrorFailBatch 第一个失败即取消 batch 内剩余调用。
	ErrorFailBatch ErrorStrategy = "fail_batch"
	// ErrorFailAll 任何失败都终止整个 dispatch。
	ErrorFailAll ErrorStrategy = "fail_all"
)

// BatchPlan 是 Plan() 的输出，包含分组后的执行批次和错误策略。
type BatchPlan struct {
	Batches       []ToolBatch   // 有序的 batch 列表，Batches[0] 最先执行
	ErrorStrategy ErrorStrategy // 全局错误策略
}

// BatchPlanner 将 tool_calls 按资源冲突图分组为可并行的 batch。
type BatchPlanner struct {
	LeaseManager LeaseManager // 可选，nil 时跳过 lease 相关逻辑
}

// ────────────────────────── ResourceKey 模板解析 ──────────────────────────

// templatePattern 匹配 {{field_name}} 占位符。
var templatePattern = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// resolveResourceKey 从 tool_call 的 JSON 参数和 ResourceKeyTemplate
// 生成具体的 ResourceKey。
//
// 算法：
//  1. 将 args JSON 解析为 map[string]any（只解一层）
//  2. 用正则找到所有 {{field}} 占位符
//  3. 逐一从 map 中取值替换
//  4. 字段缺失时使用 "*" 通配符（保守策略）
//  5. 模板为空时返回 Capability 作为 ResourceKey
func resolveResourceKey(tmpl string, capability string, args json.RawMessage) string {
	if tmpl == "" {
		return capability
	}

	var argMap map[string]any
	if err := json.Unmarshal(args, &argMap); err != nil {
		// args 解析失败时用通配符
		return templatePattern.ReplaceAllString(tmpl, "*")
	}

	result := templatePattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		fieldName := strings.TrimSpace(match[2 : len(match)-2])
		val, ok := argMap[fieldName]
		if !ok {
			return "*"
		}
		return fmt.Sprintf("%v", val)
	})

	return result
}

// ────────────────────────── 冲突判定 ──────────────────────────

// deriveLeaseRequest 从 ToolCallNode 推导 LeaseRequest。
// 如果节点没有 Spec，返回 nil。
func deriveLeaseRequest(node *ToolCallNode) *LeaseRequest {
	if node.Spec == nil {
		return nil
	}
	spec := node.Spec
	resourceKey := resolveResourceKey(spec.ResourceKeyTemplate, spec.Capability, node.Args)

	return &LeaseRequest{
		Capability:  spec.Capability,
		ResourceKey: resourceKey,
		AccessMode:  AccessMode(spec.AccessMode),
		Scope:       LeaseScope(spec.Scope),
	}
}

// isExclusiveMode 判断访问模式是否为排他模式。
func isExclusiveMode(mode AccessMode) bool {
	return mode == AccessExclusiveWrite || mode == AccessExclusiveSession
}

// nodesConflict 判断两个带有 LeaseRequest 的节点是否冲突。
// 冲突条件：同一 ResourceKey 且至少一方是排他模式。
func nodesConflict(aLease, bLease *LeaseRequest) bool {
	// 任一无租约约束 → 不冲突
	if aLease == nil || bLease == nil {
		return false
	}
	// ResourceKey 不同 → 不冲突
	if aLease.ResourceKey != bLease.ResourceKey {
		return false
	}
	// 通配符处理：含 "*" 时保守认为冲突（如果任一方排他）
	if strings.Contains(aLease.ResourceKey, "*") || strings.Contains(bLease.ResourceKey, "*") {
		return isExclusiveMode(aLease.AccessMode) || isExclusiveMode(bLease.AccessMode)
	}
	// 同 ResourceKey，按兼容矩阵判定
	return isExclusiveMode(aLease.AccessMode) || isExclusiveMode(bLease.AccessMode)
}

// ────────────────────────── 冲突图 ──────────────────────────

// conflictGraph 是冲突图的邻接矩阵表示。
// 节点是 tool_call，边表示两个节点不能在同一 batch 中并行执行。
type conflictGraph struct {
	n     int      // 节点数
	edges [][]bool // 邻接矩阵，对称且对角线为 false
}

// newConflictGraph 创建 n 节点的空冲突图。
func newConflictGraph(n int) *conflictGraph {
	edges := make([][]bool, n)
	for i := range edges {
		edges[i] = make([]bool, n)
	}
	return &conflictGraph{n: n, edges: edges}
}

// setEdge 在节点 i 和 j 之间添加冲突边（对称）。
func (g *conflictGraph) setEdge(i, j int) {
	g.edges[i][j] = true
	g.edges[j][i] = true
}

// hasEdge 判断节点 i 和 j 是否有冲突边。
func (g *conflictGraph) hasEdge(i, j int) bool {
	return g.edges[i][j]
}

// degree 返回节点 i 的度数（冲突边数量）。
func (g *conflictGraph) degree(i int) int {
	d := 0
	for j := 0; j < g.n; j++ {
		if g.edges[i][j] {
			d++
		}
	}
	return d
}

// ────────────────────────── Welsh-Powell 贪心着色 ──────────────────────────

// welshPowellColor 对冲突图执行 Welsh-Powell 贪心着色，返回每个节点的颜色（batch 编号）。
//
// 算法：
//  1. 按节点度数降序排列（度数高的先着色，减少颜色数）
//  2. 对每个节点，分配最小的未被其邻居使用的颜色
//  3. 同颜色 = 同 batch
func welshPowellColor(g *conflictGraph) []int {
	n := g.n
	if n == 0 {
		return nil
	}

	// 按度数降序排列节点索引
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		da := g.degree(order[a])
		db := g.degree(order[b])
		if da != db {
			return da > db
		}
		// 度数相同按 index 升序，保证稳定性
		return order[a] < order[b]
	})

	// 贪心着色
	colors := make([]int, n)
	for i := range colors {
		colors[i] = -1 // 未着色
	}

	for _, nodeIdx := range order {
		// 收集邻居已使用的颜色
		usedColors := make(map[int]bool)
		for neighbor := 0; neighbor < n; neighbor++ {
			if g.hasEdge(nodeIdx, neighbor) && colors[neighbor] >= 0 {
				usedColors[colors[neighbor]] = true
			}
		}
		// 找最小未使用颜色
		color := 0
		for usedColors[color] {
			color++
		}
		colors[nodeIdx] = color
	}

	return colors
}

// ────────────────────────── Plan 主入口 ──────────────────────────

// Plan 将一组 tool_call 按资源冲突关系分组为可并行的 batch。
//
// 核心流程：
//  1. 为每个有 ToolConcurrencySpec 的 call 推导 LeaseRequest
//  2. 构建冲突图（N×N 邻接矩阵）
//  3. Welsh-Powell 贪心着色
//  4. 无 Spec 的工具保守串行（放入独立 batch）
//  5. 结果按原 Index 排序
func (bp *BatchPlanner) Plan(calls []ToolCallNode) (*BatchPlan, error) {
	if len(calls) == 0 {
		return &BatchPlan{
			ErrorStrategy: ErrorContinueBatch,
		}, nil
	}

	n := len(calls)

	// Step 1：推导每个节点的 LeaseRequest
	leases := make([]*LeaseRequest, n)
	for i := range calls {
		leases[i] = deriveLeaseRequest(&calls[i])
	}

	// 区分有 Spec 和无 Spec 的节点
	type indexedCall struct {
		origIdx int // 在 calls 中的下标
		call    ToolCallNode
		lease   *LeaseRequest
	}

	var specCalls []indexedCall  // 有并发约束的
	var noSpecCalls []indexedCall // 无并发约束的（保守串行）

	for i := range calls {
		ic := indexedCall{origIdx: i, call: calls[i], lease: leases[i]}
		if calls[i].Spec != nil {
			specCalls = append(specCalls, ic)
		} else {
			noSpecCalls = append(noSpecCalls, ic)
		}
	}

	// Step 2 & 3：对有 Spec 的节点构建冲突图并着色
	var batchMap = make(map[int][]indexedCall) // color → calls
	maxColor := -1

	if len(specCalls) > 0 {
		g := newConflictGraph(len(specCalls))
		for i := 0; i < len(specCalls); i++ {
			for j := i + 1; j < len(specCalls); j++ {
				if nodesConflict(specCalls[i].lease, specCalls[j].lease) {
					g.setEdge(i, j)
				}
			}
		}

		colors := welshPowellColor(g)
		for i, c := range colors {
			batchMap[c] = append(batchMap[c], specCalls[i])
			if c > maxColor {
				maxColor = c
			}
		}
	}

	// Step 4：构建 ToolBatch 列表
	var batches []ToolBatch

	// 先添加有 Spec 的 batch（按颜色编号顺序）
	for c := 0; c <= maxColor; c++ {
		ics, ok := batchMap[c]
		if !ok {
			continue
		}
		batch := ToolBatch{}
		for _, ic := range ics {
			batch.Calls = append(batch.Calls, ic.call)
			if ic.lease != nil {
				batch.Leases = append(batch.Leases, *ic.lease)
			}
		}
		// batch 内按原始 Index 排序
		sort.Slice(batch.Calls, func(a, b int) bool {
			return batch.Calls[a].Index < batch.Calls[b].Index
		})
		batches = append(batches, batch)
	}

	// Step 5：无 Spec 的工具每个单独一个 batch（保守串行）
	for _, ic := range noSpecCalls {
		batches = append(batches, ToolBatch{
			Calls: []ToolCallNode{ic.call},
		})
	}

	return &BatchPlan{
		Batches:       batches,
		ErrorStrategy: ErrorContinueBatch,
	}, nil
}
