// Health checkers — MACCS 6.1：把关键运行时组件适配到 HealthChecker 接口
// 暴露给 GET /v1/health。
//
// 设计原则：
//   - 只读 + 快速：Check 不应阻塞，超过 100ms 视为 degraded
//   - 不触发副作用：不重启、不 reset、不清理；自愈走 HealthManager.TriggerHeal
//   - 失败原因写入 Message，便于运维诊断

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/kernel"
)

// brainPoolHealthChecker 检查 ProcessBrainPool 是否仍可用。
// 判定：AvailableKinds 为空 → down；< 期望 kind 数 → degraded；齐全 → ok。
type brainPoolHealthChecker struct {
	pool *kernel.ProcessBrainPool
}

func (c *brainPoolHealthChecker) Name() string { return "brain_pool" }

func (c *brainPoolHealthChecker) Check(ctx context.Context) kernel.ComponentHealth {
	start := time.Now()
	if c.pool == nil {
		return kernel.ComponentHealth{
			Component: c.Name(),
			Status:    kernel.HealthDown,
			Message:   "brain pool is nil",
			Latency:   time.Since(start),
		}
	}
	kinds := c.pool.AvailableKinds()
	status := kernel.HealthOK
	msg := fmt.Sprintf("%d kind(s) available", len(kinds))
	if len(kinds) == 0 {
		status = kernel.HealthDown
		msg = "no brain kinds available"
	}
	return kernel.ComponentHealth{
		Component: c.Name(),
		Status:    status,
		Message:   msg,
		Latency:   time.Since(start),
		Metadata:  map[string]string{"kinds_count": fmt.Sprintf("%d", len(kinds))},
	}
}

// leaseManagerHealthChecker 检查 LeaseManager 是否能受理一次空请求。
// 判定：AcquireSet(空) 不应阻塞 / 报错；任何错误均视为 down。
type leaseManagerHealthChecker struct {
	lm kernel.LeaseManager
}

func (c *leaseManagerHealthChecker) Name() string { return "lease_manager" }

func (c *leaseManagerHealthChecker) Check(ctx context.Context) kernel.ComponentHealth {
	start := time.Now()
	if c.lm == nil {
		return kernel.ComponentHealth{
			Component: c.Name(),
			Status:    kernel.HealthDown,
			Message:   "lease manager is nil",
			Latency:   time.Since(start),
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	leases, err := c.lm.AcquireSet(probeCtx, nil)
	if err != nil {
		return kernel.ComponentHealth{
			Component: c.Name(),
			Status:    kernel.HealthDown,
			Message:   fmt.Sprintf("probe failed: %v", err),
			Latency:   time.Since(start),
		}
	}
	c.lm.ReleaseAll(leases)
	return kernel.ComponentHealth{
		Component: c.Name(),
		Status:    kernel.HealthOK,
		Message:   "probe ok",
		Latency:   time.Since(start),
	}
}
