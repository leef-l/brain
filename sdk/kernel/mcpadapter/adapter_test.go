package mcpadapter

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

// fakeMCPServer runs a minimal MCP server on the given reader/writer pair.
// It handles initialize, notifications/initialized, tools/list, and tools/call.
func fakeMCPServer(t *testing.T, reader io.Reader, writer io.Writer, tools []MCPToolSpec) {
	t.Helper()
	fr := protocol.NewFrameReader(reader)
	fw := protocol.NewFrameWriter(writer)
	rpc := protocol.NewBidirRPC(protocol.RoleSidecar, fr, fw)

	rpc.Handle("initialize", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"serverInfo": map[string]interface{}{
				"name":    "fake-mcp",
				"version": "0.1.0",
			},
		}, nil
	})

	rpc.Handle("notifications/initialized", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	rpc.Handle("tools/list", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"tools": tools}, nil
	})

	rpc.Handle("tools/call", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return MCPToolResult{
				Content: []MCPContent{{Type: "text", Text: "parse error: " + err.Error()}},
				IsError: true,
			}, nil
		}
		// Echo tool: return the arguments as text
		return MCPToolResult{
			Content: []MCPContent{{Type: "text", Text: string(req.Arguments)}},
		}, nil
	})

	ctx := context.Background()
	if err := rpc.Start(ctx); err != nil {
		// Cannot use t.Fatalf in goroutine; just return silently.
		// The adapter side will see a broken pipe and fail the test.
		return
	}
	// Keep running until pipes close — the test's defer will close them.
}

// setupPipeAdapter creates a pipe-based adapter connected to a fake MCP server.
func setupPipeAdapter(t *testing.T, prefix string, tools []MCPToolSpec) *Adapter {
	t.Helper()

	// Pipes: adapter writes to serverIn, reads from serverOut
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()

	// Start fake server in background
	go fakeMCPServer(t, serverInR, serverOutW, tools)

	adapter := NewPipeAdapter(serverOutR, serverInW, prefix)

	t.Cleanup(func() {
		adapter.Stop(context.Background())
		serverInR.Close()
		serverInW.Close()
		serverOutR.Close()
		serverOutW.Close()
	})

	return adapter
}

func TestAdapter_StartPipe_DiscoverTools(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{Name: "search", Description: "Search code"},
		{Name: "read", Description: "Read file"},
	}
	adapter := setupPipeAdapter(t, "mcp.test.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("StartPipe: %v", err)
	}

	schemas, err := adapter.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(schemas) != 2 {
		t.Fatalf("len(schemas)=%d, want 2", len(schemas))
	}
	if schemas[0].Name != "mcp.test.search" {
		t.Errorf("schemas[0].Name=%q, want mcp.test.search", schemas[0].Name)
	}
	if schemas[1].Name != "mcp.test.read" {
		t.Errorf("schemas[1].Name=%q, want mcp.test.read", schemas[1].Name)
	}
}

func TestAdapter_Invoke(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{Name: "echo", Description: "Echo tool"},
	}
	adapter := setupPipeAdapter(t, "mcp.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("StartPipe: %v", err)
	}

	// Discover first so adapter.tools is populated
	_, err := adapter.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}

	args := json.RawMessage(`{"query":"hello"}`)
	result, err := adapter.Invoke(ctx, "echo", args)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError {
		t.Error("result.IsError should be false")
	}
	// Output is JSON-marshaled text content
	var text string
	if err := json.Unmarshal(result.Output, &text); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if text != `{"query":"hello"}` {
		t.Errorf("output=%q, want {\"query\":\"hello\"}", text)
	}
}

func TestAdapter_RegisterTools(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{Name: "run", Description: "Run command"},
	}
	adapter := setupPipeAdapter(t, "mcp.sh.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("StartPipe: %v", err)
	}

	registry := tool.NewMemRegistry()
	count, err := adapter.RegisterTools(ctx, registry)
	if err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}

	// Verify tool is in registry
	registered := registry.List()
	if len(registered) != 1 {
		t.Fatalf("registry.List()=%d, want 1", len(registered))
	}
	if registered[0].Name() != "mcp.sh.run" {
		t.Errorf("tool name=%q, want mcp.sh.run", registered[0].Name())
	}
}

