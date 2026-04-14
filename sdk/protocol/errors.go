// Package protocol implements the stdio wire protocol defined in
// 20-协议规格.md — the frozen v1 contract between the BrainKernel host
// process and each brain sidecar.
//
// The package is deliberately split into five files that mirror the spec
// layout:
//
//   - frame.go     — §2 transport-layer framing (header block + body).
//   - rpc.go       — §3 and §4 JSON-RPC 2.0 session layer and the
//     bidirectional id namespace.
//   - lifecycle.go — §6 initialize / shutdown handshake payloads and the
//     5-state state machine that governs every sidecar session.
//   - methods.go   — §10 frozen v1 method-name namespace.
//   - errors.go    — §9 helpers that wrap protocol-layer failures into the
//     shared *errors.BrainError carrier.
//
// protocol sits just above the errors subpackage in the dependency topology:
// it imports brain/errors to build typed wire-layer failures, but it MUST
// NOT import any other brain subpackage. Higher-level layers (agent, loop,
// runner) wire the protocol package up by composition — see
// 20-协议规格.md §1 for the rationale of keeping the wire protocol a leaf.
//
// This package has zero external dependencies and uses only the Go standard
// library, per the rules in brain骨架实施计划.md §4.6.
package protocol

import (
	"encoding/json"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

// Pre-defined JSON-RPC numeric error codes from 20-协议规格.md §9.2. The
// file maintains the single source of truth for the numeric ↔ error_code
// mapping. Any call site that needs to emit a protocol-layer failure MUST
// route through one of the helpers below so the mapping stays centralized
// and WrapRPCError can round-trip inbound frames symmetrically.
const (
	// JSON-RPC 2.0 standard range (20 §9.2). The five standard codes
	// defined by the JSON-RPC spec itself — v1 uses them verbatim for
	// transport/protocol-level failures that have no BrainError analogue.

	// RPCCodeParseError — JSON parse failure on an inbound frame body.
	RPCCodeParseError = -32700

	// RPCCodeInvalidRequest — structurally valid JSON but missing required
	// fields (e.g. no jsonrpc version, no method on a Request).
	RPCCodeInvalidRequest = -32600

	// RPCCodeMethodNotFound — method name not registered in the dispatcher.
	RPCCodeMethodNotFound = -32601

	// RPCCodeInvalidParams — method found but params failed schema check.
	RPCCodeInvalidParams = -32602

	// RPCCodeInternalError — unspecified internal failure on the server.
	RPCCodeInternalError = -32603

	// BrainKernel reserved range (20 §9.2). -32099..-32000. The concrete
	// assignments below are frozen; adding new codes requires a spec bump.

	// RPCCodeShuttingDown — the peer is in Draining and refuses new
	// requests. Maps to CodeShuttingDown / ClassTransient.
	RPCCodeShuttingDown = -32099

	// RPCCodeFrameTooLarge — a frame exceeded the 16 MiB body cap from §2.4.
	RPCCodeFrameTooLarge = -32098

	// RPCCodeSidecarHung — a write or heartbeat deadline expired and the
	// peer is presumed hung. Maps to CodeSidecarHung.
	RPCCodeSidecarHung = -32097

	// RPCCodeBrainOverloaded — the peer in-flight queue is full.
	RPCCodeBrainOverloaded = -32096

	// RPCCodeBusinessError — generic BrainKernel business failure. The
	// concrete error_code is carried in data.error_code per 21 §5.
	RPCCodeBusinessError = -32000

	// Cancelled — 20 §9.2 / §4.7. The peer asked to cancel the request.
	RPCCodeCancelled = -32800
)

// rpcDataEnvelope is the wire shape of RPCError.Data as frozen in
// 20-协议规格.md §9.1. The data object mirrors a sanitized BrainError tree:
// Class / Retryable / ErrorCode / Fingerprint / TraceID / SpanID / Cause /
// Suggestions / Message are all allowed. InternalOnly is intentionally NOT
// represented here — the wire layer MUST NOT encode it.
//
// The envelope is private to the protocol package; callers build wire
// payloads via WrapFrameError / NewProtocolError and consume inbound ones
// via WrapRPCError.
type rpcDataEnvelope struct {
	Class       string             `json:"class,omitempty"`
	Retryable   bool               `json:"retryable,omitempty"`
	ErrorCode   string             `json:"error_code,omitempty"`
	Fingerprint string             `json:"fingerprint,omitempty"`
	TraceID     string             `json:"trace_id,omitempty"`
	SpanID      string             `json:"span_id,omitempty"`
	Message     string             `json:"message,omitempty"`
	Hint        string             `json:"hint,omitempty"`
	Cause       *rpcCauseEnvelope  `json:"cause,omitempty"`
	Suggestions []string           `json:"suggestions,omitempty"`
}

// rpcCauseEnvelope is a flattened subset of rpcDataEnvelope used for the
// Cause chain on the wire. It is a distinct type to prevent accidental
// recursion into internal-only fields and to keep the JSON tree bounded.
type rpcCauseEnvelope struct {
	Class     string `json:"class,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

// NewProtocolError wraps a raw protocol-layer failure into a
// *errors.BrainError so that callers above the wire layer see the same
// BrainError contract regardless of whether the failure originated on the
// frame reader, the JSON-RPC dispatcher, or the lifecycle handshake.
//
// The helper is the single entry point through which wire-layer code is
// allowed to construct BrainErrors; see 20-协议规格.md §9 for the wire
// encoding of the resulting error frame and 21-错误模型.md §3.3 for the
// BrainError construction contract that the returned value MUST satisfy.
//
// code MUST be a reserved error_code from 21 appendix A. When the code is
// not registered, the errors.New fallback lands the failure in CodeUnknown
// with a registry-violation flag so the alert pipeline can fire.
func NewProtocolError(code string, msg string) *brainerrors.BrainError {
	return brainerrors.New(code,
		brainerrors.WithMessage(msg),
	)
}

// WrapFrameError converts a low-level frame reader/writer failure (for
// example a malformed header block, an oversized body, or a write-timeout)
// into a *errors.BrainError tagged with the appropriate frame.* error_code
// from the reserved clause in 21-错误模型.md §5. The raw cause is preserved
// on BrainError.Cause to comply with 20-协议规格.md §9.1 MUST rule 1.
//
// Passing a nil cause is legal — callers that synthesize a protocol error
// without an underlying Go error (e.g. a header-line length overflow
// detected by scanning) can call WrapFrameError with cause == nil and the
// returned BrainError will have no Cause set, identical to NewProtocolError
// plus a default Message.
func WrapFrameError(code string, msg string, cause error) *brainerrors.BrainError {
	return brainerrors.Wrap(cause, code,
		brainerrors.WithMessage(msg),
	)
}

// WrapRPCError converts an inbound JSON-RPC error object (the wire
// representation defined in RPCError) into a *errors.BrainError that the
// host-process business layer can consume uniformly. The mapping from the
// JSON-RPC numeric code to ErrorClass / error_code follows 20-协议规格.md
// §9.2, with any fields present in data.error_code taking precedence over
// the numeric code — the numeric code is a coarse bucket, the data
// envelope carries the exact BrainError context.
//
// Returns nil when rpcErr is nil so callers can pipe a Response.Error
// through WrapRPCError unconditionally.
func WrapRPCError(rpcErr *RPCError) *brainerrors.BrainError {
	if rpcErr == nil {
		return nil
	}

	// Extract BrainError context from data when present. A well-formed
	// sidecar response carries the full envelope and we can rebuild a
	// high-fidelity BrainError; a non-BrainError peer just provides the
	// numeric code + message, which falls through to the numeric mapping.
	envelope, ok := decodeDataEnvelope(rpcErr.Data)
	code := ""
	if ok && envelope.ErrorCode != "" {
		code = envelope.ErrorCode
	} else {
		code = codeFromNumeric(rpcErr.Code)
	}

	opts := []brainerrors.Option{
		brainerrors.WithMessage(rpcErr.Message),
	}
	if ok {
		if envelope.Hint != "" {
			opts = append(opts, brainerrors.WithHint(envelope.Hint))
		}
		if envelope.TraceID != "" {
			opts = append(opts, brainerrors.WithTraceID(envelope.TraceID))
		}
		if envelope.SpanID != "" {
			opts = append(opts, brainerrors.WithSpanID(envelope.SpanID))
		}
		if len(envelope.Suggestions) > 0 {
			opts = append(opts, brainerrors.WithSuggestions(envelope.Suggestions...))
		}
		if envelope.Class != "" {
			opts = append(opts, brainerrors.WithClass(brainerrors.ErrorClass(envelope.Class)))
		}
		if envelope.Cause != nil {
			cause := brainerrors.New(envelope.Cause.ErrorCode,
				brainerrors.WithMessage(envelope.Cause.Message),
			)
			if envelope.Cause.Class != "" {
				brainerrors.WithClass(brainerrors.ErrorClass(envelope.Cause.Class))(cause)
			}
			opts = append(opts, brainerrors.WithCause(cause))
		}
	}

	return brainerrors.New(code, opts...)
}

// decodeDataEnvelope attempts to unmarshal the RPCError.Data blob into the
// structured envelope. Returns ok=false when data is nil/empty or the JSON
// is not an object — the caller then falls back to numeric-code-only
// reconstruction.
func decodeDataEnvelope(data json.RawMessage) (rpcDataEnvelope, bool) {
	if len(data) == 0 {
		return rpcDataEnvelope{}, false
	}
	var env rpcDataEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return rpcDataEnvelope{}, false
	}
	return env, true
}

// codeFromNumeric maps a raw JSON-RPC numeric code to an error_code constant
// per 20-协议规格.md §9.2. Unknown numerics fall through to CodeUnknown so
// the registry-violation machinery can alert.
func codeFromNumeric(code int) string {
	switch code {
	case RPCCodeParseError:
		return brainerrors.CodeFrameParseError
	case RPCCodeInvalidRequest:
		return brainerrors.CodeInvalidParams
	case RPCCodeMethodNotFound:
		return brainerrors.CodeMethodNotFound
	case RPCCodeInvalidParams:
		return brainerrors.CodeInvalidParams
	case RPCCodeInternalError:
		return brainerrors.CodeAssertionFailed
	case RPCCodeShuttingDown:
		return brainerrors.CodeShuttingDown
	case RPCCodeFrameTooLarge:
		return brainerrors.CodeFrameTooLarge
	case RPCCodeSidecarHung:
		return brainerrors.CodeSidecarHung
	case RPCCodeBrainOverloaded:
		return brainerrors.CodeBrainOverloaded
	case RPCCodeBusinessError:
		return brainerrors.CodeBrainTaskFailed
	case RPCCodeCancelled:
		// A cancelled request is not a protocol violation — it is the
		// happy path for §4.7 $/cancelRequest. Callers that interpret
		// this as an error should check for ClassInternalBug and treat
		// Cancelled as a normal termination instead.
		return brainerrors.CodeStaleResponse
	default:
		return brainerrors.CodeUnknown
	}
}

// EncodeErrorToRPCError encodes a BrainError into the wire RPCError shape
// per 20 §9.1. Used by the server side of BidirRPC when a handler returns
// an error. InternalOnly is stripped before encoding — the SanitizeForWire
// call mirrors the defensive filter in errors.MarshalJSON so any leak of
// stack / raw_stderr would require a bug in both layers.
//
// The returned RPCError is always non-nil; a nil BrainError turns into a
// generic internal-error placeholder so callers can always emit some error
// frame and never drop silently.
func EncodeErrorToRPCError(err *brainerrors.BrainError) *RPCError {
	if err == nil {
		return &RPCError{
			Code:    RPCCodeInternalError,
			Message: "nil brain error",
		}
	}
	clean := brainerrors.SanitizeForWire(err)

	env := rpcDataEnvelope{
		Class:       string(clean.Class),
		Retryable:   clean.Retryable,
		ErrorCode:   clean.ErrorCode,
		Fingerprint: clean.Fingerprint,
		Message:     clean.Message,
		Hint:        clean.Hint,
		Suggestions: clean.Suggestions,
	}
	if clean.Cause != nil {
		env.Cause = &rpcCauseEnvelope{
			Class:     string(clean.Cause.Class),
			ErrorCode: clean.Cause.ErrorCode,
			Message:   clean.Cause.Message,
		}
	}
	dataBytes, _ := json.Marshal(env)

	return &RPCError{
		Code:    numericFromCode(clean.ErrorCode),
		Message: clean.Message,
		Data:    dataBytes,
	}
}

// numericFromCode is the inverse of codeFromNumeric for the small set of
// error_codes that have a dedicated reserved numeric slot per §9.2.
// Everything else lands on RPCCodeBusinessError (-32000) and relies on
// data.error_code for the concrete taxonomy.
func numericFromCode(code string) int {
	switch code {
	case brainerrors.CodeFrameParseError:
		return RPCCodeParseError
	case brainerrors.CodeFrameTooLarge:
		return RPCCodeFrameTooLarge
	case brainerrors.CodeMethodNotFound:
		return RPCCodeMethodNotFound
	case brainerrors.CodeInvalidParams:
		return RPCCodeInvalidParams
	case brainerrors.CodeShuttingDown:
		return RPCCodeShuttingDown
	case brainerrors.CodeSidecarHung:
		return RPCCodeSidecarHung
	case brainerrors.CodeBrainOverloaded:
		return RPCCodeBrainOverloaded
	default:
		return RPCCodeBusinessError
	}
}
