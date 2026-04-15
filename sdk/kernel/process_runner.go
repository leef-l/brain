package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
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

	mu    sync.Mutex
	ready bool
	done  chan struct{}
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
		done := make(chan error, 1)
		go func() { done <- a.cmd.Wait() }()
		select {
		case <-done:
		case <-ctx.Done():
			_ = a.cmd.Process.Kill()
		}
	}
	if a.cancelFunc != nil {
		a.cancelFunc()
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

	// Create the process.
	cmd := exec.CommandContext(ctx, binPath)
	cmd.Env = r.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}

	// Redirect sidecar stderr to a log file instead of polluting the
	// interactive chat/run UI. Logs go to ~/.brain/logs/<kind>.log.
	cmd.Stderr = openSidecarLog(kind)

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

	agentCtx, cancel := context.WithCancel(ctx)
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
		done:       make(chan struct{}),
	}

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
func (a *processAgent) ProcessExited() bool {
	if a.cmd == nil || a.cmd.Process == nil {
		return true
	}
	// cmd.ProcessState is non-nil after Wait() returns.
	if a.cmd.ProcessState != nil {
		return true
	}
	// Try a non-blocking check: send signal 0 to test if process is alive.
	// On Unix, this returns nil if the process exists and we have permission.
	err := a.cmd.Process.Signal(syscall.Signal(0))
	return err != nil
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

// openSidecarLog returns a writer for sidecar stderr output. Logs go to
// ~/.brain/logs/<kind>.log. Falls back to os.Stderr if the log file cannot
// be created.
func openSidecarLog(kind agent.Kind) io.Writer {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.Stderr
	}
	logDir := fmt.Sprintf("%s/.brain/logs", home)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return os.Stderr
	}
	logPath := fmt.Sprintf("%s/%s.log", logDir, kind)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return os.Stderr
	}
	return f
}

// toJSONRaw marshals v into json.RawMessage. Convenience for building
// RPC params/results in tests.
func toJSONRaw(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
