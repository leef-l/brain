package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/sdk/agent"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/protocol"
)

// ProcessRunner implements BrainRunner by fork/exec-ing a sidecar binary
// and communicating with it over stdin/stdout pipes using the Content-Length
// framed protocol from 20-协议规格.md §2.
//
// The lifecycle:
//  1. Start: exec the binary, connect pipes, run initialize handshake
//  2. The returned Agent holds the transport + RPC session
//  3. Stop: send shutdown, wait for exit, clean up
//
// See 02-BrainKernel设计.md §12.5 and 20-协议规格.md §6.
type ProcessRunner struct {
	// BinPath is the path to the sidecar binary to exec.
	// If empty, it is resolved from the brain kind via BinResolver.
	BinPath string

	// BinResolver maps a brain Kind to a binary path. Used when BinPath
	// is empty. Returns ("", error) when the kind is unknown.
	BinResolver func(kind agent.Kind) (string, error)

	// Env is the environment variables to pass to the sidecar process.
	// If nil, the current process environment is inherited.
	Env []string

	// Args are optional extra argv entries passed to the sidecar process.
	Args []string

	// InitTimeout is the deadline for the initialize handshake.
	// Defaults to 30s per 20-协议规格.md §6.4.
	InitTimeout time.Duration

	// ShutdownTimeout is the grace period for the shutdown handshake.
	// Defaults to 10s.
	ShutdownTimeout time.Duration

	// ProtocolVersion is the protocol version to advertise in the
	// initialize request. Defaults to "1.0".
	ProtocolVersion string

	// KernelVersion is the kernel version to advertise. Defaults to
	// brain.KernelVersion.
	KernelVersion string

	mu        sync.Mutex
	processes map[agent.Kind]*processAgent
}

// processAgent is the Kernel-side handle for a running sidecar process.
// It implements agent.Agent and holds the transport, RPC session, process
// handle, and the descriptor returned by the initialize handshake.
type processAgent struct {
	kind       agent.Kind
	desc       agent.Descriptor
	cmd        *exec.Cmd
	transport  *StdioTransport
	rpc        protocol.BidirRPC
	sidecar    *protocol.SidecarInstance
	cancelFunc context.CancelFunc
	logFile    io.Closer // stderr log file, closed on Shutdown

	mu    sync.Mutex
	ready bool
	done  chan struct{}

	// exited is closed when the process exits (via the background Wait goroutine).
	// ProcessExited checks this to detect crashed sidecars cross-platform.
	exited chan struct{}
}

// Kind returns the brain role.
func (a *processAgent) Kind() agent.Kind { return a.kind }

// Descriptor returns the handshake descriptor.
func (a *processAgent) Descriptor() agent.Descriptor { return a.desc }

// Ready blocks until the sidecar has completed initialize.
func (a *processAgent) Ready(ctx context.Context) error {
	a.mu.Lock()
	if a.ready {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	select {
	case <-a.done:
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.ready {
			return nil
		}
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("sidecar exited before becoming ready"))
	case <-ctx.Done():
		return brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("Ready: context cancelled"))
	}
}

// Shutdown sends a shutdown request and waits for the sidecar to exit.
// If the process doesn't exit within 3 seconds after graceful shutdown,
// it is forcibly killed. This ensures cleanup on all platforms including
// Windows where pipe closure alone may not terminate the process.
func (a *processAgent) Shutdown(ctx context.Context) error {
	if a.rpc != nil {
		_ = a.rpc.Notify(ctx, protocol.MethodShutdown, nil)
	}
	if a.sidecar != nil {
		_ = a.sidecar.TransitionTo(protocol.StateDraining)
	}
	if a.transport != nil {
		_ = a.transport.Close()
	}
	if a.sidecar != nil {
		_ = a.sidecar.TransitionTo(protocol.StateClosed)
	}
	if a.cmd != nil && a.cmd.Process != nil {
		// Wait for the process to exit. The background goroutine started
		// in Start() calls cmd.Wait() and closes a.exited on completion.
		grace := 3 * time.Second
		timer := time.NewTimer(grace)
		defer timer.Stop()

		select {
		case <-a.exited:
			// Process exited cleanly.
		case <-timer.C:
			// Grace period expired — force kill.
			_ = a.cmd.Process.Kill()
			<-a.exited
		case <-ctx.Done():
			_ = a.cmd.Process.Kill()
			<-a.exited
		}
	}
	if a.cancelFunc != nil {
		a.cancelFunc()
	}
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
	return nil
}

