// Package sidecar provides the shared runtime for built-in brain sidecars.
//
// Each built-in brain (central, code, verifier, fault) is an independent
// binary that communicates with the Kernel via stdio JSON-RPC 2.0 using
// the Content-Length framed protocol defined in 20-协议规格.md §2.
//
// The sidecar package handles the boilerplate: stdio wiring, initialize
// handshake, method registration, and graceful shutdown. Brain-specific
// logic is injected via the BrainHandler interface.
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/protocol"
)

// BrainHandler is the brain-specific logic injected into the sidecar runtime.
type BrainHandler interface {
	// Kind returns the brain's role identifier.
	Kind() agent.Kind

	// Version returns the brain sidecar's own version string.
	Version() string

	// Tools returns the list of tool names this brain supports.
	Tools() []string

	// HandleMethod is called for any RPC method not handled by the sidecar
	// framework itself. Return (nil, ErrMethodNotFound) to let the framework
	// send a standard error response.
	HandleMethod(ctx context.Context, method string, params json.RawMessage) (interface{}, error)
}

// ErrMethodNotFound is returned by HandleMethod when the method is not recognized.
var ErrMethodNotFound = fmt.Errorf("method not found")

// KernelCaller allows a BrainHandler to make outbound RPC calls to the
// Kernel (sidecar→host direction). This is used for reverse-RPC services
// such as llm.complete, subtask.delegate, and specialist.call_tool.
type KernelCaller interface {
	// CallKernel sends an RPC request to the Kernel and blocks until
	// the response arrives.
	CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error
}

// RichBrainHandler extends BrainHandler with outbound RPC capability.
// If a handler implements this interface, the sidecar runtime injects a
// KernelCaller after RPC setup, before the handler receives any work.
type RichBrainHandler interface {
	BrainHandler
	// SetKernelCaller is called by the sidecar runtime to provide
	// outbound RPC capability to the handler.
	SetKernelCaller(caller KernelCaller)
}

// rpcKernelCaller wraps a BidirRPC session as a KernelCaller.
type rpcKernelCaller struct {
	rpc protocol.BidirRPC
}

func (c *rpcKernelCaller) CallKernel(ctx context.Context, method string, params interface{}, result interface{}) error {
	return c.rpc.Call(ctx, method, params, result)
}

// Run starts the sidecar runtime with the given brain handler.
// It blocks until the Kernel sends a shutdown notification or the process
// receives SIGTERM/SIGINT. This is the entry point for every brain's main().
func Run(handler BrainHandler) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Wire stdio.
	reader := protocol.NewFrameReader(os.Stdin)
	writer := protocol.NewFrameWriter(os.Stdout)
	rpc := protocol.NewBidirRPC(protocol.RoleSidecar, reader, writer)

	// Detect stdin EOF (parent process exited). On Linux the kernel sends
	// SIGTERM via Pdeathsig, but on Windows/macOS the only signal is stdin
	// closing. When the RPC readLoop hits EOF it closes rpc — we monitor
	// that and cancel our context so Run() exits cleanly.
	go func() {
		<-rpc.Done()
		cancel()
	}()

	// Register initialize handler.
	rpc.Handle("initialize", func(_ context.Context, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{
			"protocolVersion": "1.0",
			"capabilities": map[string]interface{}{
				"tools": true,
			},
			"serverInfo": map[string]interface{}{
				"name":    fmt.Sprintf("brain-%s", handler.Kind()),
				"version": handler.Version(),
			},
			"brainDescriptor": map[string]interface{}{
				"kind":            string(handler.Kind()),
				"version":         handler.Version(),
				"llm_access":      "proxied",
				"supported_tools": handler.Tools(),
			},
		}, nil
	})

	// Register notifications/initialized (no-op ack).
	rpc.Handle("notifications/initialized", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	// Register $/shutdown.
	rpc.Handle("$/shutdown", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		cancel()
		return nil, nil
	})

	// Register tools/list.
	rpc.Handle("tools/list", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		specs := toolSpecsForHandler(handler)
		for i := range specs {
			if specs[i].Description == "" {
				specs[i].Description = fmt.Sprintf("Tool %s from %s brain", specs[i].Name, handler.Kind())
			}
		}
		return map[string]interface{}{"tools": specs}, nil
	})

	// Register tools/call — delegate to handler.
	rpc.Handle("tools/call", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return handler.HandleMethod(ctx, "tools/call", params)
	})

	// Catch-all for brain-specific methods via HandleMethod.
	// Note: BidirRPC only dispatches registered methods, so we register
	// a few common ones that brains might need.
	for _, method := range []string{"brain/execute", "brain/plan", "brain/verify"} {
		m := method // capture
		rpc.Handle(m, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return handler.HandleMethod(ctx, m, params)
		})
	}

	// Inject KernelCaller if the handler supports outbound RPC.
	if rich, ok := handler.(RichBrainHandler); ok {
		rich.SetKernelCaller(&rpcKernelCaller{rpc: rpc})
	}

	// Start the RPC reader loop.
	if err := rpc.Start(ctx); err != nil {
		return fmt.Errorf("sidecar: start RPC: %w", err)
	}

	// Log to stderr (stdout is the RPC channel).
	fmt.Fprintf(os.Stderr, "brain-%s sidecar v%s ready\n", handler.Kind(), handler.Version())

	// Block until context is cancelled.
	<-ctx.Done()

	rpc.Close()
	return nil
}
