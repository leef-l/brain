package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestMemRegistryRegisterAndLookup verifies basic register and lookup operations.
func TestMemRegistryRegisterAndLookup(t *testing.T) {
	reg := NewMemRegistry()

	// Create and register a tool
	echo := NewEchoTool("test")
	if err := reg.Register(echo); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Lookup should succeed
	found, ok := reg.Lookup("test.echo")
	if !ok || found == nil {
		t.Fatal("Lookup failed: tool not found")
	}

	// Lookup should return the same tool
	if found.Name() != "test.echo" {
		t.Errorf("Lookup returned wrong tool: got %s, want test.echo", found.Name())
	}

	// Lookup non-existent tool
	_, ok = reg.Lookup("test.nonexistent")
	if ok {
		t.Fatal("Lookup should return false for non-existent tool")
	}
}

// TestMemRegistryDuplicateRegister verifies that duplicate registration is rejected.
func TestMemRegistryDuplicateRegister(t *testing.T) {
	reg := NewMemRegistry()

	echo := NewEchoTool("test")
	if err := reg.Register(echo); err != nil {
		t.Fatalf("First register failed: %v", err)
	}

	// Try to register the same tool again
	echo2 := NewEchoTool("test")
	err := reg.Register(echo2)
	if err == nil {
		t.Fatal("Duplicate register should fail")
	}

	// Check that error mentions duplicate
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("Error message should mention duplicate: %v", err)
	}
}

// TestMemRegistryEmptyName verifies that tools with empty names are rejected.
func TestMemRegistryEmptyName(t *testing.T) {
	reg := NewMemRegistry()

	// Create a custom tool with empty name
	badTool := &testTool{name: "", schema: Schema{Name: ""}}
	err := reg.Register(badTool)
	if err == nil {
		t.Fatal("Register with empty name should fail")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Error should mention empty name: %v", err)
	}
}

// TestMemRegistryNameMismatch verifies that schema.Name != tool.Name() is rejected.
func TestMemRegistryNameMismatch(t *testing.T) {
	reg := NewMemRegistry()

	// Create a tool where Name() and Schema().Name disagree
	mismatchTool := &testTool{
		name: "foo.bar",
		schema: Schema{
			Name: "foo.baz", // Mismatch!
		},
	}
	err := reg.Register(mismatchTool)
	if err == nil {
		t.Fatal("Register with name mismatch should fail")
	}

	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("Error should mention mismatch: %v", err)
	}
}

// TestMemRegistryList verifies that List returns tools in lexicographic order.
func TestMemRegistryList(t *testing.T) {
	reg := NewMemRegistry()

	// Register tools in non-alphabetical order
	tools := []Tool{
		NewEchoTool("zebra"),
		NewEchoTool("apple"),
		NewEchoTool("banana"),
	}

	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register failed: %v", err)
		}
	}

	// List should return them in sorted order
	listed := reg.List()
	if len(listed) != 3 {
		t.Errorf("List returned %d tools, want 3", len(listed))
	}

	expected := []string{"apple.echo", "banana.echo", "zebra.echo"}
	for i, tool := range listed {
		if tool.Name() != expected[i] {
			t.Errorf("List[%d] = %s, want %s", i, tool.Name(), expected[i])
		}
	}
}

// TestMemRegistryListByBrain verifies filtering by brain kind.
func TestMemRegistryListByBrain(t *testing.T) {
	reg := NewMemRegistry()

	t1 := NewEchoTool("code")
	t2 := NewEchoTool("browser")
	t3 := NewRejectTaskTool("central", nil)

	if err := reg.Register(t1); err != nil {
		t.Fatalf("Register t1 failed: %v", err)
	}
	if err := reg.Register(t2); err != nil {
		t.Fatalf("Register t2 failed: %v", err)
	}
	if err := reg.Register(t3); err != nil {
		t.Fatalf("Register t3 failed: %v", err)
	}

	// List by "code" should return only code.echo
	codeTools := reg.ListByBrain("code")
	if len(codeTools) != 1 {
		t.Errorf("ListByBrain(code) returned %d tools, want 1", len(codeTools))
	}
	if len(codeTools) > 0 && codeTools[0].Name() != "code.echo" {
		t.Errorf("ListByBrain(code)[0] = %s, want code.echo", codeTools[0].Name())
	}

	// List by "browser" should return only browser.echo
	browserTools := reg.ListByBrain("browser")
	if len(browserTools) != 1 {
		t.Errorf("ListByBrain(browser) returned %d tools, want 1", len(browserTools))
	}
	if len(browserTools) > 0 && browserTools[0].Name() != "browser.echo" {
		t.Errorf("ListByBrain(browser)[0] = %s, want browser.echo", browserTools[0].Name())
	}

	// List by "" should return all tools
	allTools := reg.ListByBrain("")
	if len(allTools) != 3 {
		t.Errorf("ListByBrain(\"\") returned %d tools, want 3", len(allTools))
	}
}

