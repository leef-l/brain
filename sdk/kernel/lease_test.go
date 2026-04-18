package kernel

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSharedReadNonConflict 验证并发 SharedRead 不冲突。
func TestSharedReadNonConflict(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx := context.Background()

	// 多个 SharedRead 同时获取同一资源
	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leases, err := mgr.AcquireSet(ctx, []LeaseRequest{
				{Capability: "file", ResourceKey: "/data", AccessMode: AccessSharedRead, Scope: ScopeTurn, HolderID: "brain-1"},
			})
			if err != nil {
				errs <- err
				return
			}
			// 持有一小段时间后释放
			time.Sleep(5 * time.Millisecond)
			mgr.ReleaseAll(leases)
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("并发 SharedRead 不应冲突: %v", err)
	}
}

// TestExclusiveWriteSameResourceQueues 验证 ExclusiveWrite 相同 ResourceKey 排队。
func TestExclusiveWriteSameResourceQueues(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 先获取一个 ExclusiveWrite
	leases1, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-1"},
	})
	if err != nil {
		t.Fatalf("首次获取 ExclusiveWrite 失败: %v", err)
	}

	// 第二个获取者应该被阻塞，直到第一个释放
	acquired := make(chan struct{})
	go func() {
		leases2, err := mgr.AcquireSet(ctx, []LeaseRequest{
			{Capability: "file", ResourceKey: "/data", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-2"},
		})
		if err != nil {
			t.Errorf("第二次获取 ExclusiveWrite 失败: %v", err)
			return
		}
		close(acquired)
		mgr.ReleaseAll(leases2)
	}()

	// 确认第二个获取者尚未获得
	select {
	case <-acquired:
		t.Fatal("第二个 ExclusiveWrite 不应在第一个释放前获得")
	case <-time.After(50 * time.Millisecond):
		// 预期：等待中
	}

	// 释放第一个
	mgr.ReleaseAll(leases1)

	// 第二个应该能获取
	select {
	case <-acquired:
		// 成功
	case <-time.After(2 * time.Second):
		t.Fatal("释放后第二个 ExclusiveWrite 应能获取")
	}
}

// TestExclusiveWriteDifferentResourceNoConflict 验证不同 ResourceKey 的 ExclusiveWrite 不冲突。
func TestExclusiveWriteDifferentResourceNoConflict(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx := context.Background()

	leases1, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data-a", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-1"},
	})
	if err != nil {
		t.Fatalf("获取 /data-a 失败: %v", err)
	}
	defer mgr.ReleaseAll(leases1)

	// 不同 ResourceKey 应立即成功
	leases2, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data-b", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-2"},
	})
	if err != nil {
		t.Fatalf("获取 /data-b 失败（不同资源不应冲突）: %v", err)
	}
	mgr.ReleaseAll(leases2)
}

// TestAcquireSetRollbackOnConflict 验证 AcquireSet 部分冲突时回滚已获取的租约。
func TestAcquireSetRollbackOnConflict(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 先锁定 /data-b
	leaseB, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data-b", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-x"},
	})
	if err != nil {
		t.Fatalf("锁定 /data-b 失败: %v", err)
	}

	// 尝试同时获取 /data-a 和 /data-b（/data-b 会冲突）
	// 使用短超时确保不会一直阻塞
	shortCtx, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()

	_, err = mgr.AcquireSet(shortCtx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data-a", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-2"},
		{Capability: "file", ResourceKey: "/data-b", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-2"},
	})
	if err == nil {
		t.Fatal("应失败（/data-b 被锁定）")
	}

	// 验证 /data-a 已被回滚（应该可以立即获取）
	mgr.ReleaseAll(leaseB)

	leaseA, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/data-a", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "brain-3"},
	})
	if err != nil {
		t.Fatalf("/data-a 应已回滚可被获取: %v", err)
	}
	mgr.ReleaseAll(leaseA)
}

