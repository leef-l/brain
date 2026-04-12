package kernel

import (
	"context"

	"github.com/leef-l/brain/protocol"
)

// BrainTransport abstracts the underlying wire used to carry stdio-framed
// RPC messages between the Kernel and a brain sidecar.
//
// The concrete transport is almost always paired stdio pipes (02 §12.5.1),
// but the interface is kept abstract so third parties can plug in unix
// sockets, in-process channels, or test harnesses without touching the
// Kernel core.
//
// Framing and message shape are defined in 20-协议规格.md §3 (Content-Length)
// and §4 (bidirectional JSON-RPC).
type BrainTransport interface {
	// Send writes one RPC message to the peer. It must respect the framing
	// rules in 20 §3.2 and return quickly; long I/O should be pushed to an
	// internal writer goroutine.
	Send(ctx context.Context, msg *protocol.RPCMessage) error

	// Receive blocks until the next RPC message arrives, or the context is
	// cancelled. A closed peer should produce a `ProtocolClosed` BrainError.
	Receive(ctx context.Context) (*protocol.RPCMessage, error)

	// Close shuts down the transport. It MUST be idempotent.
	Close() error
}