func TestAdapter_MCPTool_Execute(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{Name: "greet", Description: "Greet"},
	}
	adapter := setupPipeAdapter(t, "mcp.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("StartPipe: %v", err)
	}

	registry := tool.NewMemRegistry()
	adapter.RegisterTools(ctx, registry)

	// Execute through the registered tool
	found, ok := registry.Lookup("mcp.greet")
	if !ok {
		t.Fatal("Lookup: tool not found")
	}

	args := json.RawMessage(`{"name":"world"}`)
	result, err := found.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Error("should not be error")
	}
}

func TestAdapter_DoubleStart(t *testing.T) {
	adapter := setupPipeAdapter(t, "mcp.", nil)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("first StartPipe: %v", err)
	}

	err := adapter.StartPipe(ctx)
	if err == nil {
		t.Fatal("second StartPipe should fail")
	}
}

func TestAdapter_InvokeBeforeStart(t *testing.T) {
	adapter := &Adapter{ToolPrefix: "mcp."}
	_, err := adapter.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("Invoke before Start should fail")
	}
}

func TestAdapter_StopIdempotent(t *testing.T) {
	adapter := &Adapter{ToolPrefix: "mcp."}
	// Stop on never-started adapter should not panic
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Double stop
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatalf("Stop again: %v", err)
	}
}

func TestMCPTool_Risk(t *testing.T) {
	mt := &MCPTool{}
	if mt.Risk() != tool.RiskHigh {
		t.Errorf("Risk()=%v, want RiskHigh", mt.Risk())
	}
}

func TestAdapter_DiscoverToolsBeforeStart(t *testing.T) {
	adapter := &Adapter{ToolPrefix: "mcp."}
	_, err := adapter.DiscoverTools(context.Background())
	if err == nil {
		t.Fatal("DiscoverTools before Start should fail")
	}
}

func TestAdapter_E2E_MultipleToolInvocations(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{Name: "echo", Description: "Echo back arguments"},
	}
	adapter := setupPipeAdapter(t, "mcp.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatalf("StartPipe: %v", err)
	}

	adapter.DiscoverTools(ctx)

	// Invoke the same tool multiple times in sequence.
	for i := 0; i < 5; i++ {
		args := json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`)
		result, err := adapter.Invoke(ctx, "echo", args)
		if err != nil {
			t.Fatalf("Invoke #%d: %v", i, err)
		}
		if result.IsError {
			t.Errorf("Invoke #%d: unexpected error", i)
		}
	}
}

func TestAdapter_E2E_FullPipeline(t *testing.T) {
	// End-to-end: start → discover → register → execute via registry → stop
	mcpTools := []MCPToolSpec{
		{Name: "greet", Description: "Greeting tool"},
		{Name: "farewell", Description: "Farewell tool"},
	}
	adapter := setupPipeAdapter(t, "ext.", mcpTools)

	ctx := context.Background()
	if err := adapter.StartPipe(ctx); err != nil {
		t.Fatal(err)
	}

	registry := tool.NewMemRegistry()
	count, err := adapter.RegisterTools(ctx, registry)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("registered=%d, want 2", count)
	}

	// Execute both tools through the registry.
	for _, name := range []string{"ext.greet", "ext.farewell"} {
		found, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q): not found", name)
		}
		result, err := found.Execute(ctx, json.RawMessage(`{"msg":"test"}`))
		if err != nil {
			t.Fatalf("Execute(%q): %v", name, err)
		}
		if result.IsError {
			t.Errorf("Execute(%q): unexpected error result", name)
		}
	}

	// Stop should not error.
	if err := adapter.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestAdapter_MCPTool_SchemaPreserved(t *testing.T) {
	mcpTools := []MCPToolSpec{
		{
			Name:        "analyze",
			Description: "Analyze code",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}}}`),
		},
	}
	adapter := setupPipeAdapter(t, "mcp.", mcpTools)

	ctx := context.Background()
	adapter.StartPipe(ctx)

	schemas, _ := adapter.DiscoverTools(ctx)
	if len(schemas) != 1 {
		t.Fatal("expected 1 schema")
	}
	if schemas[0].Name != "mcp.analyze" {
		t.Errorf("name=%q, want mcp.analyze", schemas[0].Name)
	}
	if schemas[0].Description != "Analyze code" {
		t.Errorf("desc=%q, want 'Analyze code'", schemas[0].Description)
	}
	if schemas[0].Brain != "mcp" {
		t.Errorf("brain=%q, want mcp", schemas[0].Brain)
	}

	var schema map[string]interface{}
	json.Unmarshal(schemas[0].InputSchema, &schema)
	if schema["type"] != "object" {
		t.Errorf("schema type=%v, want object", schema["type"])
	}
}
