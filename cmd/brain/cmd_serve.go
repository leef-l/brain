package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	iofs "io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/dashboard"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/cli"
	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/license"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/tool"
	"github.com/leef-l/brain/sdk/tool/cdp"
)

const patternSplitScanInterval = 30 * time.Second

var (
	patternSplitSharedLibrary = tool.SharedPatternLibrary
	patternSplitScan          = func(ctx context.Context, lib *tool.PatternLibrary, store tool.PatternFailureStore) ([]string, error) {
		return tool.ScanForSplit(ctx, lib, store)
	}
	browserLicenseVerifyOptionsFromEnv = license.VerifyOptionsFromEnv
	browserLicenseCheckSidecar         = license.CheckSidecar
)

func configureBrowserFeatureGate() error {
	verifyOpts, err := browserLicenseVerifyOptionsFromEnv(license.VerifyOptions{})
	if err != nil {
		tool.ConfigureBrowserFeatureGate(nil)
		return err
	}

	res, err := browserLicenseCheckSidecar("brain-browser", verifyOpts)
	if err != nil {
		tool.ConfigureBrowserFeatureGate(nil)
		return err
	}

	tool.ConfigureBrowserFeatureGate(res)
	return nil
}

func publishBrowserRuntimeProjection(runtimeDataDir string) error {
	if strings.TrimSpace(runtimeDataDir) == "" {
		return nil
	}
	gate := tool.CurrentBrowserFeatureGateConfig()
	projection := kernel.BrowserRuntimeProjectionForDataDir(runtimeDataDir, gate.Enabled, gate.Features)
	return kernel.WriteBrowserRuntimeProjectionFile(projection.SyncFile, projection)
}

func startBrowserRuntimeProjectionPublisher(ctx context.Context, runtimeDataDir string) {
	if strings.TrimSpace(runtimeDataDir) == "" {
		return
	}
	if err := publishBrowserRuntimeProjection(runtimeDataDir); err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: browser runtime projection: %v\n", err)
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		var (
			lastDBMod      time.Time
			lastPatternMod time.Time
			lastGateKey    string
			lastGateOn     bool
		)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				gate := tool.CurrentBrowserFeatureGateConfig()
				dbMod := fileModTime(filepath.Join(runtimeDataDir, "brain.db"))
				patternMod := fileModTime(filepath.Join(runtimeDataDir, "ui_patterns.db"))
				gateKey := strings.Join(sortedEnabledFeatureNames(gate.Features), ",")
				if dbMod.Equal(lastDBMod) && patternMod.Equal(lastPatternMod) && gateKey == lastGateKey && gate.Enabled == lastGateOn {
					continue
				}
				if err := publishBrowserRuntimeProjection(runtimeDataDir); err != nil {
					fmt.Fprintf(os.Stderr, "brain serve: warning: browser runtime projection: %v\n", err)
					continue
				}
				lastDBMod = dbMod
				lastPatternMod = patternMod
				lastGateKey = gateKey
				lastGateOn = gate.Enabled
			}
		}
	}()
}

func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime().UTC()
}

func sortedEnabledFeatureNames(features map[string]bool) []string {
	names := make([]string, 0, len(features))
	for name, enabled := range features {
		if enabled && strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func loadSharedAnomalyTemplateLibrary(ctx context.Context, learner *kernel.LearningEngine) error {
	lib := tool.NewAnomalyTemplateLibrary()
	if learner == nil {
		tool.SetSharedAnomalyTemplateLibrary(lib)
		return nil
	}

	templates, err := learner.ListAnomalyTemplates(ctx)
	if err != nil {
		return err
	}
	for _, tpl := range templates {
		if tpl == nil {
			continue
		}
		recovery, err := tool.DecodeRecoveryActions(tpl.RecoveryActions)
		if err != nil {
			return fmt.Errorf("decode anomaly template %d recovery: %w", tpl.ID, err)
		}
		lib.Upsert(&tool.AnomalyTemplate{
			ID: tpl.ID,
			Signature: tool.AnomalyTemplateSignature{
				Type:        tpl.SignatureType,
				Subtype:     tpl.SignatureSubtype,
				SitePattern: tpl.SignatureSite,
				Severity:    tpl.SignatureSeverity,
			},
			Recovery: recovery,
			Stats: tool.AnomalyTemplateStats{
				MatchCount:   tpl.MatchCount,
				SuccessCount: tpl.SuccessCount,
				FailureCount: tpl.FailureCount,
				UpdatedAt:    tpl.UpdatedAt,
			},
			CreatedAt: tpl.CreatedAt,
			UpdatedAt: tpl.UpdatedAt,
			Source:    "persisted",
		})
	}

	tool.SetSharedAnomalyTemplateLibrary(lib)
	return nil
}

// runEntry tracks an in-flight or finished Run.
type runEntry struct {
	mu          sync.Mutex      `json:"-"`
	ID          string          `json:"run_id"`
	ExecutionID string          `json:"execution_id,omitempty"` // v3: 与 run_id 相同，用于 executions 端点
	Status      string          `json:"status"`
	Brain       string          `json:"brain,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`

	// v3 execution 扩展字段（omitempty 保持向后兼容）
	Mode      string `json:"mode,omitempty"`
	Lifecycle string `json:"lifecycle,omitempty"`
	Restart   string `json:"restart,omitempty"`

	// taskExec 持有 v3 TaskExecution 状态机实例，避免 executeRun 中重复创建。
	taskExec *kernel.TaskExecution `json:"-"`

	cancel context.CancelFunc `json:"-"`
}

func runPatternSplitScanOnce(ctx context.Context, store tool.PatternFailureStore) (int, error) {
	if store == nil {
		return 0, nil
	}
	lib := patternSplitSharedLibrary()
	if lib == nil {
		return 0, nil
	}
	spawned, err := patternSplitScan(ctx, lib, store)
	if err != nil {
		return 0, err
	}
	return len(spawned), nil
}

func startPatternSplitScanner(ctx context.Context, store tool.PatternFailureStore) {
	if store == nil {
		return
	}
	go func() {
		if n, err := runPatternSplitScanOnce(ctx, store); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: warning: pattern split scan: %v\n", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "brain serve: pattern split spawned %d variant(s)\n", n)
		}

		ticker := time.NewTicker(patternSplitScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := runPatternSplitScanOnce(ctx, store)
				if err != nil {
					fmt.Fprintf(os.Stderr, "brain serve: warning: pattern split scan: %v\n", err)
					continue
				}
				if n > 0 {
					fmt.Fprintf(os.Stderr, "brain serve: pattern split spawned %d variant(s)\n", n)
				}
			}
		}
	}()
}

