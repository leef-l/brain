package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/leef-l/brain/sdk/diaglog"
	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// RPCMessage is the decoded JSON-RPC 2.0 envelope as defined in
// 20-协议规格.md §3.1. A single RPCMessage can represent any of the four
// JSON-RPC shapes — Request, Notification, Success response, Error
// response — distinguished by which of Method, Result, Error is present.
//
// Field rules (20-协议规格.md §3 and §4):
//
//   - JSONRPC MUST be the literal "2.0" on the wire.
//   - ID MUST carry the string-prefix tuple `k:<seq>` or `s:<seq>` per
//     §4.2 for Requests and responses; it MUST be omitted (or JSON null)
//     for Notifications per §4.4.
//   - Params, Result, and Error are kept as json.RawMessage so the
//     dispatcher can route frames without eagerly decoding payloads,
//     preserving zero-copy forwarding when needed.
type RPCMessage struct {
	// JSONRPC is the literal "2.0" version tag required by JSON-RPC
	// 2.0 and §3.1.
	JSONRPC string `json:"jsonrpc"`

	// ID is the string-prefix tuple defined in 20-协议规格.md §4.2.
	// Empty means "this is a notification" per §4.4.
	ID string `json:"id,omitempty"`

	// Method is the RPC method name from the methods.go namespace.
	// Non-empty on Requests and Notifications; empty on responses.
	Method string `json:"method,omitempty"`

	// Params is the raw JSON payload of a Request or Notification. The
	// dispatcher unmarshals it lazily inside the handler that owns the
	// method.
	Params json.RawMessage `json:"params,omitempty"`

	// Result is the raw JSON payload of a success response. Mutually
	// exclusive with Error per JSON-RPC 2.0.
	Result json.RawMessage `json:"result,omitempty"`

	// Error carries the structured error object documented in
	// 20-协议规格.md §9. Mutually exclusive with Result.
	Error *RPCError `json:"error,omitempty"`
}

// RPCError is the v1 JSON-RPC error object with the strict `data`
// schema from 20-协议规格.md §9.1. Data is intentionally kept as
// json.RawMessage so the wire layer neither validates nor mutates the
// BrainError payload embedded inside it — higher layers use
// errors.WrapRPCError to materialize the typed *BrainError.
type RPCError struct {
	// Code is the numeric error code, drawn from the reserved ranges in
	// 20-协议规格.md §9.2 (-32700..-32600 JSON-RPC standard,
	// -32099..-32000 BrainKernel reserved, -32800 cancelled).
	Code int `json:"code"`

	// Message is a short human-readable description. §9.1 requires it
	// to be populated for every error response.
	Message string `json:"message"`

	// Data carries the BrainError payload (class / retryable /
	// error_code / fingerprint / trace_id / cause / suggestions) per
	// 20-协议规格.md §9.1 MUST rules 1-6. Empty for standard JSON-RPC
	// errors where no BrainError context is available.
	Data json.RawMessage `json:"data,omitempty"`
}

// Role is the identity this side of the RPC session holds in the
// bidirectional id namespace from 20-协议规格.md §4.2. Host-side sessions
// run with RoleKernel (prefix "k:") and sidecar-side sessions with
// RoleSidecar (prefix "s:"). The peer's prefix is the opposite.
type Role int

const (
	// RoleKernel identifies the host process end of the session. Outbound
	// Requests from this side carry "k:" prefixed ids.
	RoleKernel Role = iota

	// RoleSidecar identifies the sidecar process end of the session.
	// Outbound Requests carry "s:" prefixed ids.
	RoleSidecar
)

// Prefix returns the one-byte id namespace prefix from §4.2. "k" for
// RoleKernel, "s" for RoleSidecar.
func (r Role) Prefix() string {
	switch r {
	case RoleKernel:
		return "k"
	case RoleSidecar:
		return "s"
	default:
		return ""
	}
}

// PeerPrefix returns the prefix the peer uses, i.e. the opposite of this
// role. Used by the response dispatcher to decide whether an inbound id
// belongs to one of our waiters or to a server handler.
func (r Role) PeerPrefix() string {
	switch r {
	case RoleKernel:
		return "s"
	case RoleSidecar:
		return "k"
	default:
		return ""
	}
}

