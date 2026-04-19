package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/protocol"
)

// wsFrame 是 WebSocket 消息中的 JSON 帧包装。
// 将 Content-Length framed 协议映射到 WebSocket text message。
type wsFrame struct {
	ContentLength int             `json:"content_length"`
	ContentType   string          `json:"content_type,omitempty"`
	Body          json.RawMessage `json:"body"`
}

// WSFrameReader 用 WebSocket 实现 protocol.FrameReader。
type WSFrameReader struct {
	conn    *websocket.Conn
	timeout time.Duration
	closed  atomic.Bool
}

// NewWSFrameReader 创建 WebSocket 帧读取器。
func NewWSFrameReader(conn *websocket.Conn, timeout time.Duration) *WSFrameReader {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &WSFrameReader{conn: conn, timeout: timeout}
}

func (r *WSFrameReader) ReadFrame(ctx context.Context) (*protocol.Frame, error) {
	if r.closed.Load() {
		return nil, brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("ws reader closed"))
	}

	// 设置读取截止时间
	deadline := time.Now().Add(r.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	r.conn.SetReadDeadline(deadline)

	// 读取 WebSocket 消息
	msgType, data, err := r.conn.ReadMessage()
	if err != nil {
		if r.closed.Load() {
			return nil, brainerrors.New(brainerrors.CodeShuttingDown,
				brainerrors.WithMessage("ws reader closed"))
		}
		return nil, brainerrors.New(brainerrors.CodeSidecarStdoutEOF,
			brainerrors.WithMessage(fmt.Sprintf("ws read: %v", err)))
	}

	if msgType != websocket.TextMessage {
		return nil, brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
			brainerrors.WithMessage(fmt.Sprintf("ws: expected text message, got type %d", msgType)))
	}

	// 解析 JSON 帧包装
	var wf wsFrame
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, brainerrors.New(brainerrors.CodeSidecarProtocolViolation,
			brainerrors.WithMessage(fmt.Sprintf("ws: unmarshal frame: %v", err)))
	}

	if len(wf.Body) > protocol.MaxBodySize {
		return nil, brainerrors.New(brainerrors.CodeFrameTooLarge,
			brainerrors.WithMessage(fmt.Sprintf("ws: body %d bytes exceeds %d limit", len(wf.Body), protocol.MaxBodySize)))
	}

	ct := wf.ContentType
	if ct == "" {
		ct = protocol.CanonicalContentType
	}

	return &protocol.Frame{
		ContentLength: len(wf.Body),
		ContentType:   ct,
		Body:          wf.Body,
	}, nil
}

// WSFrameWriter 用 WebSocket 实现 protocol.FrameWriter。
type WSFrameWriter struct {
	conn    *websocket.Conn
	timeout time.Duration
	mu      sync.Mutex
	closed  atomic.Bool
	ch      chan *writeReq
	done    chan struct{}
}

type writeReq struct {
	frame *protocol.Frame
	errCh chan error
}

// NewWSFrameWriter 创建 WebSocket 帧写入器。
// 内部用单 goroutine 序列化所有写操作，防止 WebSocket 并发写入。
func NewWSFrameWriter(conn *websocket.Conn, timeout time.Duration) *WSFrameWriter {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	w := &WSFrameWriter{
		conn:    conn,
		timeout: timeout,
		ch:      make(chan *writeReq, 64),
		done:    make(chan struct{}),
	}
	go w.writeLoop()
	return w
}

func (w *WSFrameWriter) writeLoop() {
	defer close(w.done)
	for req := range w.ch {
		req.errCh <- w.doWrite(req.frame)
	}
}

func (w *WSFrameWriter) doWrite(frame *protocol.Frame) error {
	if len(frame.Body) > protocol.MaxBodySize {
		return brainerrors.New(brainerrors.CodeFrameTooLarge,
			brainerrors.WithMessage("ws write: body too large"))
	}

	wf := wsFrame{
		ContentLength: frame.ContentLength,
		ContentType:   frame.ContentType,
		Body:          frame.Body,
	}
	data, err := json.Marshal(wf)
	if err != nil {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf("ws write: marshal: %v", err)))
	}

	w.conn.SetWriteDeadline(time.Now().Add(w.timeout))
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

func (w *WSFrameWriter) WriteFrame(ctx context.Context, frame *protocol.Frame) error {
	if w.closed.Load() {
		return brainerrors.New(brainerrors.CodeShuttingDown,
			brainerrors.WithMessage("ws writer closed"))
	}

	errCh := make(chan error, 1)
	req := &writeReq{frame: frame, errCh: errCh}

	select {
	case w.ch <- req:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *WSFrameWriter) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(w.ch)
	<-w.done
	return nil
}

// NewWSBidirRPC 用 WebSocket 连接创建完整的 BidirRPC 会话。
// 返回的 BidirRPC 可直接传给 Orchestrator 使用。
func NewWSBidirRPC(conn *websocket.Conn, role protocol.Role) protocol.BidirRPC {
	reader := NewWSFrameReader(conn, 60*time.Second)
	writer := NewWSFrameWriter(conn, 10*time.Second)
	return protocol.NewBidirRPC(role, reader, writer)
}