func (e *runEntry) snapshot() *runEntry {
	e.mu.Lock()
	defer e.mu.Unlock()

	return &runEntry{
		ID:          e.ID,
		ExecutionID: e.ExecutionID,
		Status:      e.Status,
		Brain:       e.Brain,
		Prompt:      e.Prompt,
		Result:      append(json.RawMessage(nil), e.Result...),
		CreatedAt:   e.CreatedAt,
		Mode:        e.Mode,
		Lifecycle:   e.Lifecycle,
		Restart:     e.Restart,
	}
}

func (e *runEntry) status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Status
}

func (e *runEntry) markCancelled() string {
	e.mu.Lock()
	if e.Status != "running" && e.Status != "waiting" {
		status := e.Status
		e.mu.Unlock()
		return status
	}
	e.Status = "cancelled"
	cancel := e.cancel
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return "cancelled"
}

func (e *runEntry) finish(status string, result json.RawMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Status == "cancelled" && status != "cancelled" {
		if len(e.Result) == 0 && len(result) > 0 {
			e.Result = result
		}
		return
	}
	e.Status = status
	e.Result = result
}

// runManager manages Runs in memory.
type runManager struct {
	runs         sync.Map // id → *runEntry
	store        *runtimeStore
	pool         *kernel.ProcessBrainPool  // 全局共享的 BrainPool，多 run 复用
	eventBus     *events.MemEventBus       // 全局事件总线，用于 SSE 推送
	leaseManager *kernel.MemLeaseManager   // 全局租约管理器，协调资源互斥与共享访问
	capMatcher   *kernel.CapabilityMatcher // 全局能力匹配器，三阶段 brain 选择
	learner      *kernel.LearningEngine    // 全局自适应学习引擎（L1-L3）
	ctxEngine    kernel.ContextEngine      // 全局上下文装配引擎
	orgEnforcer  kernel.OrgPolicyEnforcer  // 组织级授权策略执行器（可为 nil）
	scheduler    kernel.TaskScheduler      // 任务级调度引擎（B-2）
	remotePool   *kernel.RemoteBrainPool   // 远程 brain 连接池（D-3，可为 nil）
	rootCtx      context.Context
	wg           sync.WaitGroup
	launchMu     sync.Mutex // guards concurrent slot reservations
	running      int
}

type learningStoreSitemapCache struct {
	store persistence.LearningStore
}

func newSharedBrowserHumanEventSourceFactory() tool.HumanEventSourceFactory {
	return func(context.Context) (cdp.EventSource, error) {
		sess, ok := tool.CurrentSharedBrowserSession()
		if !ok || sess == nil {
			// The browser session is created lazily by browser tools. During
			// early takeover requests there may be no live session yet; in that
			// case degrade to marker-only recording instead of creating a new one.
			return nil, nil
		}
		return cdp.NewCDPEventSource(sess), nil
	}
}

func (c learningStoreSitemapCache) Save(ctx context.Context, snap *persistence.SitemapSnapshot) error {
	if c.store == nil {
		return nil
	}
	return c.store.SaveSitemapSnapshot(ctx, snap)
}

func (c learningStoreSitemapCache) Get(ctx context.Context, siteOrigin string, depth int) (*persistence.SitemapSnapshot, error) {
	if c.store == nil {
		return nil, nil
	}
	return c.store.GetSitemapSnapshot(ctx, siteOrigin, depth)
}

func (c learningStoreSitemapCache) Purge(ctx context.Context, olderThan time.Time) (int64, error) {
	if c.store == nil {
		return 0, nil
	}
	return c.store.PurgeSitemapSnapshots(ctx, olderThan)
}

func (rm *runManager) get(id string) (*runEntry, bool) {
	v, ok := rm.runs.Load(id)
	if ok {
		return v.(*runEntry), true
	}
	if rm.store == nil {
		return nil, false
	}
	rec, ok := rm.store.Get(id)
	if !ok {
		return nil, false
	}
	return &runEntry{
		ID:        rec.ID,
		Status:    rec.Status,
		Brain:     rec.BrainID,
		Prompt:    rec.Prompt,
		Result:    append(json.RawMessage(nil), rec.Result...),
		CreatedAt: rec.CreatedAt,
	}, true
}

func (rm *runManager) list() []*runEntry {
	if rm.store != nil {
		records := rm.store.List(0, "all")
		out := make([]*runEntry, 0, len(records))
		for _, rec := range records {
			out = append(out, &runEntry{
				ID:        rec.ID,
				Status:    rec.Status,
				Brain:     rec.BrainID,
				Prompt:    rec.Prompt,
				Result:    append(json.RawMessage(nil), rec.Result...),
				CreatedAt: rec.CreatedAt,
			})
		}
		return out
	}
	var out []*runEntry
	rm.runs.Range(func(_, v interface{}) bool {
		out = append(out, v.(*runEntry).snapshot())
		return true
	})
	return out
}

func (rm *runManager) runningCount() int {
	rm.launchMu.Lock()
	defer rm.launchMu.Unlock()
	return rm.running
}

func (rm *runManager) launch(entry *runEntry, fn func()) {
	rm.runs.Store(entry.ID, entry)
	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		fn()
	}()
}