// TestCanonicalOrderPreventsDeadlock 验证 canonical order 防止排列差异导致问题。
// 两个 goroutine 以不同顺序请求同一组资源，不应死锁。
func TestCanonicalOrderPreventsDeadlock(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	// goroutine 1：请求顺序 A, B
	wg.Add(1)
	go func() {
		defer wg.Done()
		leases, err := mgr.AcquireSet(ctx, []LeaseRequest{
			{Capability: "file", ResourceKey: "/a", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "g1"},
			{Capability: "file", ResourceKey: "/b", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "g1"},
		})
		if err != nil {
			errs <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		mgr.ReleaseAll(leases)
	}()

	// goroutine 2：请求顺序 B, A（反序）
	wg.Add(1)
	go func() {
		defer wg.Done()
		leases, err := mgr.AcquireSet(ctx, []LeaseRequest{
			{Capability: "file", ResourceKey: "/b", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "g2"},
			{Capability: "file", ResourceKey: "/a", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "g2"},
		})
		if err != nil {
			errs <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		mgr.ReleaseAll(leases)
	}()

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("canonical order 应防止死锁: %v", err)
	}
}

// TestAcquireTimeout 验证 ctx 取消时返回 ErrAcquireTimeout。
func TestAcquireTimeout(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx := context.Background()

	// 先锁定资源
	leases, err := mgr.AcquireSet(ctx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/locked", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "holder"},
	})
	if err != nil {
		t.Fatalf("初始锁定失败: %v", err)
	}
	defer mgr.ReleaseAll(leases)

	// 带短超时的 ctx 尝试获取
	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = mgr.AcquireSet(shortCtx, []LeaseRequest{
		{Capability: "file", ResourceKey: "/locked", AccessMode: AccessExclusiveWrite, Scope: ScopeTurn, HolderID: "waiter"},
	})
	if err != ErrAcquireTimeout {
		t.Fatalf("期望 ErrAcquireTimeout，实际: %v", err)
	}
}

// TestCompatibilityMatrix 验证兼容性矩阵的正确性。
func TestCompatibilityMatrix(t *testing.T) {
	cases := []struct {
		a, b   AccessMode
		expect bool
	}{
		{AccessSharedRead, AccessSharedRead, true},
		{AccessSharedRead, AccessSharedWriteAppend, true},
		{AccessSharedWriteAppend, AccessSharedWriteAppend, true},
		{AccessSharedWriteAppend, AccessSharedRead, true},
		{AccessExclusiveWrite, AccessSharedRead, false},
		{AccessExclusiveWrite, AccessSharedWriteAppend, false},
		{AccessExclusiveWrite, AccessExclusiveWrite, false},
		{AccessExclusiveSession, AccessSharedRead, false},
		{AccessExclusiveSession, AccessExclusiveWrite, false},
		{AccessExclusiveSession, AccessExclusiveSession, false},
		{AccessSharedRead, AccessExclusiveWrite, false},
		{AccessSharedRead, AccessExclusiveSession, false},
	}

	for _, tc := range cases {
		got := compatible(tc.a, tc.b)
		if got != tc.expect {
			t.Errorf("compatible(%s, %s) = %v，期望 %v", tc.a, tc.b, got, tc.expect)
		}
	}
}

// TestLeaseIDUniqueness 验证租约 ID 单调递增且唯一。
func TestLeaseIDUniqueness(t *testing.T) {
	mgr := NewMemLeaseManager()
	ctx := context.Background()

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		leases, err := mgr.AcquireSet(ctx, []LeaseRequest{
			{Capability: "cap", ResourceKey: "res", AccessMode: AccessSharedRead, Scope: ScopeTurn, HolderID: "h"},
		})
		if err != nil {
			t.Fatalf("获取失败: %v", err)
		}
		id := leases[0].ID()
		if ids[id] {
			t.Fatalf("重复 ID: %s", id)
		}
		ids[id] = true
		mgr.ReleaseAll(leases)
	}
}
