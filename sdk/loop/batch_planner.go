// batch_planner.go — BatchPlanner 接口定义
//
// 为避免 loop ↔ kernel 循环依赖，loop 包定义轻量接口，
// kernel.BatchPlanner 通过适配器实现此接口。
package loop

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/sdk/tool"
)

// ToolCallNode 代表一个待执行的工具调用，是 BatchPlanner 的输入单元。
// 与 kernel.ToolCallNode 字段对齐，但定义在 loop 包避免循环依赖。
type ToolCallNode struct {
	Index    int                       // 原始 tool_call 数组中的位置（0-based）
	ToolName string                    // 工具名称，如 "quant.place_order"
	Args     json.RawMessage           // LLM 传入的 JSON 参数
	Spec     *tool.ToolConcurrencySpec // 工具并发约束声明，可能为 nil
}

// LeaseToken 代表一个已获取的资源租约。
// 对应 kernel.Lease 接口，但定义在 loop 包避免循环依赖。
type LeaseToken interface {
	// ID 返回租约的唯一标识。
	ID() string
	// Release 释放租约。
	Release()
}

// BatchLeaseRequest 描述一个 batch 需要的资源租约请求。
// 对应 kernel.LeaseRequest，但定义在 loop 包避免循环依赖。
type BatchLeaseRequest struct {
	Capability  string // 能力名称，如 "file-write"
	ResourceKey string // 资源标识，如 "/tmp/foo.txt"
	AccessMode  string // 访问模式，如 "exclusive-write"
	Scope       string // 生命周期范围，如 "turn"
}

// ToolBatch 代表一组可并行执行的工具调用。
type ToolBatch struct {
	Calls  []ToolCallNode     // 该 batch 内的所有工具调用
	Leases []BatchLeaseRequest // 该 batch 需要的租约请求
}

// BatchPlan 是 ToolBatchPlanner.Plan() 的输出。
type BatchPlan struct {
	Batches []ToolBatch // 有序的 batch 列表，Batches[0] 最先执行
}

// ResourceLocker 定义资源锁获取/释放接口。
// 由 kernel.LeaseManager 通过适配器实现，避免 loop→kernel 循环依赖。
type ResourceLocker interface {
	// AcquireSet 原子获取一组资源租约。全部成功才返回；
	// ctx 取消或超时时返回 error。
	AcquireSet(ctx context.Context, reqs []BatchLeaseRequest) ([]LeaseToken, error)

	// ReleaseAll 释放一组租约。
	ReleaseAll(tokens []LeaseToken)
}

// ToolBatchPlanner 将 tool_calls 按资源冲突关系分组为可并行的 batch。
// kernel.BatchPlanner 的适配器实现此接口。
type ToolBatchPlanner interface {
	Plan(calls []ToolCallNode) (*BatchPlan, error)

	// ResourceLocker 返回关联的 ResourceLocker。
	// 如果返回 nil，batch 执行时不进行资源锁保护。
	ResourceLocker() ResourceLocker
}