// TestEchoToolRoundTrip verifies that EchoTool returns args unchanged.
func TestEchoToolRoundTrip(t *testing.T) {
	ctx := context.Background()
	echo := NewEchoTool("test")

	// Test with a simple JSON object
	input := json.RawMessage(`{"message":"hello"}`)
	result, err := echo.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Fatal("Echo should not return IsError=true")
	}

	// Verify the output matches the input by unmarshaling both
	var inputObj, outputObj map[string]interface{}
	err1 := json.Unmarshal(input, &inputObj)
	err2 := json.Unmarshal(result.Output, &outputObj)
	if err1 != nil || err2 != nil {
		t.Fatalf("Failed to unmarshal: err1=%v err2=%v", err1, err2)
	}
	if inputObj["message"] != outputObj["message"] {
		t.Errorf("Echo output mismatch: got %s, want %s", result.Output, input)
	}
}

// TestEchoToolSchema verifies the tool's schema is correct.
func TestEchoToolSchema(t *testing.T) {
	echo := NewEchoTool("myapp")

	schema := echo.Schema()
	if schema.Name != "myapp.echo" {
		t.Errorf("Schema name = %s, want myapp.echo", schema.Name)
	}

	if schema.Brain != "myapp" {
		t.Errorf("Schema brain = %s, want myapp", schema.Brain)
	}

	if len(schema.InputSchema) == 0 {
		t.Fatal("InputSchema should not be empty")
	}

	if echo.Risk() != RiskSafe {
		t.Errorf("Risk = %s, want safe", echo.Risk())
	}
}

// TestRejectTaskToolInputValidation verifies parameter validation.
func TestRejectTaskToolInputValidation(t *testing.T) {
	ctx := context.Background()
	reject := NewRejectTaskTool("central", nil)

	// Test with empty reason
	input := json.RawMessage(`{"reason":""}`)
	_, err := reject.Execute(ctx, input)
	if err == nil {
		t.Fatal("Execute with empty reason should fail")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Error should mention empty reason: %v", err)
	}
}

// TestRejectTaskToolOnRejectCallback verifies the onReject hook is called.
func TestRejectTaskToolOnRejectCallback(t *testing.T) {
	var callCount int32
	var capturedReason string

	onReject := func(ctx context.Context, reason string) error {
		atomic.AddInt32(&callCount, 1)
		capturedReason = reason
		return nil
	}

	ctx := context.Background()
	reject := NewRejectTaskTool("central", onReject)

	input := json.RawMessage(`{"reason":"task is invalid"}`)
	result, err := reject.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("onReject called %d times, want 1", atomic.LoadInt32(&callCount))
	}

	if capturedReason != "task is invalid" {
		t.Errorf("onReject reason = %q, want %q", capturedReason, "task is invalid")
	}

	if result.IsError {
		t.Fatal("Result should have IsError=false")
	}

	// Verify response structure
	var response map[string]interface{}
	if err := json.Unmarshal(result.Output, &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response["status"] != "rejected" {
		t.Errorf("Response status = %v, want rejected", response["status"])
	}

	if response["reason"] != "task is invalid" {
		t.Errorf("Response reason = %v, want task is invalid", response["reason"])
	}
}

// TestRejectTaskToolSchema verifies the tool's schema is correct.
func TestRejectTaskToolSchema(t *testing.T) {
	reject := NewRejectTaskTool("central", nil)

	schema := reject.Schema()
	if schema.Name != "central.reject_task" {
		t.Errorf("Schema name = %s, want central.reject_task", schema.Name)
	}

	if schema.Brain != "central" {
		t.Errorf("Schema brain = %s, want central", schema.Brain)
	}

	if len(schema.InputSchema) == 0 {
		t.Fatal("InputSchema should not be empty")
	}

	if reject.Risk() != RiskLow {
		t.Errorf("Risk = %s, want low", reject.Risk())
	}
}

// TestMemRegistryConcurrentRegisterAndLookup verifies thread safety.
// This test runs with the -race detector to catch data races.
func TestMemRegistryConcurrentRegisterAndLookup(t *testing.T) {
	reg := NewMemRegistry()
	var wg sync.WaitGroup
	done := make(chan bool)

	// Goroutine that registers tools
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			tool := NewEchoTool(fmt.Sprintf("brain%d", i))
			reg.Register(tool)
		}
		done <- true
	}()

	// Goroutine that looks up tools
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			name := fmt.Sprintf("brain%d.echo", i)
			reg.Lookup(name)
		}
		done <- true
	}()

	// Goroutine that lists tools
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			reg.List()
		}
		done <- true
	}()

	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for all goroutines to complete
	for range done {
	}

	// Verify final state: all 100 tools should be registered
	allTools := reg.List()
	if len(allTools) != 100 {
		t.Errorf("Final tool count = %d, want 100", len(allTools))
	}
}

// ============================================================================
// Test Helper: Custom Tool Implementation
// ============================================================================

// testTool is a minimal Tool implementation used for testing edge cases.
type testTool struct {
	name   string
	schema Schema
}

func (t *testTool) Name() string   { return t.name }
func (t *testTool) Schema() Schema { return t.schema }
func (t *testTool) Risk() Risk     { return RiskSafe }
func (t *testTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	return &Result{Output: args, IsError: false}, nil
}
