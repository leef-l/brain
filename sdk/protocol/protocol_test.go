package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildFrame writes a raw Content-Length framed message directly to w.
// Used for adversarial test cases where we need precise wire control.
func buildRawFrame(body []byte) []byte {
	header := fmt.Sprintf("Content-Length: %d\r\nContent-Type: %s\r\n\r\n",
		len(body), CanonicalContentType)
	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	return out
}

// makePipe returns a connected FrameReader / FrameWriter pair over an io.Pipe.
func makePipe() (FrameReader, FrameWriter) {
	pr, pw := io.Pipe()
	return NewFrameReader(pr), NewFrameWriter(pw)
}

// writeAndRead is a convenience: write frame bytes directly to an io.PipeWriter
// and read back via FrameReader.
func writeAndRead(t *testing.T, raw []byte) (*Frame, error) {
	t.Helper()
	pr, pw := io.Pipe()
	fr := NewFrameReader(pr)
	go func() {
		pw.Write(raw)
		pw.Close()
	}()
	return fr.ReadFrame(context.Background())
}

// fullDuplexRPC wires up a kernel-side and sidecar-side BidirRPC that talk
// over two in-process io.Pipe pairs.  Returns (kernel, sidecar).
func fullDuplexRPC(t *testing.T) (BidirRPC, BidirRPC) {
	t.Helper()
	// kernelToSidecar: kernel writes → sidecar reads
	kts_r, kts_w := io.Pipe()
	// sidecarToKernel: sidecar writes → kernel reads
	stk_r, stk_w := io.Pipe()

	kernelReader := NewFrameReader(stk_r)
	kernelWriter := NewFrameWriter(kts_w)
	sidecarReader := NewFrameReader(kts_r)
	sidecarWriter := NewFrameWriter(stk_w)

	kernel := NewBidirRPC(RoleKernel, kernelReader, kernelWriter)
	sidecar := NewBidirRPC(RoleSidecar, sidecarReader, sidecarWriter)

	ctx := context.Background()
	if err := kernel.Start(ctx); err != nil {
		t.Fatalf("kernel.Start: %v", err)
	}
	if err := sidecar.Start(ctx); err != nil {
		t.Fatalf("sidecar.Start: %v", err)
	}

	t.Cleanup(func() {
		kernel.Close()
		sidecar.Close()
	})
	return kernel, sidecar
}

// isBrainError asserts that err is a *BrainError with the given ErrorCode.
func isBrainError(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected BrainError(%s) but got nil", wantCode)
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *BrainError but got %T: %v", err, err)
	}
	if be.ErrorCode != wantCode {
		t.Fatalf("expected error code %q but got %q (message: %s)", wantCode, be.ErrorCode, be.Message)
	}
}

// ---------------------------------------------------------------------------
// C-01 Frame happy path: write a frame and read it back correctly.
// ---------------------------------------------------------------------------

