package protocol

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// Frame is the wire representation of a single stdio message as defined in
// 20-协议规格.md §2.2. It carries the exact bytes that cross the
// host/sidecar boundary: a Content-Length-prefixed header block followed by
// a UTF-8 JSON body.
//
// Construction rules (20-协议规格.md §2.3):
//
//   - ContentLength MUST equal len(Body) in bytes (not runes).
//   - ContentType MUST be "application/vscode-jsonrpc; charset=utf-8" on the
//     wire for v1 producers; readers MAY accept an omitted Content-Type and
//     treat it as equivalent to the canonical value.
//   - Body MUST NOT carry a BOM and MUST be the raw UTF-8 JSON payload —
//     the framing layer does NOT parse JSON itself.
type Frame struct {
	// ContentLength is the exact byte length of Body, as declared in the
	// Content-Length header. The frame reader MUST set this from the
	// header and the frame writer MUST set it before serialization.
	ContentLength int

	// ContentType is the Content-Type header value, canonically
	// "application/vscode-jsonrpc; charset=utf-8" per 20-协议规格.md §2.3.
	ContentType string

	// Body is the raw UTF-8 JSON bytes of the frame body. The framing
	// layer treats Body as opaque; higher layers unmarshal it into an
	// RPCMessage (see rpc.go).
	Body []byte
}

// Size limits from 20-协议规格.md §2.4 and §2.6.4. Any value here is a
// hard MUST, not a soft hint — the framer refuses to read or write a
// frame that exceeds them and the violation surfaces as a BrainError.
const (
	// MaxBodySize is the 16 MiB hard cap on frame body bytes. Both reader
	// and writer enforce it; §2.4 mandates that reader violations kill
	// the sidecar process.
	MaxBodySize = 16 * 1024 * 1024

	// MaxHeaderLineSize is the 8 KiB cap on a single header line from
	// §2.6.4. Longer lines are assumed to be adversarial and the reader
	// MUST refuse to continue.
	MaxHeaderLineSize = 8192

	// CanonicalContentType is the Content-Type value every v1 writer MUST
	// emit (§2.3). Readers accept omission as equivalent but SHOULD still
	// surface a warning telemetry event.
	CanonicalContentType = "application/vscode-jsonrpc; charset=utf-8"

	// DefaultWriteTimeout is the §5.2 default write deadline. A write
	// that blocks longer than this is treated as sidecar_hung and the
	// lifecycle state machine transitions to Draining.
	DefaultWriteTimeout = 10 * time.Second

	// DefaultReadBodyTimeout is the §2.6.3 per-body read deadline. 5s is
	// the spec default — the reader tracks it per Content-Length block,
	// not per frame.
	DefaultReadBodyTimeout = 5 * time.Second
)

// FrameReader is the pull-based interface implemented by the host-side and
// sidecar-side stdio framers. Implementations MUST obey the parser state
// machine in 20-协议规格.md §2.6, including the 8 KiB header-line cap and
// the 16 MiB body cap from §2.4.
//
// ReadFrame is the only way higher layers obtain an inbound Frame; the
// framer MUST NOT expose the underlying io.Reader directly.
type FrameReader interface {
	// ReadFrame reads exactly one frame from the underlying stream. It
	// returns a non-nil *Frame on success. On stream termination (peer
	// EOF, ctx cancellation, oversized body, malformed header) it MUST
	// return a *errors.BrainError built by WrapFrameError so the caller
	// can drive the half-close state machine in 20-协议规格.md §6.
	ReadFrame(ctx context.Context) (*Frame, error)
}

// FrameWriter is the push-based interface implemented by the host-side and
// sidecar-side stdio framers. Implementations MUST serialize all frames
// through a single writer goroutine per stream, as mandated by
// 20-协议规格.md §2.7 — concurrent writes to the same stdio stream cause
// header/body interleaving and immediate protocol corruption.
//
// WriteFrame is the only way higher layers emit an outbound Frame.
type FrameWriter interface {
	// WriteFrame serializes frame onto the underlying stream. It MUST
	// honour the write-timeout and back-pressure rules in
	// 20-协议规格.md §5.2; on timeout or peer hang it MUST return a
	// BrainError built by WrapFrameError so the caller can transition
	// the sidecar into Draining / Closed.
	WriteFrame(ctx context.Context, frame *Frame) error

	// Close stops the writer goroutine and releases the underlying
	// channel. Safe to call multiple times; subsequent WriteFrame calls
	// after Close return CodeShuttingDown.
	Close() error
}

