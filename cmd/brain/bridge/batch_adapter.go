package bridge

import (
	"context"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/loop"
)

// BatchPlannerAdapter 将 kernel.BatchPlanner 适配为 loop.ToolBatchPlanner 接口，
// 同时通过 leaseLockerAdapter 实现 loop.ResourceLocker，打通租约获取/释放路径。
type BatchPlannerAdapter struct {
	inner        *kernel.BatchPlanner
	locker       *leaseLockerAdapter // 非 nil 当 LeaseManager 存在时
}

func NewBatchPlannerAdapter(leaseManager kernel.LeaseManager) *BatchPlannerAdapter {
	a := &BatchPlannerAdapter{
		inner: &kernel.BatchPlanner{LeaseManager: leaseManager},
	}
	if leaseManager != nil {
		a.locker = &leaseLockerAdapter{mgr: leaseManager}
	}
	return a
}

func (a *BatchPlannerAdapter) Plan(calls []loop.ToolCallNode) (*loop.BatchPlan, error) {
	kCalls := make([]kernel.ToolCallNode, len(calls))
	for i, c := range calls {
		kCalls[i] = kernel.ToolCallNode{
			Index:    c.Index,
			ToolName: c.ToolName,
			Args:     c.Args,
			Spec:     c.Spec,
		}
	}

	kPlan, err := a.inner.Plan(kCalls)
	if err != nil {
		return nil, err
	}

	batches := make([]loop.ToolBatch, len(kPlan.Batches))
	for i, kb := range kPlan.Batches {
		lCalls := make([]loop.ToolCallNode, len(kb.Calls))
		for j, c := range kb.Calls {
			lCalls[j] = loop.ToolCallNode{
				Index:    c.Index,
				ToolName: c.ToolName,
				Args:     c.Args,
				Spec:     c.Spec,
			}
		}
		// 转换 kernel.LeaseRequest → loop.BatchLeaseRequest
		lLeases := make([]loop.BatchLeaseRequest, len(kb.Leases))
		for j, lr := range kb.Leases {
			lLeases[j] = loop.BatchLeaseRequest{
				Capability:  lr.Capability,
				ResourceKey: lr.ResourceKey,
				AccessMode:  string(lr.AccessMode),
				Scope:       string(lr.Scope),
			}
		}
		batches[i] = loop.ToolBatch{Calls: lCalls, Leases: lLeases}
	}

	return &loop.BatchPlan{Batches: batches}, nil
}

// ResourceLocker 返回关联的 ResourceLocker 适配器。
// 当 LeaseManager 为 nil 时返回 nil。
func (a *BatchPlannerAdapter) ResourceLocker() loop.ResourceLocker {
	if a.locker == nil {
		return nil
	}
	return a.locker
}

// ────────────────────────── leaseLockerAdapter ──────────────────────────

// leaseLockerAdapter 将 kernel.LeaseManager 适配为 loop.ResourceLocker 接口。
type leaseLockerAdapter struct {
	mgr kernel.LeaseManager
}

func (l *leaseLockerAdapter) AcquireSet(ctx context.Context, reqs []loop.BatchLeaseRequest) ([]loop.LeaseToken, error) {
	kReqs := make([]kernel.LeaseRequest, len(reqs))
	for i, r := range reqs {
		kReqs[i] = kernel.LeaseRequest{
			Capability:  r.Capability,
			ResourceKey: r.ResourceKey,
			AccessMode:  kernel.AccessMode(r.AccessMode),
			Scope:       kernel.LeaseScope(r.Scope),
		}
	}

	leases, err := l.mgr.AcquireSet(ctx, kReqs)
	if err != nil {
		return nil, err
	}

	tokens := make([]loop.LeaseToken, len(leases))
	for i, lease := range leases {
		tokens[i] = lease // kernel.Lease 已实现 ID() + Release()
	}
	return tokens, nil
}

func (l *leaseLockerAdapter) ReleaseAll(tokens []loop.LeaseToken) {
	leases := make([]kernel.Lease, len(tokens))
	for i, t := range tokens {
		leases[i] = t.(kernel.Lease)
	}
	l.mgr.ReleaseAll(leases)
}
