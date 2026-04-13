package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/protocol"
	braintesting "github.com/leef-l/brain/testing"
)

func registerProtocolTests(r *braintesting.MemComplianceRunner) {
	// C-01: Frame round-trip — write a frame and read it back.
	r.Register(braintesting.ComplianceTest{
		ID: "C-01", Description: "Frame write/read round-trip", Category: "protocol",
	}, func(ctx context.Context) error {
		pr, pw := io.Pipe()
		writer := protocol.NewFrameWriter(pw)
		reader := protocol.NewFrameReader(pr)

		body := []byte(`{"jsonrpc":"2.0","method":"test"}`)
		frame := &protocol.Frame{
			ContentLength: len(body),
			ContentType:   protocol.CanonicalContentType,
			Body:          body,
		}

		go func() {
			writer.WriteFrame(ctx, frame)
		}()

		got, err := reader.ReadFrame(ctx)
		if err != nil {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage(fmt.Sprintf("C-01: ReadFrame failed: %v", err)))
		}
		if got.ContentLength != len(body) {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage("C-01: ContentLength mismatch"))
		}
		if !bytes.Equal(got.Body, body) {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage("C-01: Body mismatch"))
		}
		return nil
	})

	// C-02: Content-Type header is canonical.
	r.Register(braintesting.ComplianceTest{
		ID: "C-02", Description: "Canonical Content-Type header", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.CanonicalContentType != "application/vscode-jsonrpc; charset=utf-8" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-02: CanonicalContentType mismatch"))
		}
		return nil
	})

	// C-03: Frame body size limit enforcement (16 MiB).
	r.Register(braintesting.ComplianceTest{
		ID: "C-03", Description: "Frame body size limit 16 MiB", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.MaxBodySize != 16*1024*1024 {
			return brainerrors.New(brainerrors.CodeFrameTooLarge,
				brainerrors.WithMessage("C-03: MaxBodySize not 16 MiB"))
		}
		return nil
	})

	// C-04: RPCMessage JSONRPC field must be "2.0".
	r.Register(braintesting.ComplianceTest{
		ID: "C-04", Description: "RPCMessage JSONRPC=2.0", Category: "protocol",
	}, func(ctx context.Context) error {
		msg := protocol.RPCMessage{JSONRPC: "2.0", Method: "test"}
		data, _ := json.Marshal(msg)
		var decoded protocol.RPCMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage(fmt.Sprintf("C-04: unmarshal failed: %v", err)))
		}
		if decoded.JSONRPC != "2.0" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-04: JSONRPC field not 2.0"))
		}
		return nil
	})

	// C-05: Kernel role prefix is "k".
	r.Register(braintesting.ComplianceTest{
		ID: "C-05", Description: "Kernel role prefix is k:", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.RoleKernel.Prefix() != "k" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-05: RoleKernel prefix not k"))
		}
		if protocol.RoleSidecar.Prefix() != "s" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-05: RoleSidecar prefix not s"))
		}
		return nil
	})

	// C-06: BidirRPC Start idempotency (second Start returns error).
	r.Register(braintesting.ComplianceTest{
		ID: "C-06", Description: "BidirRPC double-Start returns error", Category: "protocol",
	}, func(ctx context.Context) error {
		pr, pw := io.Pipe()
		defer pr.Close()
		defer pw.Close()
		reader := protocol.NewFrameReader(pr)
		writer := protocol.NewFrameWriter(pw)
		rpc := protocol.NewBidirRPC(protocol.RoleKernel, reader, writer)

		rpcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		if err := rpc.Start(rpcCtx); err != nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage(fmt.Sprintf("C-06: first Start failed: %v", err)))
		}
		defer rpc.Close()

		err := rpc.Start(rpcCtx)
		if err == nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-06: second Start should return error"))
		}
		return nil
	})

	// C-07: Initialize method constant is "initialize".
	r.Register(braintesting.ComplianceTest{
		ID: "C-07", Description: "MethodInitialize constant", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.MethodInitialize != "initialize" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-07: MethodInitialize wrong"))
		}
		return nil
	})

	// C-08: Shutdown method constant is "shutdown".
	r.Register(braintesting.ComplianceTest{
		ID: "C-08", Description: "MethodShutdown constant", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.MethodShutdown != "shutdown" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-08: MethodShutdown wrong"))
		}
		return nil
	})

	// C-09: Heartbeat method constant is "heartbeat".
	r.Register(braintesting.ComplianceTest{
		ID: "C-09", Description: "MethodHeartbeat constant", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.MethodHeartbeat != "heartbeat" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-09: MethodHeartbeat wrong"))
		}
		return nil
	})

	// C-10: SidecarState FSM valid transitions.
	r.Register(braintesting.ComplianceTest{
		ID: "C-10", Description: "SidecarState valid transitions", Category: "protocol",
	}, func(ctx context.Context) error {
		validTransitions := [][2]protocol.SidecarState{
			{protocol.StateStarting, protocol.StateRunning},
			{protocol.StateStarting, protocol.StateDraining},
			{protocol.StateRunning, protocol.StateDraining},
			{protocol.StateDraining, protocol.StateClosed},
			{protocol.StateClosed, protocol.StateWaited},
			{protocol.StateWaited, protocol.StateReaped},
		}
		for _, tt := range validTransitions {
			if !protocol.IsValidTransition(tt[0], tt[1]) {
				return brainerrors.New(brainerrors.CodeInvariantViolated,
					brainerrors.WithMessage(fmt.Sprintf("C-10: %s→%s should be valid", tt[0], tt[1])))
			}
		}
		return nil
	})

	// C-11: SidecarState FSM invalid transitions.
	r.Register(braintesting.ComplianceTest{
		ID: "C-11", Description: "SidecarState invalid transitions rejected", Category: "protocol",
	}, func(ctx context.Context) error {
		invalidTransitions := [][2]protocol.SidecarState{
			{protocol.StateRunning, protocol.StateStarting},
			{protocol.StateClosed, protocol.StateRunning},
			{protocol.StateReaped, protocol.StateStarting},
			{protocol.StateReaped, protocol.StateRunning},
		}
		for _, tt := range invalidTransitions {
			if protocol.IsValidTransition(tt[0], tt[1]) {
				return brainerrors.New(brainerrors.CodeInvariantViolated,
					brainerrors.WithMessage(fmt.Sprintf("C-11: %s→%s should be invalid", tt[0], tt[1])))
			}
		}
		return nil
	})

	// C-12: SidecarInstance TransitionTo success path.
	r.Register(braintesting.ComplianceTest{
		ID: "C-12", Description: "SidecarInstance TransitionTo succeeds", Category: "protocol",
	}, func(ctx context.Context) error {
		inst := protocol.NewSidecarInstance(nil)
		if inst.State() != protocol.StateStarting {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-12: initial state not Starting"))
		}
		if err := inst.TransitionTo(protocol.StateRunning); err != nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage(fmt.Sprintf("C-12: transition failed: %v", err)))
		}
		if inst.State() != protocol.StateRunning {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-12: state not Running after transition"))
		}
		return nil
	})

	// C-13: SidecarInstance TransitionTo error on invalid.
	r.Register(braintesting.ComplianceTest{
		ID: "C-13", Description: "SidecarInstance rejects invalid transition", Category: "protocol",
	}, func(ctx context.Context) error {
		inst := protocol.NewSidecarInstance(nil)
		err := inst.TransitionTo(protocol.StateClosed)
		if err == nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-13: should reject Starting→Closed"))
		}
		return nil
	})

	// C-14: InitializeRequest JSON round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-14", Description: "InitializeRequest JSON round-trip", Category: "protocol",
	}, func(ctx context.Context) error {
		req := protocol.InitializeRequest{
			ProtocolVersion: "1.0",
			KernelVersion:   "0.1.0",
			Capabilities:    map[string]bool{"streaming": true},
		}
		data, err := json.Marshal(req)
		if err != nil {
			return brainerrors.New(brainerrors.CodeFrameEncodingError,
				brainerrors.WithMessage(fmt.Sprintf("C-14: marshal: %v", err)))
		}
		var decoded protocol.InitializeRequest
		if err := json.Unmarshal(data, &decoded); err != nil {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage(fmt.Sprintf("C-14: unmarshal: %v", err)))
		}
		if decoded.ProtocolVersion != "1.0" {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-14: ProtocolVersion mismatch"))
		}
		return nil
	})

	// C-15: InitializeResponse JSON round-trip.
	r.Register(braintesting.ComplianceTest{
		ID: "C-15", Description: "InitializeResponse JSON round-trip", Category: "protocol",
	}, func(ctx context.Context) error {
		resp := protocol.InitializeResponse{
			ProtocolVersion:   "1.0",
			BrainVersion:      "2.0.0",
			BrainCapabilities: map[string]bool{"streaming": true},
			SupportedTools:    []string{"code.read", "code.write"},
		}
		data, _ := json.Marshal(resp)
		var decoded protocol.InitializeResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return brainerrors.New(brainerrors.CodeFrameParseError,
				brainerrors.WithMessage(fmt.Sprintf("C-15: unmarshal: %v", err)))
		}
		if len(decoded.SupportedTools) != 2 {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-15: SupportedTools mismatch"))
		}
		return nil
	})

	// C-16: PingScheduler construction.
	r.Register(braintesting.ComplianceTest{
		ID: "C-16", Description: "PingScheduler construction", Category: "protocol",
	}, func(ctx context.Context) error {
		pr, pw := io.Pipe()
		defer pr.Close()
		defer pw.Close()
		reader := protocol.NewFrameReader(pr)
		writer := protocol.NewFrameWriter(pw)
		rpc := protocol.NewBidirRPC(protocol.RoleKernel, reader, writer)

		ps := protocol.NewPingScheduler(rpc, protocol.PingSchedulerOptions{
			Interval:      5 * time.Second,
			MissThreshold: 3,
		})
		if ps == nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-16: PingScheduler is nil"))
		}
		if ps.PendingCount() != 0 {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-16: initial PendingCount not 0"))
		}
		return nil
	})

	// C-17: StaleResponseTracker fires at threshold.
	r.Register(braintesting.ComplianceTest{
		ID: "C-17", Description: "StaleResponseTracker fires at threshold", Category: "protocol",
	}, func(ctx context.Context) error {
		fired := false
		tracker := protocol.NewStaleResponseTracker(1*time.Minute, 3, func() { fired = true })
		tracker.Observe()
		tracker.Observe()
		if fired {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-17: should not fire before threshold"))
		}
		tracker.Observe()
		if !fired {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-17: should fire at threshold"))
		}
		return nil
	})

	// C-18: RPC error code constants.
	r.Register(braintesting.ComplianceTest{
		ID: "C-18", Description: "RPC error code constants", Category: "protocol",
	}, func(ctx context.Context) error {
		if protocol.RPCCodeParseError != -32700 {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-18: RPCCodeParseError wrong"))
		}
		if protocol.RPCCodeMethodNotFound != -32601 {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-18: RPCCodeMethodNotFound wrong"))
		}
		if protocol.RPCCodeCancelled != -32800 {
			return brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
				brainerrors.WithMessage("C-18: RPCCodeCancelled wrong"))
		}
		return nil
	})

	// C-19: EncodeErrorToRPCError nil safety.
	r.Register(braintesting.ComplianceTest{
		ID: "C-19", Description: "EncodeErrorToRPCError nil-safe", Category: "protocol",
	}, func(ctx context.Context) error {
		rpcErr := protocol.EncodeErrorToRPCError(nil)
		if rpcErr == nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-19: EncodeErrorToRPCError(nil) should return non-nil"))
		}
		return nil
	})

	// C-20: WrapRPCError nil safety.
	r.Register(braintesting.ComplianceTest{
		ID: "C-20", Description: "WrapRPCError nil returns nil", Category: "protocol",
	}, func(ctx context.Context) error {
		result := protocol.WrapRPCError(nil)
		if result != nil {
			return brainerrors.New(brainerrors.CodeInvariantViolated,
				brainerrors.WithMessage("C-20: WrapRPCError(nil) should be nil"))
		}
		return nil
	})

	// suppress unused import warnings
	_ = strings.Contains
}