// BidirRPC is the full-duplex JSON-RPC abstraction that sits on top of a
// FrameReader/FrameWriter pair. Implementations MUST obey the
// bidirectional id-namespace rules from 20-协议规格.md §4 — every
// outbound Request uses its own prefix (`k:` for host, `s:` for
// sidecar) and inbound frames are dispatched by prefix inspection per
// §4.3.
//
// BidirRPC is the only way higher layers emit Requests, Notifications,
// and register inbound handlers; the FrameReader/Writer are plumbing and
// should not be touched directly above this layer.
type BidirRPC interface {
	// Call sends a Request and blocks until the matching response
	// arrives or ctx is done. It MUST enforce the in-flight window
	// from 20-协议规格.md §4.6 and return a BrainError built by
	// WrapRPCError when the response carries a non-nil Error.
	Call(ctx context.Context, method string, params interface{}, result interface{}) error

	// Notify sends a Notification (no id, no response). It MUST NOT
	// allocate a seq from the id counter per §4.4.
	Notify(ctx context.Context, method string, params interface{}) error

	// Handle registers a server-side handler for the given method. It
	// is idempotent per method name but MUST panic if the same method
	// is registered twice, because that indicates a programming bug
	// rather than a recoverable condition (21-错误模型.md §3 programmer
	// errors).
	Handle(method string, handler HandlerFunc)

	// Start kicks off the reader goroutine that consumes inbound frames.
	// MUST be called exactly once per instance. The returned error is
	// non-nil only if the reader was already started.
	Start(ctx context.Context) error

	// Close tears down the session: stops the reader, fails every pending
	// waiter with CodeShuttingDown, and closes the underlying FrameWriter.
	Close() error

	// Done returns a channel that is closed when the RPC session ends
	// (either via explicit Close or stdin EOF in the readLoop). Sidecar
	// code monitors this to detect parent process exit on platforms
	// without Pdeathsig (Windows, macOS).
	Done() <-chan struct{}
}

// HandlerFunc is the server-side handler signature invoked by BidirRPC
// when an inbound Request or Notification matches a registered method.
// The returned interface{} is marshalled into RPCMessage.Result; a
// non-nil error is converted into RPCMessage.Error via WrapRPCError /
// NewProtocolError, so handlers MAY return a plain error and let the
// dispatcher route it into the BrainError taxonomy.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

// bidirRPC is the sole BidirRPC implementation that ships with the host.
// It owns the reader goroutine, the waiter table, the handler map, and
// the outbound id counter. All four are guarded by rpcMu; the handler
// map is additionally readable without the lock via atomic.Value so the
// reader hot path does not contend on registrations.
type bidirRPC struct {
	role   Role
	reader FrameReader
	writer FrameWriter

	rpcMu    sync.Mutex
	waiters  map[string]chan rpcResponse
	handlers map[string]HandlerFunc
	seq      atomic.Int64
	closed   bool

	// inFlightWindow caps concurrent outbound Requests per §4.6. For
	// RoleKernel this is 32 (SubprocessSpec default MaxConcurrentRun×8);
	// for RoleSidecar it is 64. Calls block on the in-flight slot until
	// one frees or ctx expires.
	inFlightWindow chan struct{}

	// staleCount tracks §4.5 stale-response arrivals (a response whose
	// waiter already gave up). Exported via StaleResponseCount so the
	// degrade/quarantine logic in the kernel can drive the three-state
	// health model.
	staleCount atomic.Int64

	started atomic.Bool
	done    chan struct{}

	// cancelRequestHandler is installed automatically for $/cancelRequest
	// notifications so the dispatcher can walk the waiter table and
	// cancel a pending Call without going through Handle.
	cancelMu       sync.Mutex
	inflightCancel map[string]context.CancelFunc
}

// rpcResponse is the payload delivered to a waiter. Exactly one of
// Result / Err is populated — the dispatcher sets Err when the inbound
// frame carried an RPCMessage.Error, otherwise it forwards Result.
type rpcResponse struct {
	Result json.RawMessage
	Err    *brainerrors.BrainError
}

// NewBidirRPC constructs a BidirRPC over the given frame pair. The role
// argument determines the id prefix and in-flight window. The caller is
// responsible for wiring the FrameReader/Writer to the concrete stdio
// streams — the RPC layer does not touch file descriptors directly.
func NewBidirRPC(role Role, reader FrameReader, writer FrameWriter) BidirRPC {
	window := 32
	if role == RoleSidecar {
		window = 64
	}
	r := &bidirRPC{
		role:           role,
		reader:         reader,
		writer:         writer,
		waiters:        make(map[string]chan rpcResponse),
		handlers:       make(map[string]HandlerFunc),
		inFlightWindow: make(chan struct{}, window),
		done:           make(chan struct{}),
		inflightCancel: make(map[string]context.CancelFunc),
	}
	// §4.7 $/cancelRequest is always registered — the cancellation
	// machinery is part of the protocol itself, not an optional handler.
	r.handlers["$/cancelRequest"] = r.handleCancelRequest
	return r
}