// Start launches a sidecar process, connects stdio pipes, and runs the
// initialize handshake. On success the returned Agent is ready for work.
func (r *ProcessRunner) Start(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error) {
	binPath := r.BinPath
	if binPath == "" && r.BinResolver != nil {
		var err error
		binPath, err = r.BinResolver(kind)
		if err != nil {
			return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
				brainerrors.WithMessage(fmt.Sprintf("resolve binary for %s: %v", kind, err)))
		}
	}
	if binPath == "" {
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("no binary path for brain kind %s", kind)))
	}

	initTimeout := r.InitTimeout
	if initTimeout <= 0 {
		initTimeout = 30 * time.Second
	}

	protoVer := r.ProtocolVersion
	if protoVer == "" {
		protoVer = "1.0"
	}
	kernelVer := r.KernelVersion
	if kernelVer == "" {
		kernelVer = brain.KernelVersion
	}

	// Create the process. Use a background context so the sidecar lifetime
	// is NOT tied to the caller's request context (which may have a short
	// timeout). The handshake still uses the caller ctx via initCtx below.
	cmd := exec.Command(binPath, r.Args...)
	cmd.Env = mergeEnvLists(os.Environ(), r.Env)

	// Auto-inject sidecar config paths from ~/.brain/<kind>-brain.yaml
	// if the corresponding env var is not already set.
	cmd.Env = injectSidecarConfigEnv(cmd.Env, kind)
	cmd.Env = injectSidecarPersistenceEnv(cmd.Env)

	// On Linux, ask the kernel to send SIGTERM to the child when the
	// parent process dies. This prevents orphan sidecar processes when
	// the user kills brain chat with Ctrl+C/Ctrl+D/kill.
	setSidecarDeathSignal(cmd)

	// Redirect sidecar stderr to a log file instead of polluting the
	// interactive chat/run UI. Logs go to ~/.brain/logs/<kind>.log.
	logWriter, logCloser := openSidecarLog(kind)
	cmd.Stderr = logWriter

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("create stdin pipe: %v", err)))
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("create stdout pipe: %v", err)))
	}

	if err := cmd.Start(); err != nil {
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("exec %s: %v", binPath, err)))
	}

	// Wire up transport and RPC.
	transport := NewStdioTransport(stdoutPipe, stdinPipe)
	rpcReader := protocol.NewFrameReader(stdoutPipe)
	rpcWriter := protocol.NewFrameWriter(stdinPipe)
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, rpcReader, rpcWriter)

	// Use a background-derived context for the RPC session so it survives
	// after the caller's ctx (which may be a short-lived start request) ends.
	agentCtx, cancel := context.WithCancel(context.Background())
	if err := rpc.Start(agentCtx); err != nil {
		cancel()
		cmd.Process.Kill()
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("start RPC: %v", err)))
	}

	sidecarInst := protocol.NewSidecarInstance(nil)

	pa := &processAgent{
		kind:       kind,
		cmd:        cmd,
		transport:  transport,
		rpc:        rpc,
		sidecar:    sidecarInst,
		cancelFunc: cancel,
		logFile:    logCloser,
		done:       make(chan struct{}),
		exited:     make(chan struct{}),
	}

	// Background goroutine: reap the process and close the exited channel
	// so ProcessExited() works cross-platform (including Windows where
	// Signal(0) is unsupported).
	go func() {
		_ = cmd.Wait()
		close(pa.exited)
	}()

	// Run the initialize handshake with timeout.
	initCtx, initCancel := context.WithTimeout(ctx, initTimeout)
	defer initCancel()

	initReq := protocol.InitializeRequest{
		ProtocolVersion: protoVer,
		KernelVersion:   kernelVer,
		Capabilities:    map[string]bool{"streaming": true},
	}

	var initResp protocol.InitializeResponse
	if err := rpc.Call(initCtx, protocol.MethodInitialize, initReq, &initResp); err != nil {
		cancel()
		cmd.Process.Kill()
		_ = sidecarInst.TransitionTo(protocol.StateDraining)
		return nil, brainerrors.Wrap(err, brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("initialize handshake failed for %s", kind)))
	}

	// Transition to running.
	if err := sidecarInst.TransitionTo(protocol.StateRunning); err != nil {
		cancel()
		cmd.Process.Kill()
		return nil, err
	}

	// Populate the descriptor from the handshake response.
	pa.desc = agent.Descriptor{
		Kind:           kind,
		Version:        initResp.BrainVersion,
		LLMAccess:      desc.LLMAccess,
		SupportedTools: initResp.SupportedTools,
		Capabilities:   initResp.BrainCapabilities,
	}

	pa.mu.Lock()
	pa.ready = true
	pa.mu.Unlock()
	close(pa.done)

	// Store in the process map.
	r.mu.Lock()
	if r.processes == nil {
		r.processes = make(map[agent.Kind]*processAgent)
	}
	r.processes[kind] = pa
	r.mu.Unlock()

	return pa, nil
}