// reserveSlot atomically acquires capacity for a new run.
// Returns false if the concurrency limit has been reached.
func (rm *runManager) reserveSlot(maxConcurrent int) bool {
	rm.launchMu.Lock()
	defer rm.launchMu.Unlock()
	if rm.running >= maxConcurrent {
		return false
	}
	rm.running++
	return true
}

func (rm *runManager) releaseSlot() {
	rm.launchMu.Lock()
	defer rm.launchMu.Unlock()
	if rm.running > 0 {
		rm.running--
	}
}

func (rm *runManager) launchReserved(entry *runEntry, fn func()) {
	rm.launch(entry, func() {
		defer rm.releaseSlot()
		fn()
	})
}

func (rm *runManager) cancelAll(reason string) {
	rm.runs.Range(func(_, v interface{}) bool {
		entry := v.(*runEntry)
		status := entry.markCancelled()
		if rm.store != nil && status == "cancelled" {
			data, _ := json.Marshal(map[string]string{"reason": reason})
			_ = rm.store.AppendEvent(entry.ID, "run.cancel.requested", reason, data)
			_, _ = rm.store.Finish(entry.ID, "cancelled", entry.Result, reason)
		}
		return true
	})
}

func (rm *runManager) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		rm.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runServe implements `brain serve [--listen <addr>]`.
func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "127.0.0.1:7701", "listen address (host:port)")
	maxRuns := fs.Int("max-concurrent-runs", 20, "maximum concurrent runs")
	logFile := fs.String("log-file", "", "log file path (default: stderr)")
	modeFlag := fs.String("mode", "", "permission mode: plan, default, accept-edits, auto, restricted, bypass-permissions")
	workDir := fs.String("workdir", "", "working directory sandbox (default: current directory)")
	runWorkdirPolicyFlag := fs.String("run-workdir-policy", "", "run workdir policy: confined or open")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}

	_ = logFile

	cfg, cfgErr := loadConfig()
	config.ApplyDiagnosticEnv(cfg)
	mode, err := resolvePermissionMode(*modeFlag, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: %v\n", err)
		return cli.ExitUsage
	}
	runWorkdirPolicy, err := resolveServeWorkdirPolicy(*runWorkdirPolicyFlag, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: %v\n", err)
		return cli.ExitUsage
	}
	env := newExecutionEnvironment(*workDir, mode, cfg, nil, false)
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()

	runtime, err := newDefaultCLIRuntime("central")
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: runtime: %v\n", err)
		return cli.ExitSoftware
	}

	// 初始化自适应工具策略（B-5）
	initAdaptiveToolPolicy(cfg)
	if err := configureBrowserFeatureGate(); err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: browser feature gate: %v\n", err)
	}
	configureBrowserRuntimeEnv(filepath.Dir(configPath()))
	startBrowserRuntimeProjectionPublisher(serveCtx, filepath.Dir(configPath()))

	// 全局 BrainPool：所有并发 run 共享，不再 per-run fork sidecar。
	pool := buildBrainPoolWithRuntimeDir(cfg, filepath.Dir(configPath()))
	defer func() {
		if pool != nil {
			_ = pool.Shutdown(context.Background())
		}
	}()
	if pool != nil {
		pool.AutoStart(serveCtx)
	}

	// 为 startup 工具注册构建一个临时 Orchestrator（共享 pool）。
	var startupOrch *kernel.Orchestrator
	if pool != nil {
		startupOrch = kernel.NewOrchestratorWithPool(pool, &kernel.ProcessRunner{BinResolver: defaultBinResolver()}, &kernel.LLMProxy{}, defaultBinResolver(), kernel.OrchestratorConfig{})
	}
	runtime.Kernel.ToolRegistry = buildManagedRegistry(cfg, env, "central", func(reg tool.Registry) {
		registerDelegateToolForEnvironment(reg, startupOrch, env)
		registerSpecialistBridgeTools(reg, startupOrch)
	})

	fmt.Fprintf(os.Stderr, "Starting Brain Kernel (cluster mode)\n")
	fmt.Fprintf(os.Stderr, "  listen:    %s\n", *listen)
	fmt.Fprintf(os.Stderr, "  max_runs:  %d\n", *maxRuns)
	fmt.Fprintf(os.Stderr, "  mode:      %s\n", mode)
	fmt.Fprintf(os.Stderr, "  workdir:   %s\n", env.Workdir)
	fmt.Fprintf(os.Stderr, "  run_wd:    %s\n", runWorkdirPolicy)
	storeLabel := "sqlite"
	if runtime.FileStore != nil {
		storeLabel = runtime.FileStore.Path()
	}
	fmt.Fprintf(os.Stderr, "  store:     %s\n\n", storeLabel)

	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: config load: %v\n", cfgErr)
	}
	// 创建全局事件总线，供 SSE 端点和运行时事件推送使用
	globalEventBus := events.NewMemEventBus()
	leaseManager := kernel.NewMemLeaseManager()

	// Task #19: 启动外部进程钩子 runner,订阅 task.state.* 事件。配置在
	// ~/.brain/hooks.json,不存在时 no-op,不影响 serve 启动。
	if hookCfg, err := kernel.LoadHookConfig(""); err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: hook config: %v\n", err)
	} else if len(hookCfg.Hooks) > 0 {
		hookRunner := kernel.NewHookRunner(globalEventBus, hookCfg, nil)
		if err := hookRunner.Start(serveCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: warning: hook runner start: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "brain serve: hook runner active (%d hooks)\n", len(hookCfg.Hooks))
			defer hookRunner.Stop()
		}
	}
	// M6 学习闭环:让 anomalyInjectingTool 每次跑完就把结果回写给
	// AdaptivePolicy,成功率低的工具会在下一轮 Evaluate 时被自动降权 / 禁用。
	// 参照 SetInteractionSink 的进程级注入风格。
	if globalAdaptivePolicy != nil {
		tool.SetOutcomeSink(globalAdaptivePolicy)
	}

	capIndex := kernel.NewCapabilityIndex()

	// 从 brain.json manifest 加载能力标签并注册到 CapabilityIndex。
	// 搜索路径：项目内 brains/<kind>/brain.json 和 central/brain.json，
	// 以及 ~/.brain/brains/<kind>/brain.json（安装目录）。
	// 找不到 manifest 时静默跳过，保持向后兼容。
	loadManifestCapabilities(capIndex, pool, env.Workdir)

	capMatcher := kernel.NewCapabilityMatcher(capIndex)

	// 自适应学习引擎：注入 SQLite 持久化，启动时恢复历史数据
	var learner *kernel.LearningEngine
	if runtime.Stores != nil && runtime.Stores.LearningStore != nil {
		learner = kernel.NewLearningEngineWithStore(runtime.Stores.LearningStore)
		if err := learner.Load(serveCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: warning: load learning data: %v\n", err)
		}
	} else {
		learner = kernel.NewLearningEngine()
	}
	if err := loadSharedAnomalyTemplateLibrary(serveCtx, learner); err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: load anomaly templates: %v\n", err)
	}

	// Task #13: 让 browser brain 的交互序列走 LearningEngine → LearningStore,
	// 这样 ui_pattern_learn 的聚类就能从 SQLite 读到 InteractionSequence。
	tool.SetInteractionSink(learner)
	if runtime.Stores != nil && runtime.Stores.LearningStore != nil {
		// P3.3/P3.4 serve 主路径接线:
		// - browser.sitemap 直接复用 LearningStore 的 snapshot 持久化接口
		// - human.request_takeover 把真人录制序列直接落到 HumanDemoSequence
		// - pattern_exec 失败样本直接落到 pattern_failure_samples,供 P3.2 自分裂扫描
		tool.SetSitemapCache(learningStoreSitemapCache{store: runtime.Stores.LearningStore})
		tool.SetHumanDemoSink(runtime.Stores.LearningStore)
		tool.SetPatternFailureStore(runtime.Stores.LearningStore)
		startPatternSplitScanner(serveCtx, runtime.Stores.LearningStore)
	} else {
		tool.SetSitemapCache(nil)
		tool.SetHumanDemoSink(nil)
		tool.SetPatternFailureStore(nil)
	}
	// P3.3 DOM 录制绑定到已存在的共享 browser session；session 尚未初始化
	// 时工厂返回 nil，安全退化为只录 takeover marker，不会误建新 browser。
	tool.SetHumanEventSourceFactory(newSharedBrowserHumanEventSourceFactory())

	// 上下文引擎：注入 LLM Summarizer + SharedStore 持久化
	ctxEngine := kernel.NewDefaultContextEngine()
	if cfg != nil {
		if session, err := openConfiguredProvider(cfg, "central", nil, "", "", "", ""); err == nil {
			ctxEngine.Summarizer = session.Provider
			ctxEngine.SummaryModel = session.Model
		}
	}

	// Task #15: 每日对话总结 daemon。订阅 task.state.* 事件累积,跨零点调
	// LLM 总结写 LearningStore.DailySummary。ctxEngine 已有 Summarizer 则
	// 复用;缺少 LearningStore 时降级为 no-op。
	if runtime.Stores != nil && runtime.Stores.LearningStore != nil {
		summaryDaemon := kernel.NewSummaryDaemon(kernel.SummaryDaemonConfig{
			Bus:          globalEventBus,
			Runs:         runtime.Stores.RunStore,
			Store:        runtime.Stores.LearningStore,
			Summarizer:   ctxEngine.Summarizer,
			SummaryModel: ctxEngine.SummaryModel,
		})
		if err := summaryDaemon.Start(serveCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: warning: summary daemon start: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "brain serve: daily summary daemon active\n")
			defer summaryDaemon.Stop()
		}
	}
	if runtime.Stores != nil && runtime.Stores.SharedMessageStore != nil {
		ctxEngine.SharedStore = runtime.Stores.SharedMessageStore
	}

	// 加载组织级授权策略（如果 ~/.brain/org-policy.json 存在）
	// 优先尝试 EnterpriseEnforcer（包含权限矩阵 + 吊销检查），回退到 FileOrgPolicyEnforcer。
	var orgEnforcer kernel.OrgPolicyEnforcer
	if enforcer, err := kernel.LoadOrgPolicyIfExists(); err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: org policy load: %v\n", err)
	} else if enforcer != nil {
		// 尝试升级为 EnterpriseEnforcer
		orgPolicyPath := kernel.DefaultOrgPolicyPath()
		ee, eeErr := kernel.NewEnterpriseEnforcer(kernel.EnterpriseConfig{
			OrgPolicyPath: orgPolicyPath,
		})
		if eeErr == nil {
			orgEnforcer = ee
		} else {
			orgEnforcer = enforcer
		}
		if p := orgEnforcer.Policy(); p != nil {
			fmt.Fprintf(os.Stderr, "  org:       %s (audit: %s)\n", p.OrgID, p.AuditLevel)
		}
	}

	// 任务级调度器：基于 L1 学习排名选择 brain
	scheduler := kernel.NewDefaultTaskScheduler(learner, func() []agent.Kind {
		return pool.AvailableKinds()
	})

	// 远程 brain 连接池（D-3）：从配置 remote_brains 初始化
	var remotePool *kernel.RemoteBrainPool
	if cfg != nil && len(cfg.RemoteBrains) > 0 {
		var remoteCfgs []*kernel.RemoteBrainConfig
		for _, rb := range cfg.RemoteBrains {
			timeout := 30 * time.Second
			if rb.Timeout != "" {
				if d, err := time.ParseDuration(rb.Timeout); err == nil {
					timeout = d
				}
			}
			remoteCfgs = append(remoteCfgs, &kernel.RemoteBrainConfig{
				Kind:      agent.Kind(rb.Kind),
				Endpoint:  rb.Endpoint,
				APIKey:    rb.APIKey,
				Timeout:   timeout,
				AutoStart: rb.AutoStart,
			})
		}
		if rp, err := kernel.NewRemoteBrainPool(remoteCfgs); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: warning: remote pool: %v\n", err)
		} else {
			remotePool = rp
			fmt.Fprintf(os.Stderr, "  remote:    %d brain(s) configured\n", len(remoteCfgs))
			remotePool.AutoStart(serveCtx)
		}
	}

	mgr := &runManager{
		store:        runtime.RunStore,
		pool:         pool,
		eventBus:     globalEventBus,
		leaseManager: leaseManager,
		capMatcher:   capMatcher,
		learner:      learner,
		ctxEngine:    ctxEngine,
		orgEnforcer:  orgEnforcer,
		scheduler:    scheduler,
		remotePool:   remotePool,
		rootCtx:      serveCtx,
	}

	// Task #16: 人类接管协调器。工具层 human.request_takeover 阻塞等待,
	// /v1/executions/{id}/resume|abort 唤醒。复用 TaskExecution.StatePaused +
	// globalEventBus,不另造通知机制。
	humanCoord := newHostHumanTakeoverCoordinator(mgr, globalEventBus)
	tool.SetHumanTakeoverCoordinator(humanCoord)

	mux := http.NewServeMux()

	// GET /health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// GET /v1/version
	mux.HandleFunc("/v1/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cli.VersionInfo{
			CLIVersion:      brain.CLIVersion,
			ProtocolVersion: brain.ProtocolVersion,
			KernelVersion:   brain.KernelVersion,
			SDKLanguage:     brain.SDKLanguage,
			SDKVersion:      brain.SDKVersion,
		})
	})

	// GET /v1/tools
	mux.HandleFunc("/v1/tools", func(w http.ResponseWriter, r *http.Request) {
		if runtime.Kernel.ToolRegistry == nil {
			http.Error(w, "tool registry not available", http.StatusServiceUnavailable)
			return
		}
		tools := runtime.Kernel.ToolRegistry.List()
		type toolItem struct {
			Name        string `json:"name"`
			Brain       string `json:"brain"`
			Description string `json:"description"`
		}
		items := make([]toolItem, 0, len(tools))
		for _, t := range tools {
			items = append(items, toolItem{
				Name:        t.Name(),
				Brain:       t.Schema().Brain,
				Description: t.Schema().Description,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tools": items, "total": len(items)})
	})

	// POST /v1/runs — submit a new Run
	// GET  /v1/runs — list all Runs
	mux.HandleFunc("/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateRun(w, r, mgr, runtime, cfg, *maxRuns, mode, env.Workdir, runWorkdirPolicy)
		case http.MethodGet:
			handleListRuns(w, r, mgr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /v1/runs/:id — query Run status
	// DELETE /v1/runs/:id — cancel Run
	mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		if id == "" {
			http.Error(w, "missing run id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleGetRun(w, r, mgr, id)
		case http.MethodDelete:
			handleCancelRun(w, r, mgr, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// ---------------------------------------------------------------
	// v3 Execution 路由族 — /v1/executions
	// /v1/runs 保留为别名，/v1/executions 是 v3 规范入口
	// ---------------------------------------------------------------

	// POST /v1/executions — 创建执行（包装 handleCreateRun，添加 mode/lifecycle/restart 字段）
	// GET  /v1/executions — 列出所有执行
	mux.HandleFunc("/v1/executions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleCreateExecution(w, r, mgr, runtime, cfg, *maxRuns, mode, env.Workdir, runWorkdirPolicy)
		case http.MethodGet:
			handleListExecutions(w, r, mgr)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET    /v1/executions/{id}        — 查询执行状态
	// POST   /v1/executions/{id}/stop   — 停止执行
	// GET    /v1/executions/{id}/events — SSE 事件流
	mux.HandleFunc("/v1/executions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/executions/")
		if path == "" {
			http.Error(w, "missing execution id", http.StatusBadRequest)
			return
		}

		// 解析子路径：{id}/stop 或 {id}/events
		parts := strings.SplitN(path, "/", 2)
		id := parts[0]
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}

		switch {
		case sub == "stop" && r.Method == http.MethodPost:
			handleStopExecution(w, r, mgr, id)
		case sub == "events" && r.Method == http.MethodGet:
			handleExecutionEvents(w, r, mgr, id)
		case sub == "resume" && r.Method == http.MethodPost:
			handleResumeExecution(w, r, humanCoord, id)
		case sub == "abort" && r.Method == http.MethodPost:
			handleAbortExecution(w, r, humanCoord, id)
		case sub == "" && r.Method == http.MethodGet:
			handleGetExecution(w, r, mgr, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Dashboard API 路由
	serverStart := time.Now().UTC()
	// Task #17: LearningProvider 聚合 PatternLibrary + LearningStore 供 Dashboard。
	var learningProvider dashboard.LearningProvider
	if runtime.Stores != nil && runtime.Stores.LearningStore != nil {
		learningProvider = &learningProviderAdapter{
			patternLib: tool.SharedPatternLibrary(),
			learning:   runtime.Stores.LearningStore,
		}
	}
	wsHub := registerDashboardRoutes(mux, mgr, pool, globalEventBus, cfg, serverStart, leaseManager, learningProvider)
	if wsHub != nil {
		wsHub.Start(context.Background())
	}

	// Dashboard 静态文件服务（嵌入式 SPA）
	staticFS, _ := iofs.Sub(dashboard.StaticFS, "static")
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", http.FileServer(http.FS(staticFS))))
	// 根路径重定向到 dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	server := &http.Server{
		Addr:         *listen,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brain serve: listen: %v\n", err)
		return cli.ExitNoPerm
	}

	fmt.Fprintf(os.Stderr, "Listening on %s\n", ln.Addr())
	fmt.Fprintln(os.Stderr, "  HTTP  ready")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Kernel is ready to accept connections. Press Ctrl-C to stop.")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(ln) }()

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "\nReceived %s, shutting down gracefully...\n", sig)
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutCancel()
		if err := server.Shutdown(shutCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: shutdown: %v\n", err)
		}
		serveCancel()
		mgr.cancelAll("server shutdown")
		if err := mgr.wait(shutCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: drain: %v\n", err)
		}
		// 保存学习数据到 SQLite
		if err := learner.Save(shutCtx); err != nil {
			fmt.Fprintf(os.Stderr, "brain serve: save learning data: %v\n", err)
		}
		if mgr.remotePool != nil {
			mgr.remotePool.Shutdown(shutCtx)
		}
		if runtime.Stores != nil {
			runtime.Stores.Close()
		}
		fmt.Fprintln(os.Stderr, "Kernel stopped.")
		if sig == syscall.SIGTERM {
			return cli.ExitSignalTerm
		}
		return cli.ExitOK
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "brain serve: %v\n", err)
			return cli.ExitSoftware
		}
		return cli.ExitOK
	}
}

// --- Run lifecycle handlers ---

type createRunRequest struct {
	Prompt      string            `json:"prompt"`
	Brain       string            `json:"brain"`
	MaxTurns    int               `json:"max_turns"`
	Stream      bool              `json:"stream"`
	Workdir     string            `json:"workdir,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
	ModelConfig *modelConfigInput `json:"model_config,omitempty"`
	FilePolicy  *filePolicyInput  `json:"file_policy,omitempty"`

	// v3 execution 扩展字段（可选，默认值兼容旧行为）
	Mode          string `json:"mode,omitempty"`           // interactive / background，默认 interactive
	Lifecycle     string `json:"lifecycle,omitempty"`      // oneshot / daemon / watch，默认 oneshot
	Restart       string `json:"restart,omitempty"`        // never / on-failure / always，默认 never
	WatchInterval string `json:"watch_interval,omitempty"` // watch 模式执行间隔，如 "60s"/"5m"，默认 60s

	timeoutDuration       time.Duration `json:"-"`
	watchIntervalDuration time.Duration `json:"-"`
}

func handleCreateRun(w http.ResponseWriter, r *http.Request, mgr *runManager, runtime *cliRuntime, cfg *brainConfig, maxConcurrent int, mode permissionMode, defaultWorkdir string, workdirPolicy serveWorkdirPolicy) {
	var req createRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if req.Prompt == "" {
		http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
		return
	}
	if req.Brain == "" {
		req.Brain = "central"
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = 20
	}
	effectiveWorkdir, err := resolveServeRunWorkdir(defaultWorkdir, req.Workdir, workdirPolicy)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	req.Workdir = effectiveWorkdir
	req.timeoutDuration, err = resolveRunTimeoutWithConfig(cfg, req.Timeout, 5*time.Minute)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}

	// Resolve provider config.
	cfgFile, cfgErr := loadConfig()
	explicitProviderInput := hasModelConfigOverrides(req.ModelConfig)
	if cfgFile == nil && !wantsMockProvider("", req.ModelConfig) && !explicitProviderInput && os.Getenv("ANTHROPIC_API_KEY") == "" {
		msg := "no config available"
		if cfgErr != nil {
			msg = cfgErr.Error()
		}
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, msg), http.StatusInternalServerError)
		return
	}
	providerSession := openMockProvider("hello from mock provider")
	if !wantsMockProvider("", req.ModelConfig) {
		providerSession, err = openConfiguredProvider(cfgFile, req.Brain, req.ModelConfig, "", "", "", "")
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	// Validate execution environment BEFORE creating the run record,
	// so failed validation doesn't leave orphan "running" records.
	env := newExecutionEnvironment(req.Workdir, mode, cfg, nil, false)
	req.FilePolicy = resolveFilePolicyInput(cfg, req.FilePolicy)
	if err := applyFilePolicy(env, req.FilePolicy); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	if mode == modeRestricted && env.FilePolicy == nil {
		http.Error(w, `{"error":"restricted mode requires file_policy (config or request body)"}`, http.StatusBadRequest)
		return
	}

	// 组织级策略检查
	if mgr.orgEnforcer != nil {
		orgAction := kernel.OrgAction{
			Type:      "start_run",
			BrainKind: req.Brain,
		}
		if err := mgr.orgEnforcer.Check(r.Context(), orgAction); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusForbidden)
			return
		}
		// 如果组织策略指定了 MaxConcurrent 且小于服务端限制，使用组织限制
		if p := mgr.orgEnforcer.Policy(); p != nil && p.MaxConcurrent > 0 && p.MaxConcurrent < maxConcurrent {
			maxConcurrent = p.MaxConcurrent
		}
	}

	if !mgr.reserveSlot(maxConcurrent) {
		http.Error(w, `{"error":"max concurrent runs reached"}`, http.StatusTooManyRequests)
		return
	}

	runRec, err := runtime.RunStore.Create(req.Brain, req.Prompt, string(mode), req.Workdir)
	if err != nil {
		mgr.releaseSlot()
		http.Error(w, fmt.Sprintf(`{"error":"create run record: %s"}`, err), http.StatusInternalServerError)
		return
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if req.timeoutDuration > 0 {
		ctx, cancel = context.WithTimeout(mgr.rootCtx, req.timeoutDuration)
	} else {
		ctx, cancel = context.WithCancel(mgr.rootCtx)
	}

	// 设置 v3 execution 默认值
	if req.Mode == "" {
		req.Mode = "interactive"
	}
	if req.Lifecycle == "" {
		req.Lifecycle = "oneshot"
	}
	if req.Restart == "" {
		req.Restart = "never"
	}

	// 解析 watch 模式的执行间隔
	if req.Lifecycle == "watch" {
		if req.WatchInterval == "" {
			req.watchIntervalDuration = 60 * time.Second
		} else {
			dur, err := time.ParseDuration(req.WatchInterval)
			if err != nil {
				cancel() // 释放 context，避免泄漏
				http.Error(w, fmt.Sprintf(`{"error":"invalid watch_interval: %s"}`, err), http.StatusBadRequest)
				return
			}
			if dur < 5*time.Second {
				dur = 5 * time.Second // 最小 5 秒间隔
			}
			req.watchIntervalDuration = dur
		}
	}

	te := kernel.NewTaskExecution(kernel.TaskExecutionConfig{
		BrainID:   req.Brain,
		Mode:      kernel.ExecutionMode(req.Mode),
		Lifecycle: kernel.LifecyclePolicy(req.Lifecycle),
		Restart:   kernel.RestartPolicy(req.Restart),
		Bus:       mgr.eventBus, // Task #19: 让 Transition 发事件到外部 hooks
	})
	entry := &runEntry{
		ID:          runRec.ID,
		ExecutionID: runRec.ID,
		Status:      "running",
		Brain:       req.Brain,
		Prompt:      req.Prompt,
		CreatedAt:   time.Now().UTC(),
		Mode:        req.Mode,
		Lifecycle:   req.Lifecycle,
		Restart:     req.Restart,
		taskExec:    te,
		cancel:      cancel,
	}
	_ = runtime.RunStore.AppendEvent(runRec.ID, "run.accepted", "run accepted by serve API", nil)
	mgr.launchReserved(entry, func() {
		executeRun(ctx, entry, mgr, runtime, providerSession, req, runRec, cfg, mode)
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	// 响应中同时返回 run_id 和 execution_id（值相同）
	json.NewEncoder(w).Encode(map[string]string{"run_id": runRec.ID, "execution_id": runRec.ID, "status": "running"})
}

func executeRun(ctx context.Context, entry *runEntry, mgr *runManager, runtime *cliRuntime, providerSession providerSession, req createRunRequest, runRec *persistedRunRecord, cfg *brainConfig, mode permissionMode) {
	// 复用 runEntry 中创建的 TaskExecution 状态机实例
	te := entry.taskExec
	_ = te.Transition(kernel.StateRunning)

	env := newExecutionEnvironment(runRec.Workdir, mode, cfg, nil, false)
	_ = applyFilePolicy(env, req.FilePolicy)

	// 使用全局 BrainPool 创建轻量 Orchestrator（共享 sidecar 进程）。
	var orch *kernel.Orchestrator
	if req.Brain == "central" && !wantsMockProvider("", req.ModelConfig) && mgr.pool != nil {
		llmProxy := &kernel.LLMProxy{
			ProviderFactory: func(kind agent.Kind) llm.Provider {
				session, err := openConfiguredProvider(cfg, string(kind), req.ModelConfig, "", "", "", "")
				if err != nil {
					return nil
				}
				return session.Provider
			},
		}
		orch = kernel.NewOrchestratorWithPool(mgr.pool, &kernel.ProcessRunner{BinResolver: defaultBinResolver()}, llmProxy, defaultBinResolver(), kernel.OrchestratorConfig{},
			kernel.WithSemanticApprover(&kernel.DefaultSemanticApprover{}),
			kernel.WithCapabilityMatcher(mgr.capMatcher),
			kernel.WithLearningEngine(mgr.learner),
			kernel.WithContextEngine(mgr.ctxEngine),
		)
	}

	runReg := buildManagedRegistry(cfg, env, req.Brain, func(reg tool.Registry) {
		registerDelegateToolForEnvironment(reg, orch, env)
		registerSpecialistBridgeTools(reg, orch)
	})
	systemPrompt := buildSystemPrompt(mode, env.Sandbox)
	if orch != nil {
		systemPrompt += buildOrchestratorPrompt(orch, runReg)
	}

	// background 模式：不限 timeout（清除 caller 设的 deadline）
	if te.Mode == kernel.ModeBackground && req.timeoutDuration == 0 {
		req.timeoutDuration = 0
	}

	var batchPlanner loop.ToolBatchPlanner
	if mgr.leaseManager != nil {
		batchPlanner = newBatchPlannerAdapter(mgr.leaseManager)
	}

	execOnce := func() (outcome *managedRunOutcome, err error) {
		return executeManagedRun(ctx, managedRunExecution{
			Runtime:       runtime,
			Record:        runRec,
			Registry:      runReg,
			Provider:      providerSession.Provider,
			ProviderName:  providerSession.Name,
			ProviderModel: providerSession.Model,
			BrainID:       req.Brain,
			Prompt:        req.Prompt,
			MaxTurns:      req.MaxTurns,
			MaxDuration:   req.timeoutDuration,
			Stream:        req.Stream,
			SystemPrompt:  systemPrompt,
			EventBus:      mgr.eventBus,
			BatchPlanner:  batchPlanner,
			MessageCompressor: func(compCtx context.Context, msgs []llm.Message, budget int) ([]llm.Message, error) {
				if mgr.ctxEngine == nil {
					return msgs, nil
				}
				return mgr.ctxEngine.Compress(compCtx, msgs, budget)
			},
			TokenBudget: 100000,
		})
	}

	for {
		outcome, err := execOnce()

		if err != nil {
			errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
			if ctx.Err() != nil || entry.status() == "cancelled" {
				_ = te.Transition(kernel.StateCanceled)
				entry.finish("cancelled", errJSON)
				mgr.runs.Store(entry.ID, entry)
				return
			}
			_ = te.Transition(kernel.StateFailed)

			// restart 策略：失败后自动重启
			if te.ShouldRestart() {
				te.IncrementRestart()
				_ = te.Transition(kernel.StateRestarting)
				_ = te.Transition(kernel.StatePending)
				_ = te.Transition(kernel.StateRunning)
				fmt.Fprintf(os.Stderr, "serve: execution %s restarting (attempt %d)\n", entry.ID, te.RestartCount)
				continue
			}
			entry.finish("failed", errJSON)
			mgr.runs.Store(entry.ID, entry)
			return
		}

		// daemon 模式：执行完一轮后立即重新执行，直到被 cancel/stop
		if te.Lifecycle == kernel.LifecycleDaemon {
			// 先回到 pending/running 状态准备下一轮
			_ = te.Transition(kernel.StateCompleted)
			te.IncrementRestart()
			entry.finish("running", outcome.SummaryJSON)
			mgr.runs.Store(entry.ID, entry)
			fmt.Fprintf(os.Stderr, "serve: daemon execution %s completed round %d, restarting\n", entry.ID, te.RestartCount)

			// 检查 context 是否已取消
			select {
			case <-ctx.Done():
				entry.finish("completed", outcome.SummaryJSON)
				mgr.runs.Store(entry.ID, entry)
				return
			default:
			}

			// 重置状态机以开始下一轮
			_ = te.Transition(kernel.StateRestarting)
			_ = te.Transition(kernel.StatePending)
			_ = te.Transition(kernel.StateRunning)
			continue
		}

		// watch 模式：执行完后等待指定间隔再重新执行
		if te.Lifecycle == kernel.LifecycleWatch {
			_ = te.Transition(kernel.StateCompleted)
			te.IncrementRestart()
			interval := req.watchIntervalDuration
			if interval <= 0 {
				interval = 60 * time.Second
			}
			entry.finish("waiting", outcome.SummaryJSON)
			entry.mu.Lock()
			entry.Status = "waiting"
			entry.mu.Unlock()
			mgr.runs.Store(entry.ID, entry)
			fmt.Fprintf(os.Stderr, "serve: watch execution %s completed round %d, next in %s\n", entry.ID, te.RestartCount, interval)

			// 等待间隔或 context 取消
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				entry.finish("completed", outcome.SummaryJSON)
				mgr.runs.Store(entry.ID, entry)
				return
			case <-timer.C:
			}

			// 重置状态机以开始下一轮
			_ = te.Transition(kernel.StateRestarting)
			_ = te.Transition(kernel.StatePending)
			_ = te.Transition(kernel.StateRunning)
			entry.mu.Lock()
			entry.Status = "running"
			entry.mu.Unlock()
			mgr.runs.Store(entry.ID, entry)
			continue
		}

		// oneshot：正常完成后退出
		_ = te.Transition(kernel.StateCompleted)
		entry.finish(outcome.FinalStatus, outcome.SummaryJSON)
		mgr.runs.Store(entry.ID, entry)
		return
	}
}

func handleGetRun(w http.ResponseWriter, _ *http.Request, mgr *runManager, id string) {
	entry, ok := mgr.get(id)
	if !ok {
		http.Error(w, `{"error":"run not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry.snapshot())
}

func handleCancelRun(w http.ResponseWriter, _ *http.Request, mgr *runManager, id string) {
	entry, ok := mgr.get(id)
	if !ok {
		http.Error(w, `{"error":"run not found"}`, http.StatusNotFound)
		return
	}
	status := entry.markCancelled()
	if mgr.store != nil && status == "cancelled" {
		data, _ := json.Marshal(map[string]string{"reason": "api cancel"})
		_ = mgr.store.AppendEvent(id, "run.cancel.requested", "api cancel", data)
		_, _ = mgr.store.Finish(id, status, entry.Result, "")
	}
	mgr.runs.Store(id, entry)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"run_id": id, "status": status})
}

func handleListRuns(w http.ResponseWriter, _ *http.Request, mgr *runManager) {
	runs := mgr.list()
	if runs == nil {
		runs = []*runEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"runs": runs})
}

// --- v3 Execution 端点处理函数 ---

// handleCreateExecution 创建一个新的 execution（包装 handleCreateRun，添加 mode/lifecycle/restart）。
func handleCreateExecution(w http.ResponseWriter, r *http.Request, mgr *runManager, runtime *cliRuntime, cfg *brainConfig, maxConcurrent int, mode permissionMode, defaultWorkdir string, workdirPolicy serveWorkdirPolicy) {
	// 直接复用 handleCreateRun，因为 createRunRequest 已包含 mode/lifecycle/restart 字段
	handleCreateRun(w, r, mgr, runtime, cfg, maxConcurrent, mode, defaultWorkdir, workdirPolicy)
}

// handleListExecutions 列出所有 executions（复用 handleListRuns）。
func handleListExecutions(w http.ResponseWriter, r *http.Request, mgr *runManager) {
	runs := mgr.list()
	if runs == nil {
		runs = []*runEntry{}
	}
	// 以 executions 为 key 返回，与 /v1/runs 的 runs key 区分
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"executions": runs})
}