// Start spawns the reader goroutine. Idempotent at the error-return level:
// calling it twice returns an InternalBug BrainError rather than silently
// starting a second reader.
func (r *bidirRPC) Start(ctx context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage("BidirRPC.Start called twice"))
	}
	go r.readLoop(ctx)
	return nil
}

// Close stops the reader loop, fails every pending waiter with
// CodeShuttingDown, and closes the writer. Safe to call multiple times.
func (r *bidirRPC) Close() error {
	r.rpcMu.Lock()
	if r.closed {
		r.rpcMu.Unlock()
		return nil
	}
	r.closed = true
	for id, ch := range r.waiters {
		select {
		case ch <- rpcResponse{Err: brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("BidirRPC closed with waiter pending"))}:
		default:
		}
		delete(r.waiters, id)
	}
	r.rpcMu.Unlock()

	close(r.done)
	return r.writer.Close()
}

// Done returns a channel that is closed when the RPC session ends.
func (r *bidirRPC) Done() <-chan struct{} {
	return r.done
}

// Call sends a Request and blocks for the matching response. The call
// honours the §4.6 in-flight window (the channel acquire can block on a
// saturated window) and the caller's ctx (both enqueue and response wait
// are selectable on ctx.Done()).
//
// On an RPC error the returned value is already a *BrainError — callers
// can type-assert to inspect Class / ErrorCode without parsing a wire
// frame themselves.
func (r *bidirRPC) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	if err := r.acquireSlot(ctx); err != nil {
		return err
	}
	defer r.releaseSlot()

	id := r.nextID()
	ch := r.registerWaiter(id)
	defer r.clearWaiter(id)

	// Allow $/cancelRequest from the peer to cancel our ctx — that is the
	// §4.7 semantic for inbound cancel. A CancelFunc variant is registered
	// per-waiter and cleared when the waiter leaves.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.registerInflightCancel(id, cancel)
	defer r.clearInflightCancel(id)

	if err := r.sendRequest(cctx, id, method, params); err != nil {
		return err
	}

	select {
	case resp := <-ch:
		if resp.Err != nil {
			return resp.Err
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return WrapFrameError(brainerrors.CodeFrameParseError,
					fmt.Sprintf("BidirRPC: cannot unmarshal result for %s", method), err)
			}
		}
		return nil
	case <-cctx.Done():
		return WrapFrameError(brainerrors.CodeDeadlineExceeded,
			fmt.Sprintf("BidirRPC: %s cancelled before response", method), cctx.Err())
	case <-r.done:
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("BidirRPC: closed during Call"))
	}
}

// Notify sends a Notification. §4.4 mandates no id and no response, so
// Notify skips the waiter table and the in-flight window entirely — the
// writer channel back-pressure is the only rate limit.
func (r *bidirRPC) Notify(ctx context.Context, method string, params interface{}) error {
	return r.sendRequest(ctx, "", method, params)
}

// Handle registers a server-side handler. Duplicate registrations panic
// per the doc comment — the rationale is that two packages registering
// the same method name is a programmer error, not a recoverable failure,
// and crashing early surfaces the bug during integration.
func (r *bidirRPC) Handle(method string, handler HandlerFunc) {
	r.rpcMu.Lock()
	defer r.rpcMu.Unlock()
	if _, exists := r.handlers[method]; exists {
		panic(fmt.Sprintf("BidirRPC: duplicate handler registration for %q", method))
	}
	r.handlers[method] = handler
}

// StaleResponseCount returns the total number of stale-response drops per
// §4.5 over the lifetime of this session. The kernel watchdog reads this
// value periodically and trips the degraded→quarantined escalation when
// it exceeds the threshold.
func (r *bidirRPC) StaleResponseCount() int64 {
	return r.staleCount.Load()
}

// nextID allocates a new "<prefix>:<seq>" id. Uses atomic counter for
// lock-free allocation on the hot path.
func (r *bidirRPC) nextID() string {
	seq := r.seq.Add(1)
	return r.role.Prefix() + ":" + strconv.FormatInt(seq, 10)
}

// sendRequest encodes and writes an RPCMessage. If id is empty the
// message is a Notification (no id field on the wire).
func (r *bidirRPC) sendRequest(ctx context.Context, id, method string, params interface{}) error {
	msg := RPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return WrapFrameError(brainerrors.CodeFrameEncodingError,
				fmt.Sprintf("BidirRPC: cannot marshal params for %s", method), err)
		}
		msg.Params = raw
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return WrapFrameError(brainerrors.CodeFrameEncodingError,
			fmt.Sprintf("BidirRPC: cannot marshal message for %s", method), err)
	}
	frame := &Frame{
		ContentLength: len(body),
		ContentType:   CanonicalContentType,
		Body:          body,
	}
	return r.writer.WriteFrame(ctx, frame)
}