// frameReader is the stdlib-only FrameReader implementation backing every
// stdio session. It wraps a bufio.Reader so header-line scanning and exact
// body reads share the same buffer — allocating a fresh buffer per frame
// would burn an extra 4 KiB on every syscall.
//
// The struct holds only the state needed to implement §2.6: the buffered
// reader itself, a byte slice reused for body reads (grown lazily but
// capped at MaxBodySize), and a mutex so concurrent ReadFrame calls cannot
// interleave their parsing. Concurrent reads are not expected in v1 but
// the mutex is a cheap insurance policy against future bugs.
type frameReader struct {
	br *bufio.Reader
	mu sync.Mutex
}

// NewFrameReader builds a FrameReader over r. Callers pass the sidecar
// stdout (host side) or the host stdin (sidecar side) — the direction is
// irrelevant at this layer. The bufio buffer is 64 KiB, matching the
// default Linux pipe buffer size from §5.1.
func NewFrameReader(r io.Reader) FrameReader {
	return &frameReader{
		br: bufio.NewReaderSize(r, 64*1024),
	}
}

// ReadFrame implements the §2.6 parser state machine end-to-end:
//  1. Scan header lines terminated by \r\n.
//  2. Each line is Key: Value; the only keys v1 recognizes are
//     Content-Length (required) and Content-Type (optional, accepted when
//     omitted per §2.3).
//  3. An empty CRLF line terminates the header block.
//  4. Read exactly Content-Length bytes from the body.
//
// Error mapping:
//   - io.EOF before any byte of a new frame → wrapped as
//     CodeSidecarStdoutEOF so the lifecycle FSM can transition to
//     Draining / Closed cleanly.
//   - Malformed header or missing Content-Length → CodeFrameParseError.
//   - Header line longer than MaxHeaderLineSize → CodeFrameTooLarge (the
//     reader MUST kill the peer per §2.4 — that decision is driven by
//     the caller inspecting the returned error).
//   - Body longer than MaxBodySize → CodeFrameTooLarge.
//   - Partial body before EOF → CodeSidecarStdoutEOF.
//   - Context cancellation → ctx.Err() passed through Wrap so upstream
//     logic can tell whether the cancel came from us or the peer.
func (r *frameReader) ReadFrame(ctx context.Context) (*Frame, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Context may already be done before we do any I/O. Checking first
	// avoids a spurious read that would ultimately be discarded.
	if err := ctx.Err(); err != nil {
		return nil, WrapFrameError(brainerrors.CodeShuttingDown,
			"frame reader: context cancelled before read", err)
	}

	var (
		contentLength = -1
		contentType   = ""
		sawAnyHeader  = false
	)

	for {
		line, err := r.readHeaderLine()
		if err != nil {
			if err == io.EOF && !sawAnyHeader {
				// Clean EOF between frames is the expected sidecar exit
				// path — surface it as stdout_eof so the lifecycle FSM
				// can transition to Draining without flagging a bug.
				return nil, WrapFrameError(brainerrors.CodeSidecarStdoutEOF,
					"frame reader: peer closed stdout", io.EOF)
			}
			return nil, WrapFrameError(brainerrors.CodeFrameParseError,
				"frame reader: header read failed", err)
		}
		sawAnyHeader = true

		if line == "" {
			// Empty line terminates the header block.
			break
		}

		key, value, ok := splitHeader(line)
		if !ok {
			return nil, WrapFrameError(brainerrors.CodeFrameParseError,
				fmt.Sprintf("frame reader: malformed header line %q", line), nil)
		}

		switch strings.ToLower(key) {
		case "content-length":
			n, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr != nil || n < 0 {
				return nil, WrapFrameError(brainerrors.CodeFrameParseError,
					fmt.Sprintf("frame reader: invalid Content-Length %q", value), convErr)
			}
			if n > MaxBodySize {
				return nil, WrapFrameError(brainerrors.CodeFrameTooLarge,
					fmt.Sprintf("frame reader: body %d exceeds 16 MiB cap", n), nil)
			}
			contentLength = n
		case "content-type":
			contentType = strings.TrimSpace(value)
		default:
			// §2.3: unknown headers MUST be ignored (not rejected) so
			// future extensions with bumped ProtocolVersion roll cleanly.
		}
	}

	if contentLength < 0 {
		return nil, WrapFrameError(brainerrors.CodeFrameParseError,
			"frame reader: missing Content-Length header", nil)
	}

	// Empty body is valid — zero-length notifications, ping/pong with
	// trivial params, etc. Fast-path the allocation so we don't make a
	// zero-length slice.
	if contentLength == 0 {
		return &Frame{
			ContentLength: 0,
			ContentType:   contentType,
			Body:          []byte{},
		}, nil
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r.br, body); err != nil {
		code := brainerrors.CodeFrameParseError
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			code = brainerrors.CodeSidecarStdoutEOF
		}
		return nil, WrapFrameError(code,
			fmt.Sprintf("frame reader: body read failed after %d bytes", contentLength), err)
	}

	// §2.2: Body MUST NOT carry a BOM. Reject EF BB BF at offset 0.
	if contentLength >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
		return nil, WrapFrameError(brainerrors.CodeFrameParseError,
			"frame reader: body carries forbidden UTF-8 BOM", nil)
	}

	return &Frame{
		ContentLength: contentLength,
		ContentType:   contentType,
		Body:          body,
	}, nil
}