// Stop gracefully shuts down the sidecar of the given kind.
func (r *ProcessRunner) Stop(ctx context.Context, kind agent.Kind) error {
	r.mu.Lock()
	pa, ok := r.processes[kind]
	if ok {
		delete(r.processes, kind)
	}
	r.mu.Unlock()

	if !ok {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("no running sidecar for kind %s", kind)))
	}

	timeout := r.ShutdownTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return pa.Shutdown(shutdownCtx)
}

// StdioRunner implements BrainRunner for pre-connected stdio streams
// (no fork/exec). This is used for in-process testing and for sidecars
// that are connected via external means (e.g., unix sockets piped to
// stdio).
type StdioRunner struct {
	// Reader is the sidecar's stdout stream (sidecar → kernel).
	Reader io.Reader

	// Writer is the sidecar's stdin stream (kernel → sidecar).
	Writer io.Writer

	// InitTimeout is the deadline for the initialize handshake.
	InitTimeout time.Duration

	// ProtocolVersion defaults to "1.0".
	ProtocolVersion string

	// KernelVersion defaults to brain.KernelVersion.
	KernelVersion string

	agent *pipeAgent
}

// pipeAgent wraps a pre-connected transport as an agent.Agent.
type pipeAgent struct {
	kind       agent.Kind
	desc       agent.Descriptor
	rpc        protocol.BidirRPC
	transport  *StdioTransport
	cancelFunc context.CancelFunc

	mu    sync.Mutex
	ready bool
	done  chan struct{}
}

func (a *pipeAgent) Kind() agent.Kind             { return a.kind }
func (a *pipeAgent) Descriptor() agent.Descriptor { return a.desc }

func (a *pipeAgent) Ready(ctx context.Context) error {
	a.mu.Lock()
	if a.ready {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return brainerrors.New(brainerrors.CodeDeadlineExceeded,
			brainerrors.WithMessage("Ready: context cancelled"))
	}
}

func (a *pipeAgent) Shutdown(ctx context.Context) error {
	if a.rpc != nil {
		_ = a.rpc.Notify(ctx, protocol.MethodShutdown, nil)
		_ = a.rpc.Close()
	}
	if a.transport != nil {
		_ = a.transport.Close()
	}
	if a.cancelFunc != nil {
		a.cancelFunc()
	}
	return nil
}