// registerWaiter creates a channel for the given id and stores it in the
// waiter table. The channel is buffered (cap 1) so the reader goroutine
// never blocks on delivery — if the caller has already left, the slot
// fills and the reader falls through to the stale-response path.
func (r *bidirRPC) registerWaiter(id string) chan rpcResponse {
	ch := make(chan rpcResponse, 1)
	r.rpcMu.Lock()
	r.waiters[id] = ch
	r.rpcMu.Unlock()
	return ch
}

// clearWaiter removes the waiter from the table. Called on the defer
// path of Call so that an early ctx cancellation does not leak entries.
func (r *bidirRPC) clearWaiter(id string) {
	r.rpcMu.Lock()
	delete(r.waiters, id)
	r.rpcMu.Unlock()
}

// acquireSlot enqueues on the in-flight window channel. Blocks until a
// slot frees, ctx expires, or the session closes.
func (r *bidirRPC) acquireSlot(ctx context.Context) error {
	select {
	case r.inFlightWindow <- struct{}{}:
		return nil
	case <-ctx.Done():
		return WrapFrameError(brainerrors.CodeDeadlineExceeded,
			"BidirRPC: in-flight window acquire cancelled", ctx.Err())
	case <-r.done:
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("BidirRPC: closed waiting for slot"))
	}
}

// releaseSlot returns a slot to the in-flight window.
func (r *bidirRPC) releaseSlot() {
	select {
	case <-r.inFlightWindow:
	default:
		// Slot was never acquired — this is a programmer bug but we do
		// not panic here because Call already defers release immediately
		// after acquire, so the path is unreachable in correct code.
	}
}

// registerInflightCancel stores the cancel func for a pending Call so the
// $/cancelRequest handler can fire it.
func (r *bidirRPC) registerInflightCancel(id string, cancel context.CancelFunc) {
	r.cancelMu.Lock()
	r.inflightCancel[id] = cancel
	r.cancelMu.Unlock()
}

func (r *bidirRPC) clearInflightCancel(id string) {
	r.cancelMu.Lock()
	delete(r.inflightCancel, id)
	r.cancelMu.Unlock()
}

// handleCancelRequest is the built-in $/cancelRequest notification
// handler per §4.7. It parses {"id": "..."} from the params, walks the
// inflight-cancel map, and fires the matching CancelFunc. Unknown ids
// are silently ignored as the spec requires.
func (r *bidirRPC) handleCancelRequest(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, WrapFrameError(brainerrors.CodeInvalidParams,
			"BidirRPC: $/cancelRequest params invalid", err)
	}
	r.cancelMu.Lock()
	cancel := r.inflightCancel[payload.ID]
	r.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil, nil
}

// readLoop is the single reader goroutine. It pulls frames off the
// FrameReader and dispatches each RPCMessage into one of four buckets:
//
//  1. Response with id → self-prefixed: waiter lookup.
//  2. Response with id → peer-prefixed: protocol violation, drop + warn.
//  3. Request with id → peer-prefixed: handler dispatch.
//  4. Notification (no id) → handler dispatch, no response.
//
// Stale responses (waiter already gone) are counted via staleCount and
// logged but not otherwise surfaced — §4.5 explicitly permits dropping
// them.
func (r *bidirRPC) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		default:
		}

		frame, err := r.reader.ReadFrame(ctx)
		if err != nil {
			// Any read error surfaces to the rest of the system by
			// closing the session. The specific error code is already
			// tagged by the FrameReader so downstream lifecycle code
			// knows whether this was a clean EOF or a parser violation.
			r.Close()
			return
		}

		var msg RPCMessage
		if err := json.Unmarshal(frame.Body, &msg); err != nil {
			// Malformed JSON from the peer. v1 mandates we respond with
			// -32600 when possible — but we do NOT know which id it was
			// for, so we just drop and count. A flood of these should
			// trip the watchdog via staleCount-like metrics.
			continue
		}
		if msg.JSONRPC != "2.0" {
			continue
		}

		r.dispatch(ctx, &msg)
	}
}