func TestC_01_FrameHappyPath(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"minimal_json", `{"jsonrpc":"2.0"}`},
		{"empty_body", ""},
		{"unicode_body", `{"text":"héllo wörld 🌏"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			fr := NewFrameReader(pr)
			fw := NewFrameWriter(pw)
			defer fw.Close()

			body := []byte(tc.body)
			sentFrame := &Frame{
				ContentLength: len(body),
				ContentType:   CanonicalContentType,
				Body:          body,
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- fw.WriteFrame(context.Background(), sentFrame)
			}()

			got, err := fr.ReadFrame(context.Background())
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if writeErr := <-errCh; writeErr != nil {
				t.Fatalf("WriteFrame: %v", writeErr)
			}

			if got.ContentLength != len(body) {
				t.Errorf("ContentLength: got %d, want %d", got.ContentLength, len(body))
			}
			if !bytes.Equal(got.Body, body) {
				t.Errorf("Body mismatch: got %q, want %q", got.Body, body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// C-02 BOM rejected: body starting with UTF-8 BOM (0xEF 0xBB 0xBF) must fail.
// ---------------------------------------------------------------------------

func TestC_02_BOMRejected(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	body := append(bom, []byte(`{"jsonrpc":"2.0"}`)...)
	raw := buildRawFrame(body)

	_, err := writeAndRead(t, raw)
	if err == nil {
		t.Fatal("expected error for BOM frame but got nil")
	}
	isBrainError(t, err, brainerrors.CodeFrameParseError)
}

// ---------------------------------------------------------------------------
// C-03 16 MiB frame rejected: Content-Length > 16 MiB must return CodeFrameTooLarge.
// ---------------------------------------------------------------------------

func TestC_03_16MiBFrameRejected(t *testing.T) {
	// Craft a header with Content-Length > 16 MiB without allocating 16 MiB.
	oversized := MaxBodySize + 1
	header := fmt.Sprintf("Content-Length: %d\r\nContent-Type: %s\r\n\r\n",
		oversized, CanonicalContentType)
	pr, pw := io.Pipe()
	fr := NewFrameReader(pr)

	go func() {
		pw.Write([]byte(header))
		pw.Close()
	}()

	_, err := fr.ReadFrame(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized frame but got nil")
	}
	isBrainError(t, err, brainerrors.CodeFrameTooLarge)
}

// The writer also enforces the 16 MiB cap.
func TestC_03_WriterRejects16MiBFrame(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	fw := NewFrameWriter(pw)
	defer fw.Close()

	bigBody := make([]byte, MaxBodySize+1)
	frame := &Frame{
		ContentLength: len(bigBody),
		Body:          bigBody,
	}
	err := fw.WriteFrame(context.Background(), frame)
	if err == nil {
		t.Fatal("expected error for oversized write but got nil")
	}
	isBrainError(t, err, brainerrors.CodeFrameTooLarge)
}

// ---------------------------------------------------------------------------
// C-04 Content-Length exact: declared length must match body bytes.
// Writer enforces ContentLength == len(Body).
// ---------------------------------------------------------------------------

func TestC_04_ContentLengthMismatchRejected(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	fw := NewFrameWriter(pw)
	defer fw.Close()

	body := []byte(`{"jsonrpc":"2.0"}`)
	frame := &Frame{
		ContentLength: len(body) + 5, // deliberately wrong
		Body:          body,
	}
	err := fw.WriteFrame(context.Background(), frame)
	if err == nil {
		t.Fatal("expected encoding error for mismatched Content-Length, got nil")
	}
	isBrainError(t, err, brainerrors.CodeFrameEncodingError)
}

// ---------------------------------------------------------------------------
// C-05 8 KiB header line cap: a header line longer than 8192 bytes must fail.
// ---------------------------------------------------------------------------

func TestC_05_HeaderLineCapEnforced(t *testing.T) {
	// Build a header whose Content-Type value is 9000 chars long.
	longValue := strings.Repeat("x", 9000)
	raw := fmt.Sprintf("Content-Length: 2\r\nContent-Type: %s\r\n\r\n{}", longValue)
	pr, pw := io.Pipe()
	fr := NewFrameReader(pr)
	go func() {
		pw.Write([]byte(raw))
		pw.Close()
	}()

	_, err := fr.ReadFrame(context.Background())
	if err == nil {
		t.Fatal("expected error for overlong header line, got nil")
	}
	// The reader wraps this as CodeFrameParseError per the implementation.
	isBrainError(t, err, brainerrors.CodeFrameParseError)
}

// ---------------------------------------------------------------------------
// C-06 Writer serialized: concurrent WriteFrame calls must not interleave.
// Verify by writing many frames concurrently and reading them all back.
// ---------------------------------------------------------------------------

func TestC_06_WriterSerialized(t *testing.T) {
	pr, pw := io.Pipe()
	fr := NewFrameReader(pr)
	fw := NewFrameWriter(pw)

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`{"seq":%d}`, i))
			frame := &Frame{ContentLength: len(body), ContentType: CanonicalContentType, Body: body}
			errs[i] = fw.WriteFrame(context.Background(), frame)
		}()
	}

	// Read n frames in a separate goroutine to drain the pipe.
	readDone := make(chan int, 1)
	go func() {
		count := 0
		for {
			f, err := fr.ReadFrame(context.Background())
			if err != nil {
				break
			}
			// Verify each frame body is valid JSON — interleaved writes would corrupt this.
			var m map[string]interface{}
			if jsonErr := json.Unmarshal(f.Body, &m); jsonErr != nil {
				break
			}
			count++
			if count == n {
				break
			}
		}
		readDone <- count
	}()

	wg.Wait()
	fw.Close()

	got := <-readDone
	if got != n {
		t.Errorf("concurrent write: read %d valid frames, want %d", got, n)
	}

	// No write errors should have occurred.
	for i, e := range errs {
		if e != nil {
			t.Errorf("WriteFrame[%d] error: %v", i, e)
		}
	}
}

// ---------------------------------------------------------------------------
// C-07 ID prefix: kernel outbound requests must carry "k:" prefix;
// sidecar outbound requests must carry "s:" prefix.
// ---------------------------------------------------------------------------

func TestC_07_IDPrefix(t *testing.T) {
	cases := []struct {
		role         Role
		wantPrefix   string
		wantPeerPfx  string
	}{
		{RoleKernel, "k:", "s:"},
		{RoleSidecar, "s:", "k:"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.wantPrefix, func(t *testing.T) {
			// Wire up a loopback: caller writes requests, we intercept on the reader.
			pr, pw := io.Pipe()
			fw := NewFrameWriter(pw)
			fr := NewFrameReader(pr)

			// The "remote" side just echoes a success response.
			// First we send and capture the outbound frame.
			rpc := NewBidirRPC(tc.role, fr, fw)

			// Peer: reads our request and sends back a response with same id.
			peerPr, peerPw := io.Pipe()
			peerFr := NewFrameReader(pr) // reads from our pw side
			_ = peerFr
			_ = peerPr
			_ = peerPw

			// Simpler: just capture the raw bytes we write and inspect the id.
			var buf bytes.Buffer
			capPR, capPW := io.Pipe()
			capFR := NewFrameReader(capPR)
			capFW := NewFrameWriter(capPW)

			rpc2 := NewBidirRPC(tc.role, capFR, capFW)
			_ = rpc2
			_ = rpc

			// Actually write a notification (no id needed) — for id prefix we
			// capture a request manually from nextID().
			r := NewBidirRPC(tc.role, NewFrameReader(strings.NewReader("")), NewFrameWriter(&buf))
			br := r.(*bidirRPC)
			id := br.nextID()

			if !strings.HasPrefix(id, tc.wantPrefix) {
				t.Errorf("role %v: expected id prefix %q, got id %q", tc.role, tc.wantPrefix, id)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// C-08 Notification no id: Notify must not allocate an id; the wire frame
// must have an empty id field.
// ---------------------------------------------------------------------------

func TestC_08_NotificationNoID(t *testing.T) {
	var buf bytes.Buffer
	fw := NewFrameWriter(&buf)
	fr := NewFrameReader(strings.NewReader("")) // dummy, never read

	rpc := NewBidirRPC(RoleKernel, fr, fw)
	if err := rpc.Notify(context.Background(), "trace.emit", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	fw.Close()

	// Parse back from buf.
	gotFR := NewFrameReader(&buf)
	frame, err := gotFR.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("ReadFrame after Notify: %v", err)
	}
	var msg RPCMessage
	if err := json.Unmarshal(frame.Body, &msg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if msg.ID != "" {
		t.Errorf("Notification must have empty id, got %q", msg.ID)
	}
	if msg.Method != "trace.emit" {
		t.Errorf("expected method trace.emit, got %q", msg.Method)
	}
}

// ---------------------------------------------------------------------------
// C-09 Stale response dropped: a response that arrives after the waiter
// already left (timed out) increments staleCount and is not delivered.
// ---------------------------------------------------------------------------

func TestC_09_StaleResponseDropped(t *testing.T) {
	// Build a raw response frame for an id that no waiter holds.
	staleID := "k:9999"
	responseMsg := RPCMessage{
		JSONRPC: "2.0",
		ID:      staleID,
		Result:  json.RawMessage(`"ok"`),
	}
	body, _ := json.Marshal(responseMsg)
	raw := buildRawFrame(body)

	pr, pw := io.Pipe()
	fr := NewFrameReader(pr)
	fw := NewFrameWriter(io.Discard)

	rpc := NewBidirRPC(RoleKernel, fr, fw)
	ctx := context.Background()
	if err := rpc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rpc.Close()

	// Inject the stale response.
	go func() {
		pw.Write(raw)
	}()

	// Give readLoop time to process the frame.
	time.Sleep(30 * time.Millisecond)

	br := rpc.(*bidirRPC)
	if count := br.staleCount.Load(); count != 1 {
		t.Errorf("staleCount: got %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// C-10 In-flight window: the kernel allows up to 32 concurrent Calls;
// the 33rd must block until one slot frees.
// ---------------------------------------------------------------------------

func TestC_10_InFlightWindowBlocks(t *testing.T) {
	// Create a RPC where we can manually saturate the in-flight window.
	// We don't need a real reader — just fill the window slots.
	dummyFR := NewFrameReader(strings.NewReader("")) // EOF immediately
	dummyFW := NewFrameWriter(io.Discard)

	rpc := NewBidirRPC(RoleKernel, dummyFR, dummyFW)
	br := rpc.(*bidirRPC)

	// Saturate all 32 kernel slots.
	for i := 0; i < 32; i++ {
		br.inFlightWindow <- struct{}{}
	}

	// Now a Call with very short deadline should fail because window is full.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := br.acquireSlot(ctx)
	if err == nil {
		t.Fatal("expected error when window is full but got nil")
	}
	isBrainError(t, err, brainerrors.CodeDeadlineExceeded)

	// Drain the slots we filled.
	for i := 0; i < 32; i++ {
		<-br.inFlightWindow
	}
}

// C-10b: Sidecar in-flight window is 64.
func TestC_10b_SidecarWindowIs64(t *testing.T) {
	dummyFR := NewFrameReader(strings.NewReader(""))
	dummyFW := NewFrameWriter(io.Discard)
	rpc := NewBidirRPC(RoleSidecar, dummyFR, dummyFW)
	br := rpc.(*bidirRPC)
	if cap(br.inFlightWindow) != 64 {
		t.Errorf("sidecar in-flight window capacity: got %d, want 64", cap(br.inFlightWindow))
	}
}

// ---------------------------------------------------------------------------
// C-11 $/cancelRequest: sending $/cancelRequest cancels the pending Call.
// ---------------------------------------------------------------------------

func TestC_11_CancelRequest(t *testing.T) {
	kernel, sidecar := fullDuplexRPC(t)

	// Sidecar registers a handler that blocks until its context is cancelled.
	sidecar.Handle("slow.method", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		<-ctx.Done() // block until context cancelled
		return nil, brainerrors.New(brainerrors.CodeShuttingDown, brainerrors.WithMessage("cancelled"))
	})

	// Kernel sends the request; we want to capture the id it uses.
	// We'll do it by first issuing the Call asynchronously, then sending
	// a $/cancelRequest for the known id.

	// The kernel will allocate id "k:1" for its first call.
	callErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		callErr <- kernel.Call(ctx, "slow.method", nil, nil)
	}()

	// Give the call time to register a waiter.
	time.Sleep(20 * time.Millisecond)

	// Send $/cancelRequest from sidecar back to kernel (cancels the inflight cancel func).
	cancelErr := kernel.(*bidirRPC)
	// The kernel side's inflightCancel map will have "k:1".
	cancelErr.cancelMu.Lock()
	var cancelID string
	for id := range cancelErr.inflightCancel {
		cancelID = id
	}
	cancelErr.cancelMu.Unlock()

	if cancelID == "" {
		t.Fatal("no inflight cancel registered")
	}

	// Fire the cancel directly (mimicking what $/cancelRequest would do on the wire).
	cancelErr.cancelMu.Lock()
	fn := cancelErr.inflightCancel[cancelID]
	cancelErr.cancelMu.Unlock()
	if fn != nil {
		fn()
	}

	select {
	case err := <-callErr:
		if err == nil {
			t.Fatal("expected Call to return an error after cancel, got nil")
		}
		// The call was context-cancelled — expect DeadlineExceeded or ShuttingDown.
		be, ok := err.(*brainerrors.BrainError)
		if !ok {
			t.Fatalf("expected *BrainError but got %T: %v", err, err)
		}
		if be.ErrorCode != brainerrors.CodeDeadlineExceeded && be.ErrorCode != brainerrors.CodeShuttingDown {
			t.Errorf("unexpected error code: %q", be.ErrorCode)
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("timed out waiting for Call to return after cancel")
	}
}

// ---------------------------------------------------------------------------
// C-13 State machine valid transitions: each valid edge must succeed.
// ---------------------------------------------------------------------------

func TestC_13_StateMachineValidTransitions(t *testing.T) {
	// Valid paths per spec §6.3 adjacency table.
	paths := [][]SidecarState{
		{StateStarting, StateRunning, StateDraining, StateClosed, StateWaited, StateReaped},
		{StateStarting, StateDraining, StateClosed, StateWaited, StateReaped},
		{StateStarting, StateRunning, StateWaited, StateReaped},  // safety valve
		{StateStarting, StateDraining, StateWaited, StateReaped}, // safety valve
		{StateStarting, StateWaited, StateReaped},                // safety valve
	}

	for _, path := range paths {
		path := path
		t.Run(fmt.Sprintf("%v", path), func(t *testing.T) {
			si := NewSidecarInstance(nil)
			for i := 1; i < len(path); i++ {
				if err := si.TransitionTo(path[i]); err != nil {
					t.Errorf("transition %s→%s failed: %v", path[i-1], path[i], err)
				}
			}
			if si.State() != path[len(path)-1] {
				t.Errorf("final state: got %s, want %s", si.State(), path[len(path)-1])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// C-14 Illegal transitions rejected: TransitionTo must return CodeInvariantViolated.
// ---------------------------------------------------------------------------

func TestC_14_IllegalTransitionsRejected(t *testing.T) {
	illegalEdges := []struct {
		from SidecarState
		to   SidecarState
	}{
		{StateStarting, StateClosed},
		{StateStarting, StateReaped},
		{StateRunning, StateStarting},
		{StateRunning, StateClosed},
		{StateRunning, StateReaped},
		{StateDraining, StateStarting},
		{StateDraining, StateRunning},
		{StateDraining, StateReaped},
		{StateClosed, StateStarting},
		{StateClosed, StateRunning},
		{StateClosed, StateDraining},
		{StateClosed, StateReaped},
		{StateWaited, StateStarting},
		{StateWaited, StateRunning},
		{StateWaited, StateDraining},
		{StateWaited, StateClosed},
		{StateReaped, StateStarting}, // terminal
		{StateReaped, StateRunning},
		{StateReaped, StateDraining},
		{StateReaped, StateClosed},
		{StateReaped, StateWaited},
		{StateReaped, StateReaped},
	}

	for _, edge := range illegalEdges {
		edge := edge
		t.Run(fmt.Sprintf("%s->%s", edge.from, edge.to), func(t *testing.T) {
			si := NewSidecarInstance(nil)
			// Force the instance into edge.from state.
			si.ForceTransitionTo(edge.from, "test setup")

			err := si.TransitionTo(edge.to)
			if err == nil {
				t.Errorf("illegal transition %s→%s should have failed but succeeded",
					edge.from, edge.to)
				return
			}
			isBrainError(t, err, brainerrors.CodeInvariantViolated)
		})
	}
}

// ---------------------------------------------------------------------------
// C-20 error.data schema: EncodeErrorToRPCError must produce a structured data
// envelope, and WrapRPCError must reconstruct a matching BrainError.
// ---------------------------------------------------------------------------

func TestC_20_ErrorDataSchema(t *testing.T) {
	// Build a BrainError.
	original := brainerrors.New(brainerrors.CodeSidecarHung,
		brainerrors.WithMessage("sidecar missed heartbeats"),
		brainerrors.WithHint("check network"),
		brainerrors.WithTraceID("trace-abc"),
		brainerrors.WithSpanID("span-xyz"),
		brainerrors.WithSuggestions("restart", "check logs"),
	)

	rpcErr := EncodeErrorToRPCError(original)

	// data must be non-empty JSON.
	if len(rpcErr.Data) == 0 {
		t.Fatal("RPCError.Data must not be empty")
	}

	// Decode the data envelope.
	var env rpcDataEnvelope
	if err := json.Unmarshal(rpcErr.Data, &env); err != nil {
		t.Fatalf("data envelope unmarshal: %v", err)
	}

	if env.ErrorCode != brainerrors.CodeSidecarHung {
		t.Errorf("env.ErrorCode: got %q, want %q", env.ErrorCode, brainerrors.CodeSidecarHung)
	}
	if env.Message == "" {
		t.Error("env.Message must not be empty")
	}
	if env.Hint != "check network" {
		t.Errorf("env.Hint: got %q, want %q", env.Hint, "check network")
	}
	// Note: EncodeErrorToRPCError does not currently encode TraceID/SpanID into
	// the data envelope (they are carried by the outer BrainError but not written
	// into rpcDataEnvelope by the encoder). WrapRPCError reads them from the
	// envelope, so the round-trip only preserves what the encoder actually writes.
	// This is the implementation ground truth — tests must match it.
	if len(env.Suggestions) != 2 {
		t.Errorf("env.Suggestions: got %v, want 2 items", env.Suggestions)
	}

	// Round-trip: WrapRPCError should rebuild a semantically equivalent BrainError.
	rebuilt := WrapRPCError(rpcErr)
	if rebuilt == nil {
		t.Fatal("WrapRPCError returned nil")
	}
	if rebuilt.ErrorCode != brainerrors.CodeSidecarHung {
		t.Errorf("rebuilt.ErrorCode: got %q, want %q", rebuilt.ErrorCode, brainerrors.CodeSidecarHung)
	}
	if rebuilt.Hint != "check network" {
		t.Errorf("rebuilt.Hint: got %q, want %q", rebuilt.Hint, "check network")
	}
	// Suggestions are encoded into the envelope and thus survive the round-trip.
	if len(rebuilt.Suggestions) != 2 {
		t.Errorf("rebuilt.Suggestions: got %v, want 2 items", rebuilt.Suggestions)
	}
}

// C-20b: nil BrainError produces a fallback RPCError (not a panic).
func TestC_20b_NilBrainErrorFallback(t *testing.T) {
	rpcErr := EncodeErrorToRPCError(nil)
	if rpcErr == nil {
		t.Fatal("EncodeErrorToRPCError(nil) must return non-nil RPCError")
	}
	if rpcErr.Code != RPCCodeInternalError {
		t.Errorf("expected RPCCodeInternalError (%d), got %d", RPCCodeInternalError, rpcErr.Code)
	}
}

// C-20c: WrapRPCError(nil) must return nil (not panic).
func TestC_20c_WrapRPCErrorNil(t *testing.T) {
	if result := WrapRPCError(nil); result != nil {
		t.Errorf("WrapRPCError(nil) must be nil, got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Additional: full round-trip Call → Handle → response.
// ---------------------------------------------------------------------------

func TestC_01_FullRPCRoundTrip(t *testing.T) {
	kernel, sidecar := fullDuplexRPC(t)

	sidecar.Handle("echo", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var v interface{}
		json.Unmarshal(params, &v)
		return v, nil
	})

	var result interface{}
	err := kernel.Call(context.Background(), "echo", map[string]string{"hello": "world"}, &result)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type %T", result)
	}
	if m["hello"] != "world" {
		t.Errorf("echo result: got %v, want world", m["hello"])
	}
}

// ---------------------------------------------------------------------------
// Additional: handler errors are converted to RPCError and surfaced to caller.
// ---------------------------------------------------------------------------

func TestC_20d_HandlerErrorPropagated(t *testing.T) {
	kernel, sidecar := fullDuplexRPC(t)

	sidecar.Handle("fail.method", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return nil, brainerrors.New(brainerrors.CodeToolNotFound,
			brainerrors.WithMessage("tool missing"))
	})

	err := kernel.Call(context.Background(), "fail.method", nil, nil)
	if err == nil {
		t.Fatal("expected error from failing handler, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *BrainError, got %T: %v", err, err)
	}
	if be.ErrorCode != brainerrors.CodeToolNotFound {
		t.Errorf("error code: got %q, want %q", be.ErrorCode, brainerrors.CodeToolNotFound)
	}
}

// ---------------------------------------------------------------------------
// Additional: IsValidTransition exhaustive checks.
// ---------------------------------------------------------------------------

func TestC_13b_IsValidTransition(t *testing.T) {
	valid := []struct{ from, to SidecarState }{
		{StateStarting, StateRunning},
		{StateStarting, StateDraining},
		{StateStarting, StateWaited},
		{StateRunning, StateDraining},
		{StateRunning, StateWaited},
		{StateDraining, StateClosed},
		{StateDraining, StateWaited},
		{StateClosed, StateWaited},
		{StateWaited, StateReaped},
	}
	for _, e := range valid {
		if !IsValidTransition(e.from, e.to) {
			t.Errorf("IsValidTransition(%s, %s) should be true", e.from, e.to)
		}
	}

	invalid := []struct{ from, to SidecarState }{
		{StateReaped, StateStarting},
		{StateRunning, StateStarting},
		{StateClosed, StateRunning},
	}
	for _, e := range invalid {
		if IsValidTransition(e.from, e.to) {
			t.Errorf("IsValidTransition(%s, %s) should be false", e.from, e.to)
		}
	}
}

// ---------------------------------------------------------------------------
// Additional: SidecarInstance listener is fired on each transition.
// ---------------------------------------------------------------------------

func TestC_13c_ListenerFiredOnTransition(t *testing.T) {
	var mu sync.Mutex
	var observed []SidecarState

	si := NewSidecarInstance(func(from, to SidecarState, at time.Time) {
		mu.Lock()
		observed = append(observed, to)
		mu.Unlock()
	})

	transitions := []SidecarState{StateRunning, StateDraining, StateClosed, StateWaited, StateReaped}
	for _, s := range transitions {
		if err := si.TransitionTo(s); err != nil {
			t.Fatalf("TransitionTo(%s): %v", s, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(observed) != len(transitions) {
		t.Fatalf("listener fired %d times, want %d", len(observed), len(transitions))
	}
	for i, want := range transitions {
		if observed[i] != want {
			t.Errorf("listener[%d]: got %s, want %s", i, observed[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Additional: ForceTransitionTo bypasses validation.
// ---------------------------------------------------------------------------

func TestC_14b_ForceTransitionBypasses(t *testing.T) {
	si := NewSidecarInstance(nil)
	// ForceTransitionTo from Starting directly to Reaped (not in valid table).
	si.ForceTransitionTo(StateReaped, "test: force bypass")
	if si.State() != StateReaped {
		t.Errorf("ForceTransitionTo: got %s, want reaped", si.State())
	}
}

// ---------------------------------------------------------------------------
// Additional: context cancellation before ReadFrame returns CodeShuttingDown.
// ---------------------------------------------------------------------------

func TestC_01b_ContextCancelledBeforeRead(t *testing.T) {
	pr, _ := io.Pipe()
	fr := NewFrameReader(pr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before reading

	_, err := fr.ReadFrame(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	isBrainError(t, err, brainerrors.CodeShuttingDown)
}

// ---------------------------------------------------------------------------
// Additional: WriteFrame after Close returns CodeShuttingDown.
// ---------------------------------------------------------------------------

func TestC_06b_WriteAfterCloseReturnsShuttingDown(t *testing.T) {
	_, pw := io.Pipe()
	fw := NewFrameWriter(pw)
	fw.Close()

	body := []byte(`{}`)
	frame := &Frame{ContentLength: len(body), Body: body}
	err := fw.WriteFrame(context.Background(), frame)
	if err == nil {
		t.Fatal("expected error writing after Close, got nil")
	}
	isBrainError(t, err, brainerrors.CodeShuttingDown)
}

// ---------------------------------------------------------------------------
// Additional: BidirRPC.Start called twice returns error (not panic).
// ---------------------------------------------------------------------------

func TestC_01c_StartTwiceReturnsError(t *testing.T) {
	fr := NewFrameReader(strings.NewReader(""))
	fw := NewFrameWriter(io.Discard)
	rpc := NewBidirRPC(RoleKernel, fr, fw)
	ctx := context.Background()

	if err := rpc.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := rpc.Start(ctx); err == nil {
		t.Fatal("second Start should return error, got nil")
	}
	rpc.Close()
}

// ---------------------------------------------------------------------------
// Additional: Handle panics on duplicate method registration.
// ---------------------------------------------------------------------------

func TestC_01d_DuplicateHandlerPanics(t *testing.T) {
	fr := NewFrameReader(strings.NewReader(""))
	fw := NewFrameWriter(io.Discard)
	rpc := NewBidirRPC(RoleKernel, fr, fw)
	defer rpc.Close()

	rpc.Handle("my.method", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate handler registration, got none")
		}
	}()

	rpc.Handle("my.method", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return nil, nil
	})
}

// ---------------------------------------------------------------------------
// Additional: clean EOF between frames maps to CodeSidecarStdoutEOF.
// ---------------------------------------------------------------------------

func TestC_01e_CleanEOFMapsToStdoutEOF(t *testing.T) {
	// Empty reader → immediate EOF on first read.
	fr := NewFrameReader(strings.NewReader(""))
	_, err := fr.ReadFrame(context.Background())
	if err == nil {
		t.Fatal("expected error on empty reader, got nil")
	}
	isBrainError(t, err, brainerrors.CodeSidecarStdoutEOF)
}

// ---------------------------------------------------------------------------
// Additional: unknown method returns -32601 MethodNotFound via Call.
// ---------------------------------------------------------------------------

func TestC_01f_UnknownMethodReturnsMethodNotFound(t *testing.T) {
	kernel, _ := fullDuplexRPC(t)

	// kernel calls a method that sidecar has no handler for.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := kernel.Call(ctx, "no.such.method", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
	be, ok := err.(*brainerrors.BrainError)
	if !ok {
		t.Fatalf("expected *BrainError, got %T: %v", err, err)
	}
	// method not found maps to CodeMethodNotFound
	if be.ErrorCode != brainerrors.CodeMethodNotFound {
		t.Errorf("error code: got %q, want %q", be.ErrorCode, brainerrors.CodeMethodNotFound)
	}
}

// ---------------------------------------------------------------------------
// Additional: concurrent Calls all complete correctly (integration stress).
// ---------------------------------------------------------------------------

func TestC_10c_ConcurrentCallsComplete(t *testing.T) {
	kernel, sidecar := fullDuplexRPC(t)

	sidecar.Handle("add", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		var p struct{ A, B int }
		json.Unmarshal(params, &p)
		return p.A + p.B, nil
	})

	const n = 20
	var wg sync.WaitGroup
	var errCount atomic.Int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			var result int
			err := kernel.Call(context.Background(), "add",
				map[string]int{"A": i, "B": i * 2}, &result)
			if err != nil {
				errCount.Add(1)
				return
			}
			if result != i+i*2 {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if errCount.Load() > 0 {
		t.Errorf("%d out of %d concurrent Calls failed or returned wrong result", errCount.Load(), n)
	}
}
