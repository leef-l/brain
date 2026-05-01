// resource_budget.go — BrainPool 的资源预算策略。
//
// 用户期望"不写死实例数 → 越多越好但不打死机器"。本文件提供：
//   - MachineMemoryMB / MachineCPUs 读机器配置
//   - SidecarBudget 计算"还能再启动一个 sidecar 吗"
//
// 默认策略：所有 sidecar 加起来不超过机器 50% 的 CPU + 50% 内存。
// 可通过环境变量 BRAIN_RESOURCE_PERCENT 覆盖（如 70 表示 70%）。
package kernel

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// SidecarMemEstimateMB 单个 sidecar 进程的平均内存占用估计（MB）。
//
// 实测数据（2026-05-01，7 个 code sidecar 并发）：每个 RSS ~10MB（thin sidecar，
// LLM 在 host 端 LLMProxy 不在 sidecar 里）。50MB 留 5x 安全余量足够。
// 重 sidecar（quant/data 接 PostgreSQL）实测 ~50-100MB，仍在 50MB × 安全系数内。
//
// 3.6G 机器 × 50% 预算 ÷ 50MB ≈ 36 个总实例上限（之前 200MB 估计只能 9 个）。
const SidecarMemEstimateMB = 50

// resourceCfg 缓存机器资源配置（启动时读一次，避免每次 Acquire 都读 /proc/meminfo）。
var (
	resourceCfgOnce sync.Once
	totalMemMB      int
	totalCPUs       int
	usagePercent    float64
)

// loadResourceConfig 加载机器配置 + BRAIN_RESOURCE_PERCENT 环境变量。
func loadResourceConfig() {
	totalCPUs = runtime.NumCPU()
	totalMemMB = readTotalMemoryMB()
	usagePercent = 0.5
	if v := os.Getenv("BRAIN_RESOURCE_PERCENT"); v != "" {
		if pct, err := strconv.ParseFloat(v, 64); err == nil && pct > 0 && pct <= 100 {
			usagePercent = pct / 100.0
		}
	}
}

// readTotalMemoryMB 读 /proc/meminfo 拿总内存（MB）；非 Linux / 读失败时返回 4096 作 fallback。
func readTotalMemoryMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 4096
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			break
		}
		kb, err := strconv.Atoi(parts[1])
		if err != nil {
			break
		}
		return kb / 1024
	}
	return 4096
}

// MaxSidecarsByMemory 返回按内存预算允许的最大 sidecar 总数。
// = (totalMemMB * usagePercent) / SidecarMemEstimateMB
func MaxSidecarsByMemory() int {
	resourceCfgOnce.Do(loadResourceConfig)
	budget := float64(totalMemMB) * usagePercent
	max := int(budget / float64(SidecarMemEstimateMB))
	if max < 1 {
		max = 1
	}
	return max
}

// MaxSidecarsByCPU 返回按 CPU 预算允许的最大 sidecar 总数。
// sidecar 大部分时间在等 LLM 响应（IO 密集），不是 CPU 密集，所以实例数可显著超过物理核数。
// 用 cpus * 8 作上限（每核 8 个并发是合理常见值，再多 LLM provider 也撑不住）。
// 内存预算通常更早卡住，所以这个值通常不是真正的瓶颈。
func MaxSidecarsByCPU() int {
	resourceCfgOnce.Do(loadResourceConfig)
	max := totalCPUs * 8
	if max < 4 {
		max = 4
	}
	return max
}

// MaxTotalSidecars 返回综合资源预算（取内存和 CPU 限制的较小值）。
func MaxTotalSidecars() int {
	a := MaxSidecarsByMemory()
	b := MaxSidecarsByCPU()
	if a < b {
		return a
	}
	return b
}

// CanSpawnSidecar 判断在已有 currentTotal 个 sidecar 的基础上是否还能启动新实例。
// hardMax 来自 BrainRegistration.MaxInstances（0 = 仅受资源限制；>0 = 还要 ≤ hardMax）。
// 返回 true 表示可以再启一个。
func CanSpawnSidecar(currentTotalAllKinds, currentSameKind, hardMaxSameKind int) bool {
	if currentTotalAllKinds >= MaxTotalSidecars() {
		return false
	}
	if hardMaxSameKind > 0 && currentSameKind >= hardMaxSameKind {
		return false
	}
	return true
}

// ResourceSnapshot 是给 /v1/health / /v1/brains 接口暴露的只读快照。
type ResourceSnapshot struct {
	TotalMemoryMB        int     `json:"total_memory_mb"`
	TotalCPUs            int     `json:"total_cpus"`
	UsagePercent         float64 `json:"usage_percent"`
	MaxSidecarsByMemory  int     `json:"max_sidecars_by_memory"`
	MaxSidecarsByCPU     int     `json:"max_sidecars_by_cpu"`
	MaxTotalSidecars     int     `json:"max_total_sidecars"`
	SidecarMemEstimateMB int     `json:"sidecar_mem_estimate_mb"`
}

// SnapshotResources 返回当前资源策略的只读快照。
func SnapshotResources() ResourceSnapshot {
	resourceCfgOnce.Do(loadResourceConfig)
	return ResourceSnapshot{
		TotalMemoryMB:        totalMemMB,
		TotalCPUs:            totalCPUs,
		UsagePercent:         usagePercent,
		MaxSidecarsByMemory:  MaxSidecarsByMemory(),
		MaxSidecarsByCPU:     MaxSidecarsByCPU(),
		MaxTotalSidecars:     MaxTotalSidecars(),
		SidecarMemEstimateMB: SidecarMemEstimateMB,
	}
}
