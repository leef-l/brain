package skeleton

import (
	"testing"

	"github.com/leef-l/brain/sdk/protocol"
)

// ---------------------------------------------------------------------------
// 方法名常量
// ---------------------------------------------------------------------------

func TestMethodConstants(t *testing.T) {
	methods := map[string]string{
		"initialize":   protocol.MethodInitialize,
		"shutdown":     protocol.MethodShutdown,
		"heartbeat":    protocol.MethodHeartbeat,
		"llm.complete": protocol.MethodLLMComplete,
		"llm.stream":   protocol.MethodLLMStream,
		"tool.invoke":  protocol.MethodToolInvoke,
	}
	for want, got := range methods {
		if got != want {
			t.Errorf("Method %q = %q, want %q", want, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// SidecarState 合法转换
// ---------------------------------------------------------------------------

func TestSidecarStateValidTransitions(t *testing.T) {
	transitions := []struct {
		from protocol.SidecarState
		to   protocol.SidecarState
	}{
		{protocol.StateStarting, protocol.StateRunning},
		{protocol.StateRunning, protocol.StateDraining},
		{protocol.StateDraining, protocol.StateClosed},
		{protocol.StateClosed, protocol.StateWaited},
		{protocol.StateWaited, protocol.StateReaped},
	}
	for _, tt := range transitions {
		if !protocol.IsValidTransition(tt.from, tt.to) {
			t.Errorf("transition %v → %v should be valid", tt.from, tt.to)
		}
	}
}

func TestSidecarStateInvalidTransitions(t *testing.T) {
	invalid := []struct {
		from protocol.SidecarState
		to   protocol.SidecarState
	}{
		{protocol.StateRunning, protocol.StateStarting},
		{protocol.StateClosed, protocol.StateRunning},
		{protocol.StateReaped, protocol.StateStarting},
	}
	for _, tt := range invalid {
		if protocol.IsValidTransition(tt.from, tt.to) {
			t.Errorf("transition %v → %v should be invalid", tt.from, tt.to)
		}
	}
}

// ---------------------------------------------------------------------------
// SidecarInstance 状态机
// ---------------------------------------------------------------------------

func TestSidecarInstanceTransitions(t *testing.T) {
	si := protocol.NewSidecarInstance(nil)
	if si.State() != protocol.StateStarting {
		t.Errorf("initial state = %v, want starting", si.State())
	}

	transitions := []protocol.SidecarState{
		protocol.StateRunning,
		protocol.StateDraining,
		protocol.StateClosed,
		protocol.StateWaited,
		protocol.StateReaped,
	}
	for _, to := range transitions {
		if err := si.TransitionTo(to); err != nil {
			t.Fatalf("TransitionTo(%v): %v", to, err)
		}
	}
	if si.State() != protocol.StateReaped {
		t.Errorf("final state = %v, want reaped", si.State())
	}
}

func TestSidecarInstanceIllegalTransition(t *testing.T) {
	si := protocol.NewSidecarInstance(nil)
	// Starting → Closed is invalid (no direct edge)
	err := si.TransitionTo(protocol.StateClosed)
	if err == nil {
		t.Error("Starting → Closed should be rejected")
	}
}

// ---------------------------------------------------------------------------
// Role ID 前缀
// ---------------------------------------------------------------------------

func TestRoleIDPrefix(t *testing.T) {
	if protocol.RoleKernel.Prefix() != "k" {
		t.Errorf("Kernel prefix = %q, want k", protocol.RoleKernel.Prefix())
	}
	if protocol.RoleSidecar.Prefix() != "s" {
		t.Errorf("Sidecar prefix = %q, want s", protocol.RoleSidecar.Prefix())
	}
}

// ---------------------------------------------------------------------------
// Frame 大小限制常量
// ---------------------------------------------------------------------------

func TestFrameSizeLimits(t *testing.T) {
	if protocol.MaxBodySize != 16*1024*1024 {
		t.Errorf("MaxBodySize = %d, want 16 MiB", protocol.MaxBodySize)
	}
	if protocol.MaxHeaderLineSize != 8*1024 {
		t.Errorf("MaxHeaderLineSize = %d, want 8 KiB", protocol.MaxHeaderLineSize)
	}
}

// ---------------------------------------------------------------------------
// RPCError 编码
// ---------------------------------------------------------------------------

func TestRPCErrorFields(t *testing.T) {
	rpcErr := &protocol.RPCError{
		Code:    -32600,
		Message: "Invalid Request",
	}
	if rpcErr.Code != -32600 {
		t.Errorf("Code = %d", rpcErr.Code)
	}
	if rpcErr.Message != "Invalid Request" {
		t.Errorf("Message = %q", rpcErr.Message)
	}
}

// ---------------------------------------------------------------------------
// InitializeRequest 字段
// ---------------------------------------------------------------------------

func TestInitializeRequestFields(t *testing.T) {
	req := protocol.InitializeRequest{
		ProtocolVersion: "1.0",
		KernelVersion:   "0.1.0",
		WorkspacePath:   "/workspace",
	}
	if req.ProtocolVersion != "1.0" {
		t.Errorf("ProtocolVersion = %q", req.ProtocolVersion)
	}
}
