// resource_tracker.go — MACCS Wave 4 资源访问追踪器
// 追踪任务访问的资源（文件、端口、API 等），提供轻量级资源锁用于冲突仲裁。
package kernel

import (
	"fmt"
	"sync"
	"time"
)

// ResourceType 描述被追踪资源的类型。
type ResourceType string

const (
	ResourceFile    ResourceType = "file"
	ResourcePort    ResourceType = "port"
	ResourceAPI     ResourceType = "api"
	ResourceDB      ResourceType = "database"
	ResourceEnv     ResourceType = "env_var"
	ResourceProcess ResourceType = "process"
)

// ResourceAccessMode 描述对资源的访问方式（区别于 lease.go 的 AccessMode）。
type ResourceAccessMode string

const (
	ResAccessRead    ResourceAccessMode = "read"
	ResAccessWrite   ResourceAccessMode = "write"
	ResAccessDelete  ResourceAccessMode = "delete"
	ResAccessExecute ResourceAccessMode = "execute"
)

// ResourceAccess 记录单次资源访问事件。
type ResourceAccess struct {
	ResourceID   string             `json:"resource_id"`
	ResourceType ResourceType       `json:"resource_type"`
	ResourcePath string             `json:"resource_path"`
	TaskID       string             `json:"task_id"`
	BrainKind    string             `json:"brain_kind"`
	Mode         ResourceAccessMode `json:"mode"`
	Timestamp    time.Time          `json:"timestamp"`
	Released     bool               `json:"released"`
}

// ResourceLock 描述某个资源上的锁。write/delete/execute 为独占，read 为共享。
type ResourceLock struct {
	ResourcePath string             `json:"resource_path"`
	HolderTaskID string             `json:"holder_task_id"`
	Mode         ResourceAccessMode `json:"mode"`
	AcquiredAt   time.Time          `json:"acquired_at"`
	ExpiresAt    *time.Time         `json:"expires_at,omitempty"`
}

// ResourceTracker 追踪任务对资源的访问，并提供资源锁机制用于冲突检测。
type ResourceTracker struct {
	mu            sync.RWMutex
	accesses      []ResourceAccess            // 访问历史
	locks         map[string][]*ResourceLock   // resourcePath -> 持有的锁列表
	taskResources map[string]map[string]struct{} // taskID -> set(resourcePath)
}

// NewResourceTracker 创建一个新的资源追踪器。
func NewResourceTracker() *ResourceTracker {
	return &ResourceTracker{
		locks:         make(map[string][]*ResourceLock),
		taskResources: make(map[string]map[string]struct{}),
	}
}

// RecordAccess 记录一次资源访问事件。
func (rt *ResourceTracker) RecordAccess(access ResourceAccess) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.accesses = append(rt.accesses, access)

	// 更新 taskResources 索引
	if _, ok := rt.taskResources[access.TaskID]; !ok {
		rt.taskResources[access.TaskID] = make(map[string]struct{})
	}
	rt.taskResources[access.TaskID][access.ResourcePath] = struct{}{}
}

// AcquireLock 获取资源锁。write/delete/execute 独占，read 共享。冲突时返回 error。
func (rt *ResourceTracker) AcquireLock(resourcePath, taskID string, mode ResourceAccessMode) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if existing := rt.locks[resourcePath]; mode == ResAccessRead {
		for _, lk := range existing {
			if lk.Mode != ResAccessRead {
				return fmt.Errorf("resource_tracker: read lock on %q conflicts with task %q (%s)", resourcePath, lk.HolderTaskID, lk.Mode)
			}
		}
	} else if len(existing) > 0 {
		return fmt.Errorf("resource_tracker: %s lock on %q conflicts with %d existing lock(s)", mode, resourcePath, len(existing))
	}

	lock := &ResourceLock{
		ResourcePath: resourcePath,
		HolderTaskID: taskID,
		Mode:         mode,
		AcquiredAt:   time.Now(),
	}
	rt.locks[resourcePath] = append(rt.locks[resourcePath], lock)

	// 更新 taskResources 索引
	if _, ok := rt.taskResources[taskID]; !ok {
		rt.taskResources[taskID] = make(map[string]struct{})
	}
	rt.taskResources[taskID][resourcePath] = struct{}{}

	return nil
}

// ReleaseLock 释放指定任务在指定资源上的锁。
func (rt *ResourceTracker) ReleaseLock(resourcePath, taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	rt.releaseLockLocked(resourcePath, taskID)
}

