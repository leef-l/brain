package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/cli"
	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/tool"
)

// runEntry tracks an in-flight or finished Run.
type runEntry struct {
	mu        sync.Mutex      `json:"-"`
	ID        string          `json:"run_id"`
	Status    string          `json:"status"`
	Brain     string          `json:"brain,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	CreatedAt time.Time       `json:"created_at"`

	cancel context.CancelFunc `json:"-"`
}

func (e *runEntry) snapshot() *runEntry {
	e.mu.Lock()
	defer e.mu.Unlock()

	return &runEntry{
		ID:        e.ID,
		Status:    e.Status,
		Brain:     e.Brain,
		Prompt:    e.Prompt,
		Result:    append(json.RawMessage(nil), e.Result...),
		CreatedAt: e.CreatedAt,
	}
}

func (e *runEntry) status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Status
}

func (e *runEntry) markCancelled() string {
	e.mu.Lock()
	if e.Status != "running" {
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
	runs    sync.Map // id → *runEntry
	store   *runtimeStore
	rootCtx context.Context
	wg      sync.WaitGroup
}

func (rm *runManager) get(id string) (*runEntry, bool) {
	v, ok := rm.runs.Load(id)
	if ok {
		return v.(*runEntry), true
	}
	if rm.store == nil {
		return nil, false
	}
	rec, ok := rm.store.get(id)
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
		records := rm.store.list(0, "all")
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
	count := 0
	rm.runs.Range(func(_, v interface{}) bool {
		if v.(*runEntry).status() == "running" {
			count++
		}
		return true
	})
	return count
}

func (rm *runManager) launch(entry *runEntry, fn func()) {
	rm.runs.Store(entry.ID, entry)
	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		fn()
	}()
}

func (rm *runManager) cancelAll(reason string) {
	rm.runs.Range(func(_, v interface{}) bool {
		entry := v.(*runEntry)
		status := entry.markCancelled()
		if rm.store != nil && status == "cancelled" {
			data, _ := json.Marshal(map[string]string{"reason": reason})
			_ = rm.store.appendEvent(entry.ID, "run.cancel.requested", reason, data)
			_, _ = rm.store.finish(entry.ID, "cancelled", entry.Result, reason)
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

	startupOrch := buildOrchestrator(orchestratorConfig{cfg: cfg})
	defer func() {
		if startupOrch != nil {
			_ = startupOrch.Shutdown(context.Background())
		}
	}()
	runtime.Kernel.ToolRegistry = buildManagedRegistry(cfg, env, "central", func(reg tool.Registry) {
		registerDelegateToolForEnvironment(reg, startupOrch, env)
	})

	fmt.Fprintf(os.Stderr, "Starting Brain Kernel (cluster mode)\n")
	fmt.Fprintf(os.Stderr, "  listen:    %s\n", *listen)
	fmt.Fprintf(os.Stderr, "  max_runs:  %d\n", *maxRuns)
	fmt.Fprintf(os.Stderr, "  mode:      %s\n", mode)
	fmt.Fprintf(os.Stderr, "  workdir:   %s\n", env.workdir)
	fmt.Fprintf(os.Stderr, "  run_wd:    %s\n", runWorkdirPolicy)
	fmt.Fprintf(os.Stderr, "  store:     %s\n\n", runtime.FileStore.Path())

	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "brain serve: warning: config load: %v\n", cfgErr)
	}
	mgr := &runManager{store: runtime.RunStore, rootCtx: serveCtx}

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
			handleCreateRun(w, r, mgr, runtime, cfg, *maxRuns, mode, env.workdir, runWorkdirPolicy)
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

	timeoutDuration time.Duration `json:"-"`
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

	// Check concurrency limit.
	if mgr.runningCount() >= maxConcurrent {
		http.Error(w, `{"error":"max concurrent runs reached"}`, http.StatusTooManyRequests)
		return
	}

	// Resolve provider config.
	cfgFile, cfgErr := loadConfig()
	explicitProviderInput := hasModelConfigOverrides(req.ModelConfig)
	if cfgFile == nil && !wantsMockProvider("", req.ModelConfig) && !explicitProviderInput {
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

	runRec, err := runtime.RunStore.create(req.Brain, req.Prompt, string(mode), req.Workdir)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"create run record: %s"}`, err), http.StatusInternalServerError)
		return
	}
	env := newExecutionEnvironment(req.Workdir, mode, cfg, nil, false)
	req.FilePolicy = resolveFilePolicyInput(cfg, req.FilePolicy)
	if err := applyFilePolicy(env, req.FilePolicy); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusBadRequest)
		return
	}
	if mode == modeRestricted && env.filePolicy == nil {
		http.Error(w, `{"error":"restricted mode requires file_policy (config or request body)"}`, http.StatusBadRequest)
		return
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if req.timeoutDuration > 0 {
		ctx, cancel = context.WithTimeout(mgr.rootCtx, req.timeoutDuration)
	} else {
		ctx, cancel = context.WithCancel(mgr.rootCtx)
	}

	entry := &runEntry{
		ID:        runRec.ID,
		Status:    "running",
		Brain:     req.Brain,
		Prompt:    req.Prompt,
		CreatedAt: time.Now().UTC(),
		cancel:    cancel,
	}
	_ = runtime.RunStore.appendEvent(runRec.ID, "run.accepted", "run accepted by serve API", nil)
	mgr.launch(entry, func() {
		executeRun(ctx, entry, mgr, runtime, providerSession, req, runRec, cfg, mode)
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"run_id": runRec.ID, "status": "running"})
}

func executeRun(ctx context.Context, entry *runEntry, mgr *runManager, runtime *cliRuntime, providerSession providerSession, req createRunRequest, runRec *persistedRunRecord, cfg *brainConfig, mode permissionMode) {
	env := newExecutionEnvironment(runRec.Workdir, mode, cfg, nil, false)
	_ = applyFilePolicy(env, req.FilePolicy)

	var orch *kernel.Orchestrator
	if req.Brain == "central" && !wantsMockProvider("", req.ModelConfig) {
		orch = buildOrchestrator(orchestratorConfig{
			cfg:         cfg,
			modelConfig: req.ModelConfig,
		})
	}
	defer func() {
		if orch != nil {
			_ = orch.Shutdown(context.Background())
		}
	}()

	runReg := buildManagedRegistry(cfg, env, req.Brain, func(reg tool.Registry) {
		registerDelegateToolForEnvironment(reg, orch, env)
	})
	systemPrompt := buildSystemPrompt(mode, env.sandbox)
	if orch != nil {
		systemPrompt += buildOrchestratorPrompt(orch, runReg)
	}

	outcome, err := executeManagedRun(ctx, managedRunExecution{
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
	})

	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		status := "failed"
		if ctx.Err() != nil || entry.status() == "cancelled" {
			status = "cancelled"
		}
		entry.finish(status, errJSON)
	} else {
		entry.finish(outcome.FinalStatus, outcome.SummaryJSON)
	}
	mgr.runs.Store(entry.ID, entry)
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
		_ = mgr.store.appendEvent(id, "run.cancel.requested", "api cancel", data)
		_, _ = mgr.store.finish(id, status, entry.Result, "")
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