// handleGetExecution 查询单个 execution 状态（复用 handleGetRun，额外返回 execution_id）。
func handleGetExecution(w http.ResponseWriter, _ *http.Request, mgr *runManager, id string) {
	entry, ok := mgr.get(id)
	if !ok {
		http.Error(w, `{"error":"execution not found"}`, http.StatusNotFound)
		return
	}
	snap := entry.snapshot()
	// 确保 execution_id 字段有值
	if snap.ExecutionID == "" {
		snap.ExecutionID = snap.ID
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

// handleStopExecution 停止一个 execution（复用 handleCancelRun 逻辑）。
func handleStopExecution(w http.ResponseWriter, _ *http.Request, mgr *runManager, id string) {
	entry, ok := mgr.get(id)
	if !ok {
		http.Error(w, `{"error":"execution not found"}`, http.StatusNotFound)
		return
	}
	status := entry.markCancelled()
	if mgr.store != nil && status == "cancelled" {
		data, _ := json.Marshal(map[string]string{"reason": "api stop execution"})
		_ = mgr.store.AppendEvent(id, "execution.stop.requested", "api stop execution", data)
		_, _ = mgr.store.Finish(id, status, entry.Result, "")
	}
	mgr.runs.Store(id, entry)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"execution_id": id, "status": status})
}

// handleExecutionEvents 提供 SSE 事件流端点，客户端可实时接收 execution 相关事件。
func handleExecutionEvents(w http.ResponseWriter, r *http.Request, mgr *runManager, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := mgr.eventBus.Subscribe(r.Context(), id)
	defer cancel()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
