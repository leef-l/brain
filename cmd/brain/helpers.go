package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/kernel/manifest"
	"github.com/leef-l/brain/sdk/llm"
)

// bgCtx returns a context with a 30-second timeout for CLI operations.
// The caller MUST defer the returned cancel function to avoid goroutine leaks.
func bgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// defaultBinResolver returns a BinResolver that searches for sidecar
// binaries next to the current executable and in PATH.
func defaultBinResolver() func(kind agent.Kind) (string, error) {
	selfPath, _ := os.Executable()
	selfDir := filepath.Dir(selfPath)

	return func(kind agent.Kind) (string, error) {
		names := sidecarBinaryNamesForOS(kind, runtime.GOOS)

		// Check next to the current binary.
		for _, name := range names {
			candidate := filepath.Join(selfDir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}

		// Check PATH.
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

		return "", fmt.Errorf("sidecar binary %q not found", names[0])
	}
}

func sidecarBinaryNamesForOS(kind agent.Kind, goos string) []string {
	// Prefer the dedicated sidecar binary (brain-<kind>-sidecar) which
	// implements the stdio JSON-RPC protocol. Fall back to brain-<kind>
	// for brains where the standalone binary doubles as a sidecar
	// (e.g. brain-code, brain-central).
	sidecar := fmt.Sprintf("brain-%s-sidecar", kind)
	fallback := fmt.Sprintf("brain-%s", kind)
	if goos == "windows" {
		return []string{sidecar + ".exe", sidecar, fallback + ".exe", fallback}
	}
	return []string{sidecar, fallback}
}

// orchestratorConfig holds parameters for building an Orchestrator.
type orchestratorConfig struct {
	cfg         *brainConfig
	modelConfig *modelConfigInput
	provider    string
	apiKey      string
	baseURL     string
	model       string
}

// discoverBrainsFromManifest 扫描标准目录中的 brain.json，将发现的脑
// 转换为 kernel.BrainRegistration 列表。扫描顺序：
//  1. 项目内 brains/*/brain.json 和 central/brain.json
//  2. ~/.brain/brains/<kind>/brain.json（安装目录）
//
// 当同一 kind 在多个位置出现时，先发现的优先（项目内 > 安装目录）。
func discoverBrainsFromManifest() []kernel.BrainRegistration {
	var registrations []kernel.BrainRegistration
	seen := make(map[string]bool)

	// 确定项目根目录
	workdir, _ := os.Getwd()
	projectRoot := findProjectRoot(workdir)

	// 收集所有要扫描的目录对 (dir, kindHint)
	type scanEntry struct {
		dir      string
		kindHint string // 目录名暗示的 kind，可被 manifest 覆盖
	}
	var scanDirs []scanEntry

	// 1. 项目内 central/brain.json
	if projectRoot != "" {
		centralDir := filepath.Join(projectRoot, "central")
		if fi, err := os.Stat(centralDir); err == nil && fi.IsDir() {
			scanDirs = append(scanDirs, scanEntry{dir: centralDir, kindHint: "central"})
		}

		// 项目内 brains/*/
		brainsBase := filepath.Join(projectRoot, "brains")
		if entries, err := os.ReadDir(brainsBase); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					scanDirs = append(scanDirs, scanEntry{
						dir:      filepath.Join(brainsBase, e.Name()),
						kindHint: e.Name(),
					})
				}
			}
		}
	}

	// 2. ~/.brain/brains/<kind>/
	if homeDir, err := os.UserHomeDir(); err == nil {
		homeBrainsDir := filepath.Join(homeDir, ".brain", "brains")
		if entries, err := os.ReadDir(homeBrainsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					scanDirs = append(scanDirs, scanEntry{
						dir:      filepath.Join(homeBrainsDir, e.Name()),
						kindHint: e.Name(),
					})
				}
			}
		}
	}

	// 逐目录加载 manifest
	for _, entry := range scanDirs {
		m, err := manifest.LoadFromDir(entry.dir)
		if err != nil {
			continue
		}

		kind := m.Kind
		if kind == "" {
			kind = entry.kindHint
		}
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true

		reg := kernel.BrainRegistration{
			Kind: agent.Kind(kind),
		}

		// 从 runtime.entrypoint 推导 Binary 路径
		if m.Runtime.Entrypoint != "" {
			reg.Binary = m.Runtime.Entrypoint
		}

		// 从 metadata 中提取可选的 model 和 auto_start
		if m.Metadata != nil {
			if model, ok := m.Metadata["model"].(string); ok {
				reg.Model = model
			}
			if autoStart, ok := m.Metadata["auto_start"].(bool); ok {
				reg.AutoStart = autoStart
			}
		}

		registrations = append(registrations, reg)
		fmt.Fprintf(os.Stderr, "  manifest-discovery: %s (runtime=%s, entrypoint=%s)\n",
			kind, m.Runtime.Type, m.Runtime.Entrypoint)
	}

	if len(registrations) > 0 {
		fmt.Fprintf(os.Stderr, "  manifest-discovery: found %d brain(s) from manifest files\n\n", len(registrations))
	}

	return registrations
}

