package braintesting

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// FakeSidecar is the minimal scripted sidecar described in 25-测试策略.md
// Appendix B. It implements enough of the stdio wire protocol from
// 20-协议规格.md to satisfy Unit and Contract tests without spawning a real
// Python/TS brain process, without hitting a real LLM, and without actually
// executing tools.
//
// A FakeSidecar walks through its Script one SidecarStep at a time, applying
// deterministic side effects on every step (reply with canned result, inject
// a reverse-RPC call, crash, sleep, write a malformed frame, etc.). Tests
// drive the kernel under test, the kernel talks to the fake over stdio, and
// assertions are made on the kernel's observable behaviour.
//
// Implementations MUST treat Script as frozen at Start time: mutating the
// script mid-run is a programming error and SHOULD panic.
type FakeSidecar interface {
	// Start brings the fake online: open the stdio pipes, write the
	// protocol `hello` frame (20 §4), and park at Script[0].
	Start(ctx context.Context) error

	// Stop drains any in-flight frame, closes stdio and releases all
	// resources. It MUST be idempotent.
	Stop(ctx context.Context) error

	// SendFrame writes a single wire frame to the kernel side of the
	// transport. The frame argument is left as interface{} so that tests
	// can inject raw bytes, RPC messages or malformed payloads alike.
	SendFrame(ctx context.Context, frame interface{}) error

	// ReceiveFrame reads the next frame observed from the kernel side.
	// It MUST block until a frame is available, the context is cancelled,
	// or Stop has been called.
	ReceiveFrame(ctx context.Context) (interface{}, error)
}

// SidecarStep is a single scripted action applied by a FakeSidecar during
// Run. See 25-测试策略.md Appendix B for the taxonomy of built-in steps and
// the contract each one MUST satisfy.
//
// Tests MAY define their own steps by implementing this interface; doing so
// keeps the script ergonomic while avoiding a combinatorial explosion in the
// built-in catalogue.
type SidecarStep interface {
	// Apply mutates the FakeSidecar state to perform this step's effect.
	// Apply MUST return a Permanent BrainError when the step cannot be
	// satisfied (e.g. the kernel has not yet sent the expected request).
	Apply(s FakeSidecar) error
}

// ReplyStep instructs the FakeSidecar to answer a previously received
// request with a canned Result or Error, as documented in 25 Appendix B.
// Exactly one of Result and Error MUST be set; both set or both nil is a
// programming error.
type ReplyStep struct {
	// RequestID is the JSON-RPC id of the request being answered. See
	// 20-协议规格.md §5 for the id format ("k:<n>" for kernel-initiated
	// requests, "b:<n>" for brain-initiated).
	RequestID string

	// Result is the success payload to return. Typed as interface{} so
	// that tests can pass any of the Result structs from 20 §6.
	Result interface{}

	// Error is the failure payload to return. When non-nil it MUST be a
	// *errors.BrainError so that cross-language error fidelity is
	// preserved; typed as interface{} here only to keep this package
	// free of an errors import cycle.
	Error interface{}
}

// Apply satisfies SidecarStep for ReplyStep. See 25 Appendix B.
//
// The contract is: the fake sidecar MUST have previously received a
// request whose JSON-RPC id equals r.RequestID, and Apply MUST enqueue
// a response frame (Result or Error) that the kernel will subsequently
// receive via ReceiveFrame on its side of the transport.
func (r ReplyStep) Apply(s FakeSidecar) error {
	if (r.Result == nil) == (r.Error == nil) {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(
				"braintesting.ReplyStep.Apply: exactly one of Result and Error must be set",
			),
		)
	}
	mem, ok := s.(*memFakeSidecar)
	if !ok {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(
				"braintesting.ReplyStep.Apply: expected *memFakeSidecar",
			),
		)
	}
	frame := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      r.RequestID,
	}
	if r.Error != nil {
		frame["error"] = r.Error
	} else {
		frame["result"] = r.Result
	}
	return mem.enqueueFromBrain(frame)
}

// ReverseRPCStep instructs the FakeSidecar to initiate a brain → kernel
// reverse RPC call, simulating what a real sidecar does when it needs
// services like llm.complete or tool.invoke. See 25 Appendix B.
type ReverseRPCStep struct {
	// Method is one of the reverse-RPC method names enumerated in
	// 20-协议规格.md §7 (e.g. "llm.complete", "tool.invoke",
	// "artifact.put").
	Method string

	// Params is the untyped JSON-serialisable parameter object for the
	// call. The fake MUST marshal it via encoding/json before writing
	// the frame.
	Params interface{}
}

