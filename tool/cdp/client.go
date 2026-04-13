package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Client is a CDP (Chrome DevTools Protocol) client that communicates
// with a browser over WebSocket using JSON-RPC.
//
// It supports:
//   - Sending commands and receiving responses (Call)
//   - Subscribing to events (Subscribe/On)
//   - Session targeting (for per-tab sessions)
//
// This client works with any browser implementing CDP:
//   - Google Chrome / Chromium
//   - Microsoft Edge (Chromium-based)
//   - Brave
//   - Opera
//   - Vivaldi
//   - Any Chromium-based browser
//
// Firefox has partial CDP support behind a flag but is not fully compatible.
type Client struct {
	ws        *wsConn
	nextID    atomic.Int64
	pending   map[int64]chan *rpcResponse
	listeners map[string][]func(json.RawMessage)
	sessionMu sync.Mutex
	mu        sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
	lastErr   error
}

// rpcRequest is a CDP JSON-RPC request.
type rpcRequest struct {
	ID        int64           `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// rpcResponse is a CDP JSON-RPC response.
type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcEvent is a CDP event notification.
type rpcEvent struct {
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// rpcError is a CDP error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

// Dial connects to a browser's CDP WebSocket endpoint.
// The url is typically ws://127.0.0.1:PORT/devtools/browser/UUID.
func Dial(url string) (*Client, error) {
	ws, err := wsDial(url)
	if err != nil {
		return nil, err
	}

	c := &Client{
		ws:        ws,
		pending:   make(map[int64]chan *rpcResponse),
		listeners: make(map[string][]func(json.RawMessage)),
		done:      make(chan struct{}),
	}

	go c.readLoop()
	return c, nil
}

// Call sends a CDP command and waits for the response.
// params can be nil for commands with no parameters.
// result will be unmarshaled from the response — pass nil to discard.
func (c *Client) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	return c.CallSession(ctx, "", method, params, result)
}

// CallSession sends a CDP command on a specific session (tab).
func (c *Client) CallSession(ctx context.Context, sessionID string, method string, params interface{}, result interface{}) error {
	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		var err error
		rawParams, err = json.Marshal(params)
		if err != nil {
			return fmt.Errorf("cdp: marshal params: %w", err)
		}
	}

	req := rpcRequest{
		ID:        id,
		Method:    method,
		Params:    rawParams,
		SessionID: sessionID,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cdp: marshal request: %w", err)
	}

	// Register pending response channel.
	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	// Send.
	if err := c.ws.WriteText(data); err != nil {
		return fmt.Errorf("cdp: send: %w", err)
	}

	// Wait for response.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.lastErr != nil {
			return fmt.Errorf("cdp: connection lost: %w", c.lastErr)
		}
		return fmt.Errorf("cdp: connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("cdp: unmarshal result: %w", err)
			}
		}
		return nil
	}
}

// On registers an event listener for a CDP event method.
// The callback receives the event params as raw JSON.
// Multiple listeners can be registered for the same event.
func (c *Client) On(method string, fn func(json.RawMessage)) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	c.listeners[method] = append(c.listeners[method], fn)
}

// Close closes the CDP connection.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
	})
	return c.ws.Close()
}

// readLoop reads WebSocket messages and dispatches them to pending calls
// or event listeners.
func (c *Client) readLoop() {
	defer c.closeOnce.Do(func() { close(c.done) })

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.lastErr = err
			return
		}

		// Peek to determine if this is a response (has "id") or event (has "method" without "id").
		var peek struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(data, &peek) != nil {
			continue
		}

		if peek.ID != nil {
			// Response to a pending call.
			var resp rpcResponse
			if json.Unmarshal(data, &resp) != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[resp.ID]
			c.mu.Unlock()
			if ok {
				ch <- &resp
			}
		} else if peek.Method != "" {
			// Event notification.
			var ev rpcEvent
			if json.Unmarshal(data, &ev) != nil {
				continue
			}
			c.sessionMu.Lock()
			listeners := c.listeners[ev.Method]
			c.sessionMu.Unlock()
			for _, fn := range listeners {
				fn(ev.Params)
			}
		}
	}
}
