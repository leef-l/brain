package kernel

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	brain "github.com/leef-l/brain"
	"github.com/leef-l/brain/agent"
	"github.com/leef-l/brain/protocol"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fakeSidecarHandler runs a minimal sidecar that handles the initialize
// handshake over the given reader/writer pair. It reads one frame, expects
// it to be an initialize request, and sends back a canned response.
func fakeSidecarHandler(t *testing.T, sidecarReader io.Reader, sidecarWriter io.Writer, brainVersion string, supportedTools []string) {
	t.Helper()

	reader := protocol.NewFrameReader(sidecarReader)
	writer := protocol.NewFrameWriter(sidecarWriter)
	rpc := protocol.NewBidirRPC(protocol.RoleSidecar, reader, writer)

	rpc.Handle(protocol.MethodInitialize, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var req protocol.InitializeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			t.Errorf("sidecar: failed to decode initialize request: %v", err)
			return nil, err
		}

		return &protocol.InitializeResponse{
			ProtocolVersion:   req.ProtocolVersion,
			BrainVersion:      brainVersion,
			BrainCapabilities: map[string]bool{"streaming": true},
			SupportedTools:    supportedTools,
		}, nil
	})

	rpc.Handle(protocol.MethodShutdown, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := rpc.Start(ctx); err != nil {
		t.Errorf("sidecar: failed to start RPC: %v", err)
		return
	}

	// Keep alive until context is done.
	<-ctx.Done()
	rpc.Close()
}

// ---------------------------------------------------------------------------
// Test 1: StdioTransport — Send and Receive round trip
// ---------------------------------------------------------------------------

func TestStdioTransport_RoundTrip(t *testing.T) {
	// Create two pipe pairs: kernel→sidecar and sidecar→kernel.
	k2s_r, k2s_w := io.Pipe()
	s2k_r, s2k_w := io.Pipe()

	// Kernel transport: reads from s2k, writes to k2s.
	transport := NewStdioTransport(s2k_r, k2s_w)
	defer transport.Close()

	// Sidecar side: reads from k2s, writes to s2k.
	sidecarReader := protocol.NewFrameReader(k2s_r)
	sidecarWriter := protocol.NewFrameWriter(s2k_w)

	ctx := context.Background()

	// Send a message from kernel.
	msg := &protocol.RPCMessage{
		JSONRPC: "2.0",
		ID:      "k:1",
		Method:  "test.ping",
		Params:  json.RawMessage(`{"hello":"world"}`),
	}

	go func() {
		if err := transport.Send(ctx, msg); err != nil {
			t.Errorf("Send failed: %v", err)
		}
	}()

	// Read it on sidecar side.
	frame, err := sidecarReader.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("sidecar ReadFrame: %v", err)
	}

	var received protocol.RPCMessage
	if err := json.Unmarshal(frame.Body, &received); err != nil {
		t.Fatalf("sidecar unmarshal: %v", err)
	}
	if received.Method != "test.ping" {
		t.Errorf("method = %q, want test.ping", received.Method)
	}

	// Send a response from sidecar.
	respMsg := &protocol.RPCMessage{
		JSONRPC: "2.0",
		ID:      "k:1",
		Result:  json.RawMessage(`{"pong":true}`),
	}
	respBody, _ := json.Marshal(respMsg)
	respFrame := &protocol.Frame{
		ContentLength: len(respBody),
		ContentType:   protocol.CanonicalContentType,
		Body:          respBody,
	}

	go func() {
		sidecarWriter.WriteFrame(ctx, respFrame)
	}()

	// Read on kernel side.
	gotMsg, err := transport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	if gotMsg.ID != "k:1" {
		t.Errorf("ID = %q, want k:1", gotMsg.ID)
	}
	if string(gotMsg.Result) == "" {
		t.Error("expected non-empty result")
	}
}

// ---------------------------------------------------------------------------
// Test 2: StdioTransport — Send after Close returns error
// ---------------------------------------------------------------------------

func TestStdioTransport_SendAfterClose(t *testing.T) {
	_, w := io.Pipe()
	r, _ := io.Pipe()
	transport := NewStdioTransport(r, w)
	transport.Close()

	err := transport.Send(context.Background(), &protocol.RPCMessage{
		JSONRPC: "2.0",
		Method:  "test",
	})
	if err == nil {
		t.Fatal("expected error after Close")
	}
}

