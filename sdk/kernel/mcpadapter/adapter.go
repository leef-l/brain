// Package mcpadapter provides an adapter that lets any MCP (Model Context
// Protocol) server serve as a tool provider for BrainKernel agents.
//
// The adapter communicates with MCP servers over stdio using Content-Length
// framed JSON-RPC 2.0 — the same wire format used by BrainKernel's own
// protocol (20-协议规格.md §2), which is also the MCP transport format.
//
// The adapter is technically a ToolProvider, not a BrainRunner — MCP servers
// cannot be BrainAgents because they lack BrainPlan, Agent Loop,
// SpecialistReport, and reverse RPC capabilities. See 02-BrainKernel设计.md
// §12.5.0 (Decision 8).
//
// Lifecycle:
//  1. Connect: fork/exec the MCP server binary, wire stdio pipes
//  2. Initialize: send "initialize" request (MCP protocol)
//  3. DiscoverTools: send "tools/list" request, convert to tool.Schema[]
//  4. RegisterTools: register discovered tools into the Kernel's ToolRegistry
//  5. Invoke: when a brain's LLM issues tool_use for an MCP-backed tool,
//     forward as "tools/call" to the MCP server
//  6. Shutdown: close pipes, wait for process exit
package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

// MCPToolSpec describes an MCP tool as returned by "tools/list".
type MCPToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// MCPToolResult is the result of an MCP "tools/call" invocation.
type MCPToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPContent is a content block in an MCP tool result.
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Adapter manages the lifecycle of an MCP server and exposes its tools
// to the BrainKernel tool registry.
type Adapter struct {
	// BinPath is the path to the MCP server binary.
	BinPath string

	// Args are additional arguments to pass to the MCP server.
	Args []string

	// Env is the environment variables for the MCP server process.
	// nil inherits the current process environment.
	Env []string

	// ToolPrefix is prepended to every MCP tool name to prevent
	// collisions with native brain tools. Required.
	// Example: "mcp.github." → tool "search" becomes "mcp.github.search"
	ToolPrefix string

	mu       sync.Mutex
	cmd      *exec.Cmd
	rpc      protocol.BidirRPC
	tools    []MCPToolSpec
	started  bool
	shutdown bool
}

// Start launches the MCP server process and performs the initialize
// handshake.
func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("mcpadapter: already started"))
	}
	if a.BinPath == "" {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("mcpadapter: BinPath is required"))
	}
	if a.ToolPrefix == "" {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("mcpadapter: ToolPrefix is required"))
	}

	cmd := exec.CommandContext(ctx, a.BinPath, a.Args...)
	cmd.Env = a.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: stdin pipe: %v", err)))
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: stdout pipe: %v", err)))
	}

	if err := cmd.Start(); err != nil {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: exec: %v", err)))
	}

	reader := protocol.NewFrameReader(stdoutPipe)
	writer := protocol.NewFrameWriter(stdinPipe)
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, reader, writer)

	if err := rpc.Start(ctx); err != nil {
		cmd.Process.Kill()
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: start RPC: %v", err)))
	}

	// MCP initialize handshake.
	initReq := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "BrainKernel",
			"version": "0.1.0",
		},
	}
	var initResp json.RawMessage
	if err := rpc.Call(ctx, "initialize", initReq, &initResp); err != nil {
		cmd.Process.Kill()
		return brainerrors.Wrap(err, brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage("mcpadapter: initialize failed"))
	}

	// Send initialized notification.
	_ = rpc.Notify(ctx, "notifications/initialized", nil)

	a.cmd = cmd
	a.rpc = rpc
	a.started = true

	return nil
}

