package skeleton

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/leef-l/brain/sdk/tool"
)

// ---------------------------------------------------------------------------
// MemRegistry 基础操作
// ---------------------------------------------------------------------------

func TestMemRegistryRegisterAndLookup(t *testing.T) {
	reg := tool.NewMemRegistry()
	echo := tool.NewEchoTool("central")
	if err := reg.Register(echo); err != nil {
		t.Fatal(err)
	}
	found, ok := reg.Lookup("central.echo")
	if !ok {
		t.Fatal("Lookup should find registered tool")
	}
	if found.Name() != "central.echo" {
		t.Errorf("Name = %q", found.Name())
	}
}

func TestMemRegistryDuplicateReject(t *testing.T) {
	reg := tool.NewMemRegistry()
	echo := tool.NewEchoTool("central")
	_ = reg.Register(echo)
	err := reg.Register(echo)
	if err == nil {
		t.Fatal("duplicate register should return error")
	}
}

func TestMemRegistryEmptyNameReject(t *testing.T) {
	reg := tool.NewMemRegistry()
	err := reg.Register(&testTool{name: "", brain: "x"})
	if err == nil {
		t.Fatal("empty name should be rejected")
	}
}

func TestMemRegistryNameMismatchReject(t *testing.T) {
	reg := tool.NewMemRegistry()
	err := reg.Register(&testTool{name: "a", brain: "x", schemaName: "b"})
	if err == nil {
		t.Fatal("name mismatch should be rejected")
	}
}

func TestMemRegistryList(t *testing.T) {
	reg := tool.NewMemRegistry()
	_ = reg.Register(tool.NewEchoTool("central"))
	_ = reg.Register(tool.NewRejectTaskTool("central", nil))

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// 字典序: central.echo < central.reject_task
	if list[0].Name() != "central.echo" {
		t.Errorf("list[0] = %q", list[0].Name())
	}
	if list[1].Name() != "central.reject_task" {
		t.Errorf("list[1] = %q", list[1].Name())
	}
}

func TestMemRegistryListByBrain(t *testing.T) {
	reg := tool.NewMemRegistry()
	_ = reg.Register(tool.NewEchoTool("central"))
	_ = reg.Register(tool.NewEchoTool("code"))

	central := reg.ListByBrain("central")
	if len(central) != 1 {
		t.Fatalf("ListByBrain(central) len = %d, want 1", len(central))
	}
	all := reg.ListByBrain("")
	if len(all) != 2 {
		t.Fatalf("ListByBrain('') len = %d, want 2", len(all))
	}
}

// ---------------------------------------------------------------------------
// EchoTool
// ---------------------------------------------------------------------------

func TestEchoToolRoundTrip(t *testing.T) {
	echo := tool.NewEchoTool("central")
	input := json.RawMessage(`{"message":"hello"}`)
	result, err := echo.Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != string(input) {
		t.Errorf("Output = %s, want %s", result.Output, input)
	}
	if result.IsError {
		t.Error("IsError should be false")
	}
}

func TestEchoToolSchema(t *testing.T) {
	echo := tool.NewEchoTool("code")
	s := echo.Schema()
	if s.Name != "code.echo" {
		t.Errorf("Schema.Name = %q", s.Name)
	}
	if s.Brain != "code" {
		t.Errorf("Schema.Brain = %q", s.Brain)
	}
	if len(s.InputSchema) == 0 {
		t.Error("InputSchema should not be empty")
	}
}

func TestEchoToolRisk(t *testing.T) {
	echo := tool.NewEchoTool("central")
	if echo.Risk() != tool.RiskSafe {
		t.Errorf("Risk = %q, want safe", echo.Risk())
	}
}

// ---------------------------------------------------------------------------
// RejectTaskTool
// ---------------------------------------------------------------------------

func TestRejectTaskToolInputValidation(t *testing.T) {
	rt := tool.NewRejectTaskTool("central", nil)
	_, err := rt.Execute(context.Background(), json.RawMessage(`{"reason":""}`))
	if err == nil {
		t.Fatal("empty reason should return error")
	}
}

func TestRejectTaskToolCallback(t *testing.T) {
	var gotReason string
	callback := func(ctx context.Context, reason string) error {
		gotReason = reason
		return nil
	}
	rt := tool.NewRejectTaskTool("central", callback)
	result, err := rt.Execute(context.Background(), json.RawMessage(`{"reason":"too complex"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotReason != "too complex" {
		t.Errorf("callback reason = %q", gotReason)
	}
	if result.IsError {
		t.Error("IsError should be false on success")
	}
}

func TestRejectTaskToolRisk(t *testing.T) {
	rt := tool.NewRejectTaskTool("central", nil)
	if rt.Risk() != tool.RiskLow {
		t.Errorf("Risk = %q, want low", rt.Risk())
	}
}

// ---------------------------------------------------------------------------
// 并发安全
// ---------------------------------------------------------------------------

func TestMemRegistryConcurrency(t *testing.T) {
	reg := tool.NewMemRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "tool_" + intToStr(i)
			_ = reg.Register(&testTool{name: name, brain: "test", schemaName: name})
		}(i)
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg.Lookup("tool_" + intToStr(i))
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.List()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

type testTool struct {
	name       string
	brain      string
	schemaName string
}

func (t *testTool) Name() string { return t.name }
func (t *testTool) Schema() tool.Schema {
	sn := t.schemaName
	if sn == "" {
		sn = t.name
	}
	return tool.Schema{
		Name:        sn,
		Description: "test tool",
		InputSchema: json.RawMessage(`{}`),
		Brain:       t.brain,
	}
}
func (t *testTool) Risk() tool.Risk { return tool.RiskSafe }
func (t *testTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	return &tool.Result{Output: args}, nil
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