// dispatch is the §4.3 id-prefix routing logic. It categorizes the inbound
// message and hands it off to the appropriate downstream (waiter channel
// or handler goroutine).
func (r *bidirRPC) dispatch(ctx context.Context, msg *RPCMessage) {
	// Notifications have no id. The method is mandatory; if it is empty
	// we have a malformed frame and drop it.
	if msg.ID == "" {
		if msg.Method == "" {
			return
		}
		r.dispatchRequest(ctx, msg, false)
		return
	}

	// Responses carry no method field. Requests carry a method.
	if msg.Method != "" {
		// Inbound Request from the peer. §4.3 requires the id to carry
		// the peer prefix; otherwise the peer is violating its own id
		// namespace and we drop the frame with a warning.
		if !strings.HasPrefix(msg.ID, r.role.PeerPrefix()+":") {
			return
		}
		r.dispatchRequest(ctx, msg, true)
		return
	}

	// Response from a previous outbound Call. The id MUST carry OUR
	// prefix per §4.3 — otherwise the peer is echoing a garbled id.
	if !strings.HasPrefix(msg.ID, r.role.Prefix()+":") {
		return
	}

	r.rpcMu.Lock()
	ch, ok := r.waiters[msg.ID]
	r.rpcMu.Unlock()
	if !ok {
		// §4.5 stale response. Count and drop.
		r.staleCount.Add(1)
		return
	}

	resp := rpcResponse{Result: msg.Result}
	if msg.Error != nil {
		resp.Err = WrapRPCError(msg.Error)
	}
	select {
	case ch <- resp:
	default:
		// Buffer is size 1 — this branch should never fire unless the
		// peer sent two responses for the same id. Count and drop.
		r.staleCount.Add(1)
	}
}

// dispatchRequest runs a handler in a fresh goroutine so a slow handler
// cannot block the reader. When expectResponse is true, the result or
// error is serialized into a response frame and pushed through the
// writer.
func (r *bidirRPC) dispatchRequest(ctx context.Context, msg *RPCMessage, expectResponse bool) {
	r.rpcMu.Lock()
	handler := r.handlers[msg.Method]
	r.rpcMu.Unlock()

	if handler == nil {
		if !expectResponse {
			return
		}
		r.sendErrorResponse(ctx, msg.ID, &RPCError{
			Code:    RPCCodeMethodNotFound,
			Message: "method not found: " + msg.Method,
		})
		return
	}

	go func() {
		result, err := handler(ctx, msg.Params)
		if !expectResponse {
			return
		}
		if err != nil {
			var be *brainerrors.BrainError
			// Accept both BrainError and plain error — plain errors get
			// promoted to CodeUnknown by the helper.
			if cast, ok := err.(*brainerrors.BrainError); ok {
				be = cast
			} else {
				be = brainerrors.Wrap(err, brainerrors.CodeBrainTaskFailed,
					brainerrors.WithMessage(err.Error()))
			}
			r.sendErrorResponse(ctx, msg.ID, EncodeErrorToRPCError(be))
			return
		}
		r.sendResultResponse(ctx, msg.ID, result)
	}()
}

// sendResultResponse writes a success response frame for id / result.
func (r *bidirRPC) sendResultResponse(ctx context.Context, id string, result interface{}) {
	msg := RPCMessage{
		JSONRPC: "2.0",
		ID:      id,
	}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			r.sendErrorResponse(ctx, id, &RPCError{
				Code:    RPCCodeInternalError,
				Message: "result marshal failed: " + err.Error(),
			})
			return
		}
		msg.Result = raw
	} else {
		msg.Result = json.RawMessage("null")
	}
	body, err := json.Marshal(msg)
	if err != nil {
		diaglog.Logf("rpc", "send result response marshal failed id=%s err=%v", id, err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, DefaultWriteTimeout)
	defer cancel()
	if err := r.writer.WriteFrame(writeCtx, &Frame{
		ContentLength: len(body),
		ContentType:   CanonicalContentType,
		Body:          body,
	}); err != nil {
		diaglog.Logf("rpc", "send result response write failed id=%s err=%v", id, err)
	}
}

// sendErrorResponse writes a protocol error response frame for id / err.
func (r *bidirRPC) sendErrorResponse(ctx context.Context, id string, rpcErr *RPCError) {
	msg := RPCMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		diaglog.Logf("rpc", "send error response marshal failed id=%s err=%v", id, err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, DefaultWriteTimeout)
	defer cancel()
	if err := r.writer.WriteFrame(writeCtx, &Frame{
		ContentLength: len(body),
		ContentType:   CanonicalContentType,
		Body:          body,
	}); err != nil {
		diaglog.Logf("rpc", "send error response write failed id=%s err=%v", id, err)
	}
}