// mergeManifestAndConfigBrains 合并 manifest 发现的脑和 cfg.Brains 配置的脑。
// manifest 优先：相同 kind 以 manifest 为准，cfg.Brains 中未被覆盖的条目作为 fallback 补充。
func mergeManifestAndConfigBrains(manifestBrains []kernel.BrainRegistration, cfgBrains []kernel.BrainRegistration) []kernel.BrainRegistration {
	seen := make(map[agent.Kind]bool, len(manifestBrains))
	result := make([]kernel.BrainRegistration, 0, len(manifestBrains)+len(cfgBrains))

	for _, reg := range manifestBrains {
		seen[reg.Kind] = true
		result = append(result, reg)
	}

	// cfg.Brains 中不在 manifest 中的条目作为 fallback
	for _, reg := range cfgBrains {
		if !seen[reg.Kind] {
			result = append(result, reg)
		}
	}

	return result
}

// buildBrainPool 创建全局 BrainPool，serve 生命周期持有。
// 多个并发 run 共享同一个 pool，不再 per-run fork sidecar。
// 优先使用 manifest 发现的脑，cfg.Brains 作为 fallback。
func buildBrainPool(cfg *brainConfig) *kernel.ProcessBrainPool {
	binResolver := defaultBinResolver()
	runner := &kernel.ProcessRunner{BinResolver: binResolver}

	var orchCfg kernel.OrchestratorConfig

	// 1. 从 manifest 自动发现脑
	manifestBrains := discoverBrainsFromManifest()

	// 2. 合并 cfg.Brains 作为 fallback
	var cfgBrains []kernel.BrainRegistration
	if cfg != nil && len(cfg.Brains) > 0 {
		cfgBrains = cfg.Brains
	}

	merged := mergeManifestAndConfigBrains(manifestBrains, cfgBrains)
	if len(merged) > 0 {
		orchCfg.Brains = merged
	}

	pool := kernel.NewProcessBrainPool(runner, binResolver, orchCfg)
	if len(pool.AvailableKinds()) == 0 {
		return nil
	}
	return pool
}