// DiscoverTools sends "tools/list" to the MCP server and returns the
// discovered tool schemas. Call after Start.
func (a *Adapter) DiscoverTools(ctx context.Context) ([]tool.Schema, error) {
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("mcpadapter: not started"))
	}
	rpc := a.rpc
	a.mu.Unlock()

	var result struct {
		Tools []MCPToolSpec `json:"tools"`
	}
	if err := rpc.Call(ctx, "tools/list", map[string]interface{}{}, &result); err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeToolNotFound,
			brainerrors.WithMessage("mcpadapter: tools/list failed"))
	}

	a.mu.Lock()
	a.tools = result.Tools
	a.mu.Unlock()

	schemas := make([]tool.Schema, 0, len(result.Tools))
	for _, t := range result.Tools {
		schemas = append(schemas, tool.Schema{
			Name:        a.ToolPrefix + t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Brain:       "mcp",
		})
	}
	return schemas, nil
}

// RegisterTools discovers MCP tools and registers them into the given
// registry as MCPTool instances that forward calls to the MCP server.
func (a *Adapter) RegisterTools(ctx context.Context, registry tool.Registry) (int, error) {
	schemas, err := a.DiscoverTools(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for i, schema := range schemas {
		mcpTool := &MCPTool{
			adapter:  a,
			schema:   schema,
			mcpName:  a.tools[i].Name,
		}
		if err := registry.Register(mcpTool); err != nil {
			return count, brainerrors.Wrap(err, brainerrors.CodeToolNotFound,
				brainerrors.WithMessage(fmt.Sprintf("mcpadapter: register %s: %v", schema.Name, err)))
		}
		count++
	}
	return count, nil
}

// Invoke calls an MCP tool by its original (unprefixed) name.
func (a *Adapter) Invoke(ctx context.Context, toolName string, args json.RawMessage) (*tool.Result, error) {
	a.mu.Lock()
	if !a.started || a.shutdown {
		a.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("mcpadapter: not available"))
	}
	rpc := a.rpc
	a.mu.Unlock()

	callReq := map[string]interface{}{
		"name":      toolName,
		"arguments": args,
	}

	var result MCPToolResult
	if err := rpc.Call(ctx, "tools/call", callReq, &result); err != nil {
		return nil, brainerrors.Wrap(err, brainerrors.CodeToolExecutionFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: tools/call %s failed", toolName)))
	}

	// Aggregate text content blocks.
	text := ""
	for _, c := range result.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}

	output, _ := json.Marshal(text)
	return &tool.Result{
		Output:  output,
		IsError: result.IsError,
	}, nil
}

// Stop gracefully shuts down the MCP server.
func (a *Adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started || a.shutdown {
		return nil
	}
	a.shutdown = true

	if a.rpc != nil {
		_ = a.rpc.Close()
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
	return nil
}

// MCPTool is a tool.Tool that forwards execution to an MCP server
// via the Adapter.
type MCPTool struct {
	adapter *Adapter
	schema  tool.Schema
	mcpName string // original MCP tool name without prefix
}

func (t *MCPTool) Name() string      { return t.schema.Name }
func (t *MCPTool) Schema() tool.Schema { return t.schema }
func (t *MCPTool) Risk() tool.Risk    { return tool.RiskHigh } // MCP tools are external

func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	return t.adapter.Invoke(ctx, t.mcpName, args)
}

// NewPipeAdapter creates an Adapter connected to pre-wired io.Reader/Writer
// pairs instead of a forked process. This is the test-friendly constructor.
func NewPipeAdapter(reader io.Reader, writer io.Writer, toolPrefix string) *Adapter {
	rpcReader := protocol.NewFrameReader(reader)
	rpcWriter := protocol.NewFrameWriter(writer)
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, rpcReader, rpcWriter)

	return &Adapter{
		BinPath:    "(pipe)",
		ToolPrefix: toolPrefix,
		rpc:        rpc,
	}
}

// StartPipe starts the RPC layer for a pipe-connected adapter. Unlike Start,
// it does not fork a process. The caller must start the RPC before calling
// DiscoverTools.
func (a *Adapter) StartPipe(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("mcpadapter: already started"))
	}

	if err := a.rpc.Start(ctx); err != nil {
		return brainerrors.New(brainerrors.CodeSidecarStartFailed,
			brainerrors.WithMessage(fmt.Sprintf("mcpadapter: start pipe RPC: %v", err)))
	}

	a.started = true
	return nil
}

// --- Compile-time interface assertion ---
var _ tool.Tool = (*MCPTool)(nil)