func (rt *ResourceTracker) releaseLockLocked(resourcePath, taskID string) {
	filtered := make([]*ResourceLock, 0, len(rt.locks[resourcePath]))
	for _, lk := range rt.locks[resourcePath] {
		if lk.HolderTaskID != taskID {
			filtered = append(filtered, lk)
		}
	}
	if len(filtered) == 0 {
		delete(rt.locks, resourcePath)
	} else {
		rt.locks[resourcePath] = filtered
	}
}

// ReleaseAllForTask 释放某任务持有的所有锁。
func (rt *ResourceTracker) ReleaseAllForTask(taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	paths, ok := rt.taskResources[taskID]
	if !ok {
		return
	}
	for p := range paths {
		rt.releaseLockLocked(p, taskID)
	}
	delete(rt.taskResources, taskID)
}

// GetTaskResources 返回某任务曾访问过的所有资源路径。
func (rt *ResourceTracker) GetTaskResources(taskID string) []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	paths := rt.taskResources[taskID]
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	return out
}

// GetResourceHolders 返回持有指定资源锁的所有任务 ID。
func (rt *ResourceTracker) GetResourceHolders(resourcePath string) []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	locks := rt.locks[resourcePath]
	seen := make(map[string]struct{}, len(locks))
	for _, lk := range locks {
		seen[lk.HolderTaskID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// IsLocked 返回指定资源是否被锁定。
func (rt *ResourceTracker) IsLocked(resourcePath string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.locks[resourcePath]) > 0
}

// GetLock 返回指定资源上的第一个锁信息，无锁时返回 nil。
func (rt *ResourceTracker) GetLock(resourcePath string) *ResourceLock {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	locks := rt.locks[resourcePath]
	if len(locks) == 0 {
		return nil
	}
	// 返回副本，避免外部修改
	cp := *locks[0]
	return &cp
}

// GetAccessHistory 返回指定资源的所有访问记录。
func (rt *ResourceTracker) GetAccessHistory(resourcePath string) []ResourceAccess {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var out []ResourceAccess
	for _, a := range rt.accesses {
		if a.ResourcePath == resourcePath {
			out = append(out, a)
		}
	}
	return out
}

// GetConflictingTasks 返回对指定资源以给定模式访问时会产生冲突的任务 ID 列表。
func (rt *ResourceTracker) GetConflictingTasks(resourcePath string, mode ResourceAccessMode) []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	locks := rt.locks[resourcePath]
	seen := make(map[string]struct{})

	for _, lk := range locks {
		if mode == ResAccessRead && lk.Mode == ResAccessRead {
			continue // read vs read 不冲突
		}
		seen[lk.HolderTaskID] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// CleanExpiredLocks 清理所有已过期的锁。
func (rt *ResourceTracker) CleanExpiredLocks() {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	for path, locks := range rt.locks {
		filtered := locks[:0]
		for _, lk := range locks {
			if lk.ExpiresAt != nil && lk.ExpiresAt.Before(now) {
				continue // 过期，跳过
			}
			filtered = append(filtered, lk)
		}
		if len(filtered) == 0 {
			delete(rt.locks, path)
		} else {
			rt.locks[path] = filtered
		}
	}
}

// ResourceTrackerSnapshot 描述资源追踪器的当前状态快照。
type ResourceTrackerSnapshot struct {
	TotalAccesses int            `json:"total_accesses"`
	ActiveLocks   int            `json:"active_locks"`
	TaskCount     int            `json:"task_count"`
	ResourceCount int            `json:"resource_count"`
	LocksByType   map[string]int `json:"locks_by_type"`
}

// Snapshot 返回当前追踪器状态的快照（线程安全）。
func (rt *ResourceTracker) Snapshot() ResourceTrackerSnapshot {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// 建立 resourcePath -> ResourceType 索引
	pathType := make(map[string]string)
	for _, a := range rt.accesses {
		pathType[a.ResourcePath] = string(a.ResourceType)
	}

	locksByType := make(map[string]int)
	totalLocks := 0
	for path, locks := range rt.locks {
		totalLocks += len(locks)
		rt := pathType[path]
		if rt == "" {
			rt = "unknown"
		}
		locksByType[rt] += len(locks)
	}

	return ResourceTrackerSnapshot{
		TotalAccesses: len(rt.accesses),
		ActiveLocks:   totalLocks,
		TaskCount:     len(rt.taskResources),
		ResourceCount: len(rt.locks),
		LocksByType:   locksByType,
	}
}