// buildOrchestrator creates an Orchestrator with LLM proxy for specialist
// brain delegation. Returns nil if no specialist binaries are found.
// This is shared between `brain chat` and `brain run`.
//
// When the config contains a "brains" array, only those brains are probed
// (configuration-driven mode). Otherwise falls back to probing all built-in
// kinds via binary discovery on PATH.
func buildOrchestrator(oc orchestratorConfig) *kernel.Orchestrator {
	binResolver := defaultBinResolver()

	llmProxy := &kernel.LLMProxy{
		ProviderFactory: func(kind agent.Kind) llm.Provider {
			if wantsMockProvider(oc.provider, oc.modelConfig) {
				return nil
			}
			session, err := openConfiguredProvider(oc.cfg, string(kind), oc.modelConfig, oc.provider, oc.apiKey, oc.baseURL, oc.model)
			if err != nil {
				return nil
			}
			return session.Provider
		},
	}

	runner := &kernel.ProcessRunner{BinResolver: binResolver}

	// 优先使用 manifest 发现的脑，cfg.Brains 作为 fallback。
	var orchCfg kernel.OrchestratorConfig
	manifestBrains := discoverBrainsFromManifest()
	var cfgBrains []kernel.BrainRegistration
	if oc.cfg != nil && len(oc.cfg.Brains) > 0 {
		cfgBrains = oc.cfg.Brains
	}
	merged := mergeManifestAndConfigBrains(manifestBrains, cfgBrains)
	if len(merged) > 0 {
		orchCfg.Brains = merged
	}
	orch := kernel.NewOrchestratorWithConfig(runner, llmProxy, binResolver, orchCfg)

	// 注入自适应学习引擎（L1 协作级学习）
	learner := kernel.NewLearningEngine()
	kernel.WithLearningEngine(learner)(orch)

	if len(orch.AvailableKinds()) == 0 {
		return nil
	}
	return orch
}

// loadManifestCapabilities 从 brain.json manifest 文件加载能力标签，
// 注册到 CapabilityIndex。搜索顺序：
//  1. 项目目录 brains/<kind>/brain.json
//  2. 项目目录 central/brain.json（kind == "central"）
//  3. ~/.brain/brains/<kind>/brain.json（安装目录）
//
// 找不到 manifest 时静默跳过，保持向后兼容。
func loadManifestCapabilities(capIndex *kernel.CapabilityIndex, pool *kernel.ProcessBrainPool, workdir string) {
	if capIndex == nil {
		return
	}

	// 收集要扫描的 kinds：从 pool 获取（如果可用），另外始终包含 central。
	kindSet := map[agent.Kind]struct{}{
		"central": {},
	}
	if pool != nil {
		for _, k := range pool.AvailableKinds() {
			kindSet[k] = struct{}{}
		}
	}

	// 确定项目根目录（workdir 向上找 go.mod，或直接用 workdir）
	projectRoot := findProjectRoot(workdir)

	// ~/.brain/brains/ 安装目录
	homeDir, _ := os.UserHomeDir()
	var homeBrainsDir string
	if homeDir != "" {
		homeBrainsDir = filepath.Join(homeDir, ".brain", "brains")
	}

	registered := 0
	for kind := range kindSet {
		m := loadManifestForKind(kind, projectRoot, homeBrainsDir)
		if m == nil {
			continue
		}
		if len(m.Capabilities) == 0 {
			continue
		}
		capIndex.AddBrain(kind, m.Capabilities)
		registered++
		fmt.Fprintf(os.Stderr, "  manifest: %s → %d capabilities\n", kind, len(m.Capabilities))
	}
	if registered > 0 {
		fmt.Fprintf(os.Stderr, "  manifest: %d brain(s) registered in CapabilityIndex\n\n", registered)
	}
}

// loadManifestForKind 按优先级搜索某个 kind 的 brain.json。
func loadManifestForKind(kind agent.Kind, projectRoot, homeBrainsDir string) *manifest.Manifest {
	// 搜索候选目录列表
	var dirs []string
	if projectRoot != "" {
		if kind == "central" {
			dirs = append(dirs, filepath.Join(projectRoot, "central"))
		}
		dirs = append(dirs, filepath.Join(projectRoot, "brains", string(kind)))
	}
	if homeBrainsDir != "" {
		dirs = append(dirs, filepath.Join(homeBrainsDir, string(kind)))
	}

	for _, dir := range dirs {
		m, err := manifest.LoadFromDir(dir)
		if err == nil {
			return m
		}
	}
	return nil
}

// findProjectRoot 从 startDir 向上搜索包含 go.mod 的目录作为项目根。
// 找不到则返回 startDir 本身。
func findProjectRoot(startDir string) string {
	if startDir == "" {
		return ""
	}
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return startDir
}