// Start connects to the pre-wired streams and runs the initialize
// handshake. This is the primary constructor for tests that use io.Pipe
// pairs instead of real processes.
func (r *StdioRunner) Start(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error) {
	if r.Reader == nil || r.Writer == nil {
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("StdioRunner: Reader and Writer must be set"))
	}

	initTimeout := r.InitTimeout
	if initTimeout <= 0 {
		initTimeout = 30 * time.Second
	}
	protoVer := r.ProtocolVersion
	if protoVer == "" {
		protoVer = "1.0"
	}
	kernelVer := r.KernelVersion
	if kernelVer == "" {
		kernelVer = brain.KernelVersion
	}

	transport := NewStdioTransport(r.Reader, r.Writer)
	rpcReader := protocol.NewFrameReader(r.Reader)
	rpcWriter := protocol.NewFrameWriter(r.Writer)
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, rpcReader, rpcWriter)

	agentCtx, cancel := context.WithCancel(ctx)
	if err := rpc.Start(agentCtx); err != nil {
		cancel()
		return nil, brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("start RPC: %v", err)))
	}

	pa := &pipeAgent{
		kind:       kind,
		rpc:        rpc,
		transport:  transport,
		cancelFunc: cancel,
		done:       make(chan struct{}),
	}

	// Run the initialize handshake.
	initCtx, initCancel := context.WithTimeout(ctx, initTimeout)
	defer initCancel()

	initReq := protocol.InitializeRequest{
		ProtocolVersion: protoVer,
		KernelVersion:   kernelVer,
		Capabilities:    map[string]bool{"streaming": true},
	}

	var initResp protocol.InitializeResponse
	if err := rpc.Call(initCtx, protocol.MethodInitialize, initReq, &initResp); err != nil {
		cancel()
		return nil, brainerrors.Wrap(err, brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("initialize handshake failed for %s", kind)))
	}

	pa.desc = agent.Descriptor{
		Kind:           kind,
		Version:        initResp.BrainVersion,
		LLMAccess:      desc.LLMAccess,
		SupportedTools: initResp.SupportedTools,
		Capabilities:   initResp.BrainCapabilities,
	}

	pa.mu.Lock()
	pa.ready = true
	pa.mu.Unlock()
	close(pa.done)

	r.agent = pa
	return pa, nil
}

// Stop shuts down the pre-connected sidecar.
func (r *StdioRunner) Stop(ctx context.Context, kind agent.Kind) error {
	if r.agent == nil {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("StdioRunner: no agent to stop"))
	}
	return r.agent.Shutdown(ctx)
}

// PerformInitialize runs the initialize handshake over a BidirRPC session.
// This is extracted as a standalone function so both ProcessRunner and
// StdioRunner can reuse the same logic, and so tests can call it directly.
func PerformInitialize(ctx context.Context, rpc protocol.BidirRPC, protoVer, kernelVer string) (*protocol.InitializeResponse, error) {
	initReq := protocol.InitializeRequest{
		ProtocolVersion: protoVer,
		KernelVersion:   kernelVer,
		Capabilities:    map[string]bool{"streaming": true},
	}

	var resp protocol.InitializeResponse
	if err := rpc.Call(ctx, protocol.MethodInitialize, initReq, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// ProcessExited reports whether the underlying process has exited.
// Used by the Orchestrator's health check to detect crashed sidecars.
// This works cross-platform (including Windows) by checking the exited
// channel which is closed by the background Wait goroutine.
func (a *processAgent) ProcessExited() bool {
	if a.cmd == nil || a.cmd.Process == nil {
		return true
	}
	select {
	case <-a.exited:
		return true
	default:
		return false
	}
}

// RPC returns the underlying BidirRPC session. Implements agent.RPCAgent.
func (a *processAgent) RPC() interface{} { return a.rpc }

// RPC returns the underlying BidirRPC session. Implements agent.RPCAgent.
func (a *pipeAgent) RPC() interface{} { return a.rpc }

// --- Compile-time interface assertions ---

var _ agent.Agent = (*processAgent)(nil)
var _ agent.Agent = (*pipeAgent)(nil)
var _ agent.RPCAgent = (*processAgent)(nil)
var _ agent.RPCAgent = (*pipeAgent)(nil)
var _ BrainRunner = (*ProcessRunner)(nil)
var _ BrainRunner = (*StdioRunner)(nil)

// openSidecarLog returns a writer for sidecar stderr output. Logs are
// organized by date: ~/.brain/logs/2026-04-16/<kind>.log. A symlink
// ~/.brain/logs/<kind>.log always points to today's file for convenience.
// Falls back to os.Stderr if the log directory cannot be created.
func openSidecarLog(kind agent.Kind) (io.Writer, io.Closer) {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.Stderr, nil
	}
	baseDir := filepath.Join(home, ".brain", "logs")
	dateStr := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(baseDir, dateStr)
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		return os.Stderr, nil
	}
	logPath := filepath.Join(dayDir, fmt.Sprintf("%s.log", kind))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return os.Stderr, nil
	}

	// Update convenience symlink: ~/.brain/logs/<kind>.log → today's file
	symlink := filepath.Join(baseDir, fmt.Sprintf("%s.log", kind))
	_ = os.Remove(symlink)
	_ = os.Symlink(logPath, symlink)

	return f, f
}

