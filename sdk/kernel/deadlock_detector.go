// deadlock_detector.go — 资源等待图死锁检测器（Wait-For Graph）
// MACCS Wave 4 — 并发控制与冲突仲裁。
package kernel

import (
	"fmt"
	"sync"
	"time"
)

// WaitEdge 等待边：waiter 等待 holder 释放 resource。
type WaitEdge struct {
	WaiterTaskID string    `json:"waiter_task_id"`
	HolderTaskID string    `json:"holder_task_id"`
	ResourcePath string    `json:"resource_path"`
	WaitingSince time.Time `json:"waiting_since"`
}

// DeadlockCycle 检测到的一个死锁环。
type DeadlockCycle struct {
	CycleID    string    `json:"cycle_id"`
	TaskIDs    []string  `json:"task_ids"`
	Resources  []string  `json:"resources"`
	DetectedAt time.Time `json:"detected_at"`
	Severity   string    `json:"severity"`
}

// DeadlockReport 一次完整检测的报告。
type DeadlockReport struct {
	ReportID    string          `json:"report_id"`
	Cycles      []DeadlockCycle `json:"cycles"`
	HasDeadlock bool            `json:"has_deadlock"`
	TotalEdges  int             `json:"total_edges"`
	TotalNodes  int             `json:"total_nodes"`
	CheckedAt   time.Time       `json:"checked_at"`
}

// DeadlockStats 统计摘要。
type DeadlockStats struct {
	TotalTasks   int `json:"total_tasks"`
	TotalEdges   int `json:"total_edges"`
	MaxWaitDepth int `json:"max_wait_depth"`
}

// DeadlockDetector 基于 wait-for graph 的死锁检测器。
type DeadlockDetector struct {
	mu    sync.RWMutex
	edges map[string][]WaitEdge // waiterTaskID → 出边
	tasks map[string]bool
}

func NewDeadlockDetector() *DeadlockDetector {
	return &DeadlockDetector{edges: make(map[string][]WaitEdge), tasks: make(map[string]bool)}
}

func (d *DeadlockDetector) AddWaitEdge(waiterTaskID, holderTaskID, resourcePath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tasks[waiterTaskID] = true
	d.tasks[holderTaskID] = true
	d.edges[waiterTaskID] = append(d.edges[waiterTaskID], WaitEdge{
		WaiterTaskID: waiterTaskID, HolderTaskID: holderTaskID,
		ResourcePath: resourcePath, WaitingSince: time.Now(),
	})
}

func (d *DeadlockDetector) RemoveEdge(waiterTaskID, holderTaskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.edges[waiterTaskID]
	n := 0
	for _, e := range list {
		if e.HolderTaskID != holderTaskID {
			list[n] = e
			n++
		}
	}
	if n == 0 {
		delete(d.edges, waiterTaskID)
	} else {
		d.edges[waiterTaskID] = list[:n]
	}
}

func (d *DeadlockDetector) RemoveTask(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.edges, taskID)
	delete(d.tasks, taskID)
	for w, list := range d.edges {
		n := 0
		for _, e := range list {
			if e.HolderTaskID != taskID {
				list[n] = e
				n++
			}
		}
		if n == 0 {
			delete(d.edges, w)
		} else {
			d.edges[w] = list[:n]
		}
	}
}

func (d *DeadlockDetector) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.edges = make(map[string][]WaitEdge)
	d.tasks = make(map[string]bool)
}

func (d *DeadlockDetector) Detect() *DeadlockReport {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.detectLocked()
}