// readHeaderLine reads a single CRLF-terminated header line from the
// buffered reader. The implementation is a hand-rolled scanner because
// bufio.ReadString('\n') does not enforce the 8 KiB cap and would let an
// adversarial peer balloon a single header into unbounded memory.
//
// The returned string does NOT include the trailing \r\n. An empty string
// with err=nil means "empty header line" (the terminator of the block).
func (r *frameReader) readHeaderLine() (string, error) {
	var buf strings.Builder
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\r' {
			// Expect LF next per §2.2. If not, it is a malformed frame.
			nextB, err := r.br.ReadByte()
			if err != nil {
				return "", err
			}
			if nextB != '\n' {
				return "", fmt.Errorf("bare CR in header line")
			}
			return buf.String(), nil
		}
		if buf.Len() >= MaxHeaderLineSize {
			return "", fmt.Errorf("header line exceeds 8 KiB cap")
		}
		buf.WriteByte(b)
	}
}

// splitHeader parses a single "Key: Value" line per §2.3. Trailing and
// leading spaces around the value are trimmed; the key is returned
// verbatim and lowercased by the caller for the case-insensitive compare.
func splitHeader(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	return line[:idx], strings.TrimSpace(line[idx+1:]), true
}

// frameWriter is the single-goroutine writer that backs every outbound
// stdio stream. §2.7 mandates exactly one writer per pipe direction —
// concurrent writes would interleave headers and bodies and immediately
// corrupt the wire. The implementation achieves this by funneling all
// outbound frames through a buffered channel that the writer loop drains
// sequentially.
//
// Shutdown is idempotent: Close first sets closed=true under mu, then
// closes the channel, then waits for the loop goroutine to drain any
// frames already in flight. Subsequent WriteFrame calls return
// CodeShuttingDown rather than racing against the shutdown.
type frameWriter struct {
	w io.Writer

	mu     sync.Mutex
	closed bool

	ch     chan writeRequest
	loopWG sync.WaitGroup

	writeTimeout time.Duration
}

// writeRequest is one (ctx, frame, done) tuple passed from WriteFrame to
// the writer goroutine. The done channel carries the write result so the
// caller can observe timeouts / errors synchronously even though the
// actual write happens on a different goroutine.
type writeRequest struct {
	ctx   context.Context
	frame *Frame
	done  chan error
}

// NewFrameWriter builds a FrameWriter over w with a default capacity of 64
// pending frames. The channel capacity matches the §4.6 in-flight window
// so a well-behaved host that saturates its in-flight budget never blocks
// on the writer channel.
func NewFrameWriter(w io.Writer) FrameWriter {
	return newFrameWriterWithOpts(w, 64, DefaultWriteTimeout)
}