// toJSONRaw marshals v into json.RawMessage. Convenience for building
// RPC params/results in tests.
func toJSONRaw(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// sidecarConfigEnvMap maps brain kind to the env var name and the config
// file basename under ~/.brain/.
var sidecarConfigEnvMap = map[agent.Kind]struct {
	envVar   string
	filename string
}{
	agent.KindData:    {"DATA_CONFIG", "data-brain.yaml"},
	agent.KindQuant:   {"QUANT_CONFIG", "quant-brain.yaml"},
	agent.KindCentral: {"CENTRAL_CONFIG", "central-brain.yaml"},
}

const (
	envBrainDBPath         = "BRAIN_DB_PATH"
	envUIPatternDBPath     = "BRAIN_UI_PATTERN_DB_PATH"
	envPersistenceDriver   = "BRAIN_PERSISTENCE_DRIVER"
	envPersistenceDSN      = "BRAIN_PERSISTENCE_DSN"
	sidecarPersistenceType = "sqlite"
)

func mergeEnvLists(base, extra []string) []string {
	out := append([]string{}, base...)
	out = append(out, extra...)
	return out
}

func sidecarPersistenceEnvEntries(runtimeDataDir string) []string {
	projection := BrowserRuntimeProjectionForDataDir(runtimeDataDir, false, nil)
	return projection.Env()
}

// SidecarPersistenceEnvForDataDir returns the canonical host→child runtime
// env entries for the given runtime data directory. Empty input falls back to
// the default host config directory.
func SidecarPersistenceEnvForDataDir(runtimeDataDir string) []string {
	return sidecarPersistenceEnvEntries(runtimeDataDir)
}

// injectSidecarConfigEnv auto-discovers config files under ~/.brain/ and
// injects the corresponding *_CONFIG env var if not already set.
func injectSidecarConfigEnv(env []string, kind agent.Kind) []string {
	entry, ok := sidecarConfigEnvMap[kind]
	if !ok {
		return env
	}

	// Check if already set.
	prefix := entry.envVar + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return env // user explicitly set it, don't override
		}
	}

	// Probe ~/.brain/<kind>-brain.yaml
	home, err := os.UserHomeDir()
	if err != nil {
		return env
	}
	configPath := filepath.Join(home, ".brain", entry.filename)
	if _, err := os.Stat(configPath); err != nil {
		return env // file doesn't exist, skip
	}

	return append(env, entry.envVar+"="+configPath)
}

// injectSidecarPersistenceEnv mirrors the host-side runtime backend location
// so child sidecars can open the same persistence database. The host runtime
// currently derives its SQLite DSN from filepath.Dir(configPath())/brain.db;
// this helper exports that decision via env without overriding explicit values.
func injectSidecarPersistenceEnv(env []string) []string {
	var (
		hasDBPath    bool
		hasPatternDB bool
		hasDriver    bool
		hasDSN       bool
	)
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, envBrainDBPath+"="):
			hasDBPath = true
		case strings.HasPrefix(entry, envUIPatternDBPath+"="):
			hasPatternDB = true
		case strings.HasPrefix(entry, envPersistenceDriver+"="):
			hasDriver = true
		case strings.HasPrefix(entry, envPersistenceDSN+"="):
			hasDSN = true
		}
	}
	hasSyncFile := false
	for _, entry := range env {
		if strings.HasPrefix(entry, envBrowserRuntimeSync+"=") {
			hasSyncFile = true
			break
		}
	}
	if hasDBPath && hasPatternDB && hasDriver && hasDSN && hasSyncFile {
		return env
	}

	entries := sidecarPersistenceEnvEntries("")
	if !hasDBPath {
		env = append(env, entries[0])
	}
	if !hasPatternDB {
		env = append(env, entries[1])
	}
	if !hasDriver {
		env = append(env, entries[2])
	}
	if !hasDSN {
		env = append(env, entries[3])
	}
	if !hasSyncFile && len(entries) > 4 {
		env = append(env, entries[4])
	}
	return env
}