// Apply satisfies SidecarStep for ReverseRPCStep. See 25 Appendix B.
func (rv ReverseRPCStep) Apply(s FakeSidecar) error {
	if rv.Method == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage(
				"braintesting.ReverseRPCStep.Apply: Method is required",
			),
		)
	}
	mem, ok := s.(*memFakeSidecar)
	if !ok {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(
				"braintesting.ReverseRPCStep.Apply: expected *memFakeSidecar",
			),
		)
	}
	// Marshalling Params ahead of time validates that the caller
	// passed something serialisable — a common test-authoring mistake.
	if rv.Params != nil {
		if _, err := json.Marshal(rv.Params); err != nil {
			return brainerrors.New(brainerrors.CodeFrameEncodingError,
				brainerrors.WithMessage(fmt.Sprintf(
					"braintesting.ReverseRPCStep.Apply: params not JSON-serialisable: %v",
					err,
				)),
			)
		}
	}
	frame := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      mem.nextBrainID(),
		"method":  rv.Method,
		"params":  rv.Params,
	}
	return mem.enqueueFromBrain(frame)
}

// DelayStep pauses script execution for a fixed duration. Useful for
// regression tests of timeout, watchdog and circuit-breaker behaviour.
// See 25 Appendix B.
type DelayStep struct {
	// Duration is the amount of time to sleep before moving to the next
	// step. Implementations MUST honour context cancellation during the
	// sleep so that tests do not hang.
	Duration time.Duration
}

// Apply satisfies SidecarStep for DelayStep. See 25 Appendix B.
//
// The delay is honoured synchronously inside the script pump goroutine;
// because Apply observes the sidecar's stopCh, a test calling Stop
// mid-delay unblocks immediately instead of waiting out the sleep.
func (d DelayStep) Apply(s FakeSidecar) error {
	mem, ok := s.(*memFakeSidecar)
	if !ok {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(
				"braintesting.DelayStep.Apply: expected *memFakeSidecar",
			),
		)
	}
	if d.Duration <= 0 {
		return nil
	}
	t := time.NewTimer(d.Duration)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-mem.stopCh:
		return nil
	}
}

// CrashStep forces the FakeSidecar to exit with a specific code, letting
// tests assert the kernel's Restart / Dead-letter handling from
// 02-BrainKernel设计.md §11. See 25 Appendix B.
type CrashStep struct {
	// ExitCode is the process exit code to report back through the
	// transport. Values 0..255 are honoured.
	ExitCode int
}

// Apply satisfies SidecarStep for CrashStep. See 25 Appendix B.
//
// The in-memory fake models a crash by closing the brain→kernel channel
// and flipping an internal "crashed" flag so that subsequent
// ReceiveFrame calls return a sidecar_crashed BrainError carrying the
// scripted exit code. This is the closest stdio-free analogue to the
// real protocol's "process died" observable.
func (c CrashStep) Apply(s FakeSidecar) error {
	mem, ok := s.(*memFakeSidecar)
	if !ok {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(
				"braintesting.CrashStep.Apply: expected *memFakeSidecar",
			),
		)
	}
	mem.crash(c.ExitCode)
	return nil
}

// SendMalformedFrameStep writes an arbitrary byte sequence onto the wire
// without going through the normal framing layer. Used to exercise the
// kernel's 20-协议规格.md §3 frame-parser error paths. See 25 Appendix B.
type SendMalformedFrameStep struct {
	// Raw is the exact byte sequence to write, including any
	// intentionally corrupt Content-Length headers.
	Raw []byte
}

// Apply satisfies SidecarStep for SendMalformedFrameStep. See 25
// Appendix B. The raw bytes are enqueued verbatim so the kernel sees
// the exact malformed payload the test author intended.
func (m SendMalformedFrameStep) Apply(s FakeSidecar) error {
	mem, ok := s.(*memFakeSidecar)
	if !ok {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(
				"braintesting.SendMalformedFrameStep.Apply: expected *memFakeSidecar",
			),
		)
	}
	return mem.enqueueFromBrain(m.Raw)
}

// memFakeSidecar is the in-memory FakeSidecar used by unit tests. It
// models the stdio transport as two buffered channels: kernelToBrain
// collects frames the kernel sends via SendFrame; brainToKernel
// collects frames the script pushes via Apply. Both are drained by the
// opposing side through ReceiveFrame.
//
// The script pump goroutine walks through the Script one step at a
// time; steps block as long as they need to (e.g. a DelayStep waits
// out its Duration). Stop closes stopCh which causes every blocking
// path to unwind.
type memFakeSidecar struct {
	script []SidecarStep

	// kernelToBrain carries frames produced by the kernel side via
	// SendFrame. The script pump MAY drain it to inspect what the
	// kernel sent.
	kernelToBrain chan interface{}

	// brainToKernel carries frames produced by the script and is the
	// source read by ReceiveFrame on the kernel side.
	brainToKernel chan interface{}

	mu        sync.Mutex
	started   bool
	stopped   bool
	crashed   bool
	exitCode  int
	nextReqID int

	stopCh chan struct{}
	done   chan struct{}
}