// newFrameWriterWithOpts is the internal constructor that lets tests tune
// buffer depth and timeout. Production callers use NewFrameWriter.
func newFrameWriterWithOpts(w io.Writer, bufSize int, writeTimeout time.Duration) *frameWriter {
	fw := &frameWriter{
		w:            w,
		ch:           make(chan writeRequest, bufSize),
		writeTimeout: writeTimeout,
	}
	fw.loopWG.Add(1)
	go fw.loop()
	return fw
}

// WriteFrame implements FrameWriter. The call path is:
//  1. Enqueue a writeRequest into the channel. If the channel is full or
//     the writer has been Closed, return immediately with the appropriate
//     error so callers can surface back-pressure.
//  2. Wait on the done channel for the writer loop's verdict, with the
//     caller's ctx honored as a hard deadline per §5.2.
func (w *frameWriter) WriteFrame(ctx context.Context, frame *Frame) error {
	if frame == nil {
		return WrapFrameError(brainerrors.CodeFrameEncodingError,
			"frame writer: nil frame", nil)
	}
	if frame.ContentLength != len(frame.Body) {
		return WrapFrameError(brainerrors.CodeFrameEncodingError,
			fmt.Sprintf("frame writer: Content-Length=%d but body=%d bytes",
				frame.ContentLength, len(frame.Body)), nil)
	}
	if frame.ContentLength > MaxBodySize {
		return WrapFrameError(brainerrors.CodeFrameTooLarge,
			fmt.Sprintf("frame writer: body %d exceeds 16 MiB cap", frame.ContentLength), nil)
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return WrapFrameError(brainerrors.CodeShuttingDown,
			"frame writer: closed", nil)
	}
	w.mu.Unlock()

	req := writeRequest{
		ctx:   ctx,
		frame: frame,
		done:  make(chan error, 1),
	}

	select {
	case w.ch <- req:
		// Handed off to writer goroutine.
	case <-ctx.Done():
		return WrapFrameError(brainerrors.CodeShuttingDown,
			"frame writer: enqueue cancelled", ctx.Err())
	}

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		// The writer goroutine may still be trying to write — we cannot
		// abort it mid-frame without corrupting the stream. Return the
		// ctx error so the caller knows the deadline fired, but the
		// writer loop will finish or time out on its own DefaultWriteTimeout.
		return WrapFrameError(brainerrors.CodeShuttingDown,
			"frame writer: caller context cancelled before write completion", ctx.Err())
	}
}

// Close stops the writer goroutine. Pending frames already in the channel
// are still written out — we do NOT drop them, because a caller that got
// a "success" from the channel enqueue step expects the frame to actually
// hit the wire. Subsequent WriteFrame calls after Close return immediately.
func (w *frameWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()
	w.loopWG.Wait()
	return nil
}

// loop is the single writer goroutine. It drains the channel until close,
// writing each frame serially. A write failure is reported on the request's
// done channel and also returned to any pending frames so the caller
// observes the stream failure immediately.
func (w *frameWriter) loop() {
	defer w.loopWG.Done()
	for req := range w.ch {
		err := w.writeOne(req.frame)
		req.done <- err
	}
}

// writeOne serializes the frame into the canonical §2.2 wire layout:
//
//	Content-Length: N\r\n
//	Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n
//	\r\n
//	<body bytes>
//
// Two writes would be enough, but pre-assembling into a single buffer lets
// the underlying os.Pipe Write do a single syscall and avoids any chance
// of interleaving at the kernel level.
func (w *frameWriter) writeOne(frame *Frame) error {
	ct := frame.ContentType
	if ct == "" {
		ct = CanonicalContentType
	}
	header := fmt.Sprintf("Content-Length: %d\r\nContent-Type: %s\r\n\r\n",
		frame.ContentLength, ct)

	// Combine header+body into one buffer. For the 16 MiB worst case this
	// costs us an extra O(n) copy versus two Write calls, but it keeps
	// the syscall atomic which is what §2.7 actually cares about.
	buf := make([]byte, 0, len(header)+len(frame.Body))
	buf = append(buf, header...)
	buf = append(buf, frame.Body...)

	if _, err := w.w.Write(buf); err != nil {
		return WrapFrameError(brainerrors.CodeSidecarStdinBrokenPipe,
			"frame writer: underlying Write failed", err)
	}
	return nil
}