// ---------------------------------------------------------------------------
// Test 3: StdioTransport — Receive after Close returns error
// ---------------------------------------------------------------------------

func TestStdioTransport_ReceiveAfterClose(t *testing.T) {
	r, _ := io.Pipe()
	_, w := io.Pipe()
	transport := NewStdioTransport(r, w)
	transport.Close()

	_, err := transport.Receive(context.Background())
	if err == nil {
		t.Fatal("expected error after Close")
	}
}

// ---------------------------------------------------------------------------
// Test 4: StdioRunner — Initialize handshake over pipes
// ---------------------------------------------------------------------------

func TestStdioRunner_InitializeHandshake(t *testing.T) {
	// Create pipe pairs.
	k2s_r, k2s_w := io.Pipe()
	s2k_r, s2k_w := io.Pipe()

	// Start a fake sidecar in the background.
	go fakeSidecarHandler(t, k2s_r, s2k_w, "1.0.0-test", []string{"test.echo"})

	runner := &StdioRunner{
		Reader:      s2k_r,
		Writer:      k2s_w,
		InitTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ag, err := runner.Start(ctx, agent.KindCode, agent.Descriptor{
		Kind:      agent.KindCode,
		LLMAccess: agent.LLMAccessProxied,
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if ag.Kind() != agent.KindCode {
		t.Errorf("Kind = %q, want %q", ag.Kind(), agent.KindCode)
	}

	desc := ag.Descriptor()
	if desc.Version != "1.0.0-test" {
		t.Errorf("Version = %q, want 1.0.0-test", desc.Version)
	}
	if len(desc.SupportedTools) != 1 || desc.SupportedTools[0] != "test.echo" {
		t.Errorf("SupportedTools = %v, want [test.echo]", desc.SupportedTools)
	}
	if desc.LLMAccess != agent.LLMAccessProxied {
		t.Errorf("LLMAccess = %q, want proxied", desc.LLMAccess)
	}

	// Ready should return immediately since handshake is done.
	if err := ag.Ready(ctx); err != nil {
		t.Errorf("Ready failed: %v", err)
	}

	// Shutdown.
	if err := runner.Stop(ctx, agent.KindCode); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 5: StdioRunner — Initialize timeout
// ---------------------------------------------------------------------------

func TestStdioRunner_InitializeTimeout(t *testing.T) {
	// Create pipes but the "sidecar" side never responds.
	_, k2s_w := io.Pipe()
	s2k_r, _ := io.Pipe()

	runner := &StdioRunner{
		Reader:      s2k_r,
		Writer:      k2s_w,
		InitTimeout: 200 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := runner.Start(ctx, agent.KindCode, agent.Descriptor{
		Kind: agent.KindCode,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ---------------------------------------------------------------------------
// Test 6: StdioTransport — Send nil message returns error
// ---------------------------------------------------------------------------

func TestStdioTransport_SendNilMessage(t *testing.T) {
	_, w := io.Pipe()
	r, _ := io.Pipe()
	transport := NewStdioTransport(r, w)
	defer transport.Close()

	err := transport.Send(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil message")
	}
}

// ---------------------------------------------------------------------------
// Test 7: PerformInitialize standalone
// ---------------------------------------------------------------------------

func TestPerformInitialize(t *testing.T) {
	k2s_r, k2s_w := io.Pipe()
	s2k_r, s2k_w := io.Pipe()

	// Fake sidecar.
	go fakeSidecarHandler(t, k2s_r, s2k_w, "2.0.0-beta", []string{"a", "b"})

	reader := protocol.NewFrameReader(s2k_r)
	writer := protocol.NewFrameWriter(k2s_w)
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, reader, writer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rpc.Start(ctx); err != nil {
		t.Fatalf("rpc.Start: %v", err)
	}
	defer rpc.Close()

	resp, err := PerformInitialize(ctx, rpc, "1.0", brain.KernelVersion)
	if err != nil {
		t.Fatalf("PerformInitialize: %v", err)
	}

	if resp.ProtocolVersion != "1.0" {
		t.Errorf("ProtocolVersion = %q, want 1.0", resp.ProtocolVersion)
	}
	if resp.BrainVersion != "2.0.0-beta" {
		t.Errorf("BrainVersion = %q, want 2.0.0-beta", resp.BrainVersion)
	}
	if len(resp.SupportedTools) != 2 {
		t.Errorf("SupportedTools = %v, want [a b]", resp.SupportedTools)
	}
}