// NewFakeSidecar returns a new FakeSidecar preloaded with the given script.
// The script is evaluated lazily: nothing happens until Start is called.
// See 25 Appendix B for the usage example.
func NewFakeSidecar(script []SidecarStep) FakeSidecar {
	frozen := make([]SidecarStep, len(script))
	copy(frozen, script)
	return &memFakeSidecar{
		script:        frozen,
		kernelToBrain: make(chan interface{}, 64),
		brainToKernel: make(chan interface{}, 64),
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// Start launches the script pump goroutine. Calling Start more than
// once returns CodeInvariantViolated because the fake's internal
// channels are single-use.
func (m *memFakeSidecar) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("memFakeSidecar.Start: already started"),
		)
	}
	m.started = true
	m.mu.Unlock()

	// Enqueue a hello frame as the §4 handshake would; real sidecars
	// announce their protocol version here and tests frequently look
	// for the frame as a "sidecar is alive" signal.
	_ = m.enqueueFromBrain(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "hello",
		"params":  map[string]interface{}{"protocol": "1.0"},
	})

	go m.pump()
	return nil
}

// pump walks through the frozen script, calling Apply on each step.
// Once the script is exhausted the pump idles until Stop is called —
// this matches the real-sidecar behaviour of waiting for the kernel
// to initiate further RPC calls.
func (m *memFakeSidecar) pump() {
	defer close(m.done)
	for _, step := range m.script {
		select {
		case <-m.stopCh:
			return
		default:
		}
		if err := step.Apply(m); err != nil {
			// A step failing is a test-authoring bug; surface it on
			// the brain→kernel channel so the kernel-side test sees
			// it during ReceiveFrame.
			_ = m.enqueueFromBrain(err)
			return
		}
	}
	<-m.stopCh
}

// Stop closes the stop channel and waits for the pump goroutine to
// exit. Subsequent calls are no-ops, satisfying the "MUST be
// idempotent" clause of the FakeSidecar contract.
func (m *memFakeSidecar) Stop(ctx context.Context) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopped = true
	close(m.stopCh)
	m.mu.Unlock()

	select {
	case <-m.done:
	case <-ctx.Done():
		return wrapCtxErr(ctx.Err())
	case <-time.After(2 * time.Second):
		// Prevent hangs in pathological scripts — a pump that takes
		// more than 2s to drain after Stop is a bug in the step
		// implementation, not in the fake infrastructure.
	}
	return nil
}

// SendFrame is called by the kernel-side test to push a frame to the
// brain. The frame is enqueued on kernelToBrain so the script pump (or
// a test assertion) can pick it up.
func (m *memFakeSidecar) SendFrame(ctx context.Context, frame interface{}) error {
	if err := ctx.Err(); err != nil {
		return wrapCtxErr(err)
	}
	select {
	case m.kernelToBrain <- frame:
		return nil
	case <-m.stopCh:
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("memFakeSidecar.SendFrame: fake stopped"),
		)
	case <-ctx.Done():
		return wrapCtxErr(ctx.Err())
	}
}

// ReceiveFrame is called by the kernel-side test to pull the next
// brain → kernel frame. When the fake has been crashed via CrashStep
// the method returns a sidecar_crashed BrainError.
func (m *memFakeSidecar) ReceiveFrame(ctx context.Context) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapCtxErr(err)
	}
	m.mu.Lock()
	crashed := m.crashed
	exit := m.exitCode
	m.mu.Unlock()
	if crashed && len(m.brainToKernel) == 0 {
		return nil, brainerrors.New(brainerrors.CodeSidecarCrashed,
			brainerrors.WithMessage(fmt.Sprintf(
				"memFakeSidecar.ReceiveFrame: scripted crash (exit=%d)", exit,
			)),
		)
	}
	select {
	case frame, ok := <-m.brainToKernel:
		if !ok {
			return nil, brainerrors.New(brainerrors.CodeSidecarStdoutEOF,
				brainerrors.WithMessage("memFakeSidecar.ReceiveFrame: stream closed"),
			)
		}
		return frame, nil
	case <-m.stopCh:
		return nil, brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("memFakeSidecar.ReceiveFrame: fake stopped"),
		)
	case <-ctx.Done():
		return nil, wrapCtxErr(ctx.Err())
	}
}

// enqueueFromBrain pushes a frame onto brainToKernel. Used by the
// script steps (ReplyStep, ReverseRPCStep, SendMalformedFrameStep) as
// well as by Start for the hello frame.
func (m *memFakeSidecar) enqueueFromBrain(frame interface{}) error {
	select {
	case m.brainToKernel <- frame:
		return nil
	case <-m.stopCh:
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("memFakeSidecar.enqueueFromBrain: fake stopped"),
		)
	}
}

// nextBrainID produces stable "b:<n>" identifiers for reverse RPC
// calls, matching the 20 §5 id format.
func (m *memFakeSidecar) nextBrainID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextReqID++
	return fmt.Sprintf("b:%d", m.nextReqID)
}

// crash flips the crashed flag and closes the brain→kernel channel so
// pending readers see EOF. Any subsequent ReceiveFrame returns a
// sidecar_crashed BrainError carrying the stored exit code.
func (m *memFakeSidecar) crash(exit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.crashed {
		return
	}
	m.crashed = true
	m.exitCode = exit
	// Do NOT close brainToKernel: existing queued frames (e.g. a
	// reply the kernel was about to read) should still drain. The
	// crashed flag + empty-queue check in ReceiveFrame provides the
	// §A.1 "sidecar_crashed" observable.
}
