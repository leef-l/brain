package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	brainerrors "github.com/leef-l/brain/errors"
	"github.com/leef-l/brain/protocol"
)

// StdioTransport implements BrainTransport over a pair of stdio streams
// using the Content-Length framing protocol from 20-协议规格.md §2.
//
// It wraps a protocol.FrameReader (sidecar stdout → kernel) and a
// protocol.FrameWriter (kernel → sidecar stdin), converting between
// protocol.RPCMessage and raw wire frames.
//
// StdioTransport is the concrete transport used by ProcessRunner when
// launching sidecar binaries via fork/exec. It can also be constructed
// directly over io.Pipe pairs for testing.
type StdioTransport struct {
	reader protocol.FrameReader
	writer protocol.FrameWriter

	mu     sync.Mutex
	closed bool
}

// NewStdioTransport creates a transport over the given reader (sidecar
// stdout) and writer (sidecar stdin) streams. The caller is responsible
// for providing the correct stream direction.
func NewStdioTransport(r io.Reader, w io.Writer) *StdioTransport {
	return &StdioTransport{
		reader: protocol.NewFrameReader(r),
		writer: protocol.NewFrameWriter(w),
	}
}

// NewStdioTransportFromFrames creates a transport from pre-built
// FrameReader/FrameWriter. Useful for testing with custom frame
// implementations.
func NewStdioTransportFromFrames(reader protocol.FrameReader, writer protocol.FrameWriter) *StdioTransport {
	return &StdioTransport{
		reader: reader,
		writer: writer,
	}
}

// Send encodes an RPCMessage into JSON and writes it as a Content-Length
// framed message. See 20-协议规格.md §3.2.
func (t *StdioTransport) Send(ctx context.Context, msg *protocol.RPCMessage) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("StdioTransport.Send: transport closed"))
	}
	t.mu.Unlock()

	if msg == nil {
		return brainerrors.New(brainerrors.CodeFrameEncodingError,
			brainerrors.WithMessage("StdioTransport.Send: nil message"))
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return brainerrors.New(brainerrors.CodeFrameEncodingError,
			brainerrors.WithMessage(fmt.Sprintf("StdioTransport.Send: marshal failed: %v", err)))
	}

	frame := &protocol.Frame{
		ContentLength: len(body),
		ContentType:   protocol.CanonicalContentType,
		Body:          body,
	}

	return t.writer.WriteFrame(ctx, frame)
}

// Receive reads the next Content-Length framed message from the sidecar
// stdout and decodes it into an RPCMessage.
func (t *StdioTransport) Receive(ctx context.Context) (*protocol.RPCMessage, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("StdioTransport.Receive: transport closed"))
	}
	t.mu.Unlock()

	frame, err := t.reader.ReadFrame(ctx)
	if err != nil {
		return nil, err
	}

	var msg protocol.RPCMessage
	if err := json.Unmarshal(frame.Body, &msg); err != nil {
		return nil, brainerrors.New(brainerrors.CodeFrameParseError,
			brainerrors.WithMessage(fmt.Sprintf("StdioTransport.Receive: JSON decode failed: %v", err)))
	}

	return &msg, nil
}

// Close shuts down the transport by closing the underlying writer.
// Idempotent per the BrainTransport contract.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return t.writer.Close()
}