func (d *DeadlockDetector) detectLocked() *DeadlockReport {
	now := time.Now()
	report := &DeadlockReport{
		ReportID: fmt.Sprintf("dr-%d", now.UnixNano()),
		TotalEdges: d.countEdges(), TotalNodes: len(d.tasks), CheckedAt: now,
	}
	visited, recStack := make(map[string]bool), make(map[string]bool)
	parent, edgeRes, seen := make(map[string]string), make(map[string]string), make(map[string]bool)
	for _, list := range d.edges {
		for _, e := range list {
			edgeRes[e.WaiterTaskID+"->"+e.HolderTaskID] = e.ResourcePath
		}
	}
	var dfs func(string)
	dfs = func(node string) {
		visited[node], recStack[node] = true, true
		for _, e := range d.edges[node] {
			next := e.HolderTaskID
			if !visited[next] {
				parent[next] = node
				dfs(next)
			} else if recStack[next] {
				cy := d.extractCycle(next, node, parent, edgeRes)
				sig := fmt.Sprintf("%v", cy.TaskIDs)
				if !seen[sig] {
					seen[sig] = true
					cy.CycleID = fmt.Sprintf("cyc-%d-%d", now.UnixNano(), len(report.Cycles))
					cy.DetectedAt = now
					cy.Severity = "warning"
					if len(cy.TaskIDs) <= 2 {
						cy.Severity = "critical"
					}
					report.Cycles = append(report.Cycles, cy)
				}
			}
		}
		recStack[node] = false
	}
	for t := range d.tasks {
		if !visited[t] {
			dfs(t)
		}
	}
	report.HasDeadlock = len(report.Cycles) > 0
	return report
}

func (d *DeadlockDetector) extractCycle(start, end string, parent, edgeRes map[string]string) DeadlockCycle {
	var tasks, resources []string
	for cur := end; cur != start; {
		tasks = append([]string{cur}, tasks...)
		p, ok := parent[cur]
		if !ok {
			break
		}
		if r, ok := edgeRes[p+"->"+cur]; ok {
			resources = append([]string{r}, resources...)
		}
		cur = p
	}
	tasks = append([]string{start}, tasks...)
	if r, ok := edgeRes[end+"->"+start]; ok {
		resources = append(resources, r)
	}
	return DeadlockCycle{TaskIDs: tasks, Resources: resources}
}

func (d *DeadlockDetector) WouldDeadlock(waiterTaskID, holderTaskID, resourcePath string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tasks[waiterTaskID], d.tasks[holderTaskID] = true, true
	d.edges[waiterTaskID] = append(d.edges[waiterTaskID], WaitEdge{
		WaiterTaskID: waiterTaskID, HolderTaskID: holderTaskID,
		ResourcePath: resourcePath, WaitingSince: time.Now(),
	})
	has := d.detectLocked().HasDeadlock
	list := d.edges[waiterTaskID]
	d.edges[waiterTaskID] = list[:len(list)-1]
	if len(d.edges[waiterTaskID]) == 0 {
		delete(d.edges, waiterTaskID)
	}
	return has
}

func (d *DeadlockDetector) GetWaitChain(taskID string) []WaitEdge {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var chain []WaitEdge
	seen := map[string]bool{taskID: true}
	q := []string{taskID}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		for _, e := range d.edges[cur] {
			chain = append(chain, e)
			if !seen[e.HolderTaskID] {
				seen[e.HolderTaskID] = true
				q = append(q, e.HolderTaskID)
			}
		}
	}
	return chain
}

func (d *DeadlockDetector) SuggestVictim(cycle DeadlockCycle) string {
	if len(cycle.TaskIDs) == 0 {
		return ""
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var victim string
	var latest time.Time
	for _, tid := range cycle.TaskIDs {
		for _, e := range d.edges[tid] {
			if e.WaitingSince.After(latest) {
				latest = e.WaitingSince
				victim = tid
			}
		}
	}
	if victim == "" {
		return cycle.TaskIDs[len(cycle.TaskIDs)-1]
	}
	return victim
}

func (d *DeadlockDetector) Stats() DeadlockStats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	md := 0
	for t := range d.tasks {
		if dep := d.walkDepth(t, make(map[string]bool)); dep > md {
			md = dep
		}
	}
	return DeadlockStats{TotalTasks: len(d.tasks), TotalEdges: d.countEdges(), MaxWaitDepth: md}
}

func (d *DeadlockDetector) countEdges() int {
	n := 0
	for _, l := range d.edges {
		n += len(l)
	}
	return n
}

func (d *DeadlockDetector) walkDepth(node string, vis map[string]bool) int {
	if vis[node] {
		return 0
	}
	vis[node] = true
	m := 0
	for _, e := range d.edges[node] {
		if c := d.walkDepth(e.HolderTaskID, vis); c > m {
			m = c
		}
	}
	return m + 1
}
