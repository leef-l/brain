package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PrivateWSConfig configures an OKX private WebSocket connection.
type PrivateWSConfig struct {
	APIKey     string
	SecretKey  string
	Passphrase string
	Simulated  bool   // OKX demo mode
	WSEndpoint string // default: "wss://ws.okx.com:8443/ws/v5/private"
}

// OrderFillEvent is emitted when an order is filled.
type OrderFillEvent struct {
	OrderID   string  `json:"ordId"`
	ClientID  string  `json:"clOrdId"`
	InstID    string  `json:"instId"`
	Side      string  `json:"side"`     // "buy" / "sell"
	PosSide   string  `json:"posSide"`  // "long" / "short"
	FillPrice float64 `json:"fillPx,string"`
	FillQty   float64 `json:"fillSz,string"`
	Fee       float64 `json:"fee,string"`
	State     string  `json:"state"` // "filled", "partially_filled", "canceled"
	Timestamp int64   `json:"uTime,string"`
}

// PositionUpdateEvent is emitted when a position changes.
type PositionUpdateEvent struct {
	InstID    string  `json:"instId"`
	PosSide   string  `json:"posSide"` // "long" / "short" / "net"
	Quantity  float64 `json:"pos,string"`
	AvgPrice  float64 `json:"avgPx,string"`
	MarkPrice float64 `json:"markPx,string"`
	UPnL      float64 `json:"upl,string"`
	Leverage  string  `json:"lever"`
	Timestamp int64   `json:"uTime,string"`
}

// AccountUpdateEvent is emitted when account balance changes.
type AccountUpdateEvent struct {
	TotalEquity float64 `json:"totalEq,string"`
	Available   float64 `json:"details"` // simplified
	Timestamp   int64   `json:"uTime,string"`
}

// PrivateWSCallbacks holds the event handlers.
type PrivateWSCallbacks struct {
	OnOrderFill      func(accountID string, event OrderFillEvent)
	OnPositionUpdate func(accountID string, event PositionUpdateEvent)
	OnAccountUpdate  func(accountID string, event AccountUpdateEvent)
}

// PrivateWSConn manages a single OKX private WebSocket connection.
type PrivateWSConn struct {
	accountID string
	config    PrivateWSConfig
	callbacks PrivateWSCallbacks
	logger    *slog.Logger

	conn   *websocket.Conn
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewPrivateWSConn creates a private WS connection for one OKX account.
func NewPrivateWSConn(accountID string, cfg PrivateWSConfig, cb PrivateWSCallbacks, logger *slog.Logger) *PrivateWSConn {
	if cfg.WSEndpoint == "" {
		cfg.WSEndpoint = "wss://ws.okx.com:8443/ws/v5/private"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PrivateWSConn{
		accountID: accountID,
		config:    cfg,
		callbacks: cb,
		logger:    logger.With("component", "private_ws", "account", accountID),
		done:      make(chan struct{}),
	}
}

// Start connects and begins listening for events.
func (c *PrivateWSConn) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	if err := c.connect(ctx); err != nil {
		cancel()
		return err
	}

	go c.readLoop(ctx)
	go c.pingLoop(ctx)

	c.logger.Info("private ws started")
	return nil
}

// Stop closes the connection.
func (c *PrivateWSConn) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()
}

// connect dials the WS, authenticates, and subscribes.
func (c *PrivateWSConn) connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
	header := make(map[string][]string)
	if c.config.Simulated {
		header["x-simulated-trading"] = []string{"1"}
	}

	conn, _, err := dialer.DialContext(ctx, c.config.WSEndpoint, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Authenticate
	if err := c.authenticate(); err != nil {
		conn.Close()
		return fmt.Errorf("auth: %w", err)
	}

	// Subscribe to channels
	if err := c.subscribe(); err != nil {
		conn.Close()
		return fmt.Errorf("subscribe: %w", err)
	}

	return nil
}

// authenticate sends the login message.
func (c *PrivateWSConn) authenticate() error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	preSign := ts + "GET" + "/users/self/verify"
	mac := hmac.New(sha256.New, []byte(c.config.SecretKey))
	mac.Write([]byte(preSign))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	loginMsg := map[string]any{
		"op": "login",
		"args": []map[string]string{
			{
				"apiKey":     c.config.APIKey,
				"passphrase": c.config.Passphrase,
				"timestamp":  ts,
				"sign":       sign,
			},
		},
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteJSON(loginMsg); err != nil {
		return err
	}

	// Read login response
	_, msg, err := c.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}

	var resp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}
	if resp.Event != "login" || resp.Code != "0" {
		return fmt.Errorf("login failed: code=%s msg=%s", resp.Code, resp.Msg)
	}

	c.logger.Info("authenticated")
	return nil
}

// subscribe subscribes to orders, positions, and account channels.
func (c *PrivateWSConn) subscribe() error {
	subMsg := map[string]any{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "orders", "instType": "SWAP"},
			{"channel": "positions", "instType": "SWAP"},
			{"channel": "account"},
		},
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(subMsg)
}

// readLoop reads messages and dispatches to callbacks.
func (c *PrivateWSConn) readLoop(ctx context.Context) {
	defer func() {
		select {
		case <-c.done:
			// already closed
		default:
			close(c.done)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.mu.Lock()
		conn := c.conn
		if conn == nil {
			c.mu.Unlock()
			return
		}
		// Set a read deadline so ReadMessage doesn't block forever
		// while we hold no lock — the deadline prevents missing a
		// concurrent Stop() that closes the connection.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		c.mu.Unlock()

		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return // expected shutdown
			}
			// Check if connection was closed by Stop()
			c.mu.Lock()
			stopped := c.conn == nil
			c.mu.Unlock()
			if stopped {
				return
			}
			c.logger.Warn("read error, attempting reconnect", "err", err)
			c.reconnect(ctx)
			continue
		}

		c.handleMessage(msg)
	}
}

// safeCallback runs a callback with panic recovery.
func (c *PrivateWSConn) safeCallback(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("callback panic recovered",
				"callback", name,
				"panic", fmt.Sprintf("%v", r))
		}
	}()
	fn()
}

// handleMessage parses and dispatches a WS message.
func (c *PrivateWSConn) handleMessage(msg []byte) {
	var envelope struct {
		Arg  map[string]string `json:"arg"`
		Data json.RawMessage   `json:"data"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return // pong or other non-data message
	}

	channel := envelope.Arg["channel"]
	if len(envelope.Data) == 0 {
		return
	}

	switch channel {
	case "orders":
		var events []OrderFillEvent
		if err := json.Unmarshal(envelope.Data, &events); err != nil {
			c.logger.Error("parse order event", "err", err)
			return
		}
		if c.callbacks.OnOrderFill != nil {
			for _, e := range events {
				if e.State == "filled" || e.State == "partially_filled" {
					evt := e
					c.safeCallback("OnOrderFill", func() { c.callbacks.OnOrderFill(c.accountID, evt) })
				}
			}
		}

	case "positions":
		var events []PositionUpdateEvent
		if err := json.Unmarshal(envelope.Data, &events); err != nil {
			c.logger.Error("parse position event", "err", err)
			return
		}
		if c.callbacks.OnPositionUpdate != nil {
			for _, e := range events {
				evt := e
				c.safeCallback("OnPositionUpdate", func() { c.callbacks.OnPositionUpdate(c.accountID, evt) })
			}
		}

	case "account":
		var events []AccountUpdateEvent
		if err := json.Unmarshal(envelope.Data, &events); err != nil {
			c.logger.Error("parse account event", "err", err)
			return
		}
		if c.callbacks.OnAccountUpdate != nil {
			for _, e := range events {
				evt := e
				c.safeCallback("OnAccountUpdate", func() { c.callbacks.OnAccountUpdate(c.accountID, evt) })
			}
		}
	}
}

// reconnect attempts to re-establish the connection with backoff.
func (c *PrivateWSConn) reconnect(ctx context.Context) {
	backoffs := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
	}

	for i, d := range backoffs {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}

		c.logger.Info("reconnecting", "attempt", i+1)
		if err := c.connect(ctx); err != nil {
			c.logger.Warn("reconnect failed", "attempt", i+1, "err", err)
			continue
		}
		c.logger.Info("reconnected")
		return
	}

	c.logger.Error("reconnect exhausted all attempts")
}

// pingLoop sends periodic pings to keep the connection alive.
func (c *PrivateWSConn) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				_ = c.conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			}
			c.mu.Unlock()
		}
	}
}

// PrivateWSManager manages private WS connections for multiple accounts.
type PrivateWSManager struct {
	conns  map[string]*PrivateWSConn
	mu     sync.RWMutex
	logger *slog.Logger
}

// NewPrivateWSManager creates a manager.
func NewPrivateWSManager(logger *slog.Logger) *PrivateWSManager {
	return &PrivateWSManager{
		conns:  make(map[string]*PrivateWSConn),
		logger: logger,
	}
}

// Add registers a private WS connection for an account.
func (m *PrivateWSManager) Add(conn *PrivateWSConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[conn.accountID] = conn
}

// StartAll connects all registered accounts.
func (m *PrivateWSManager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, conn := range m.conns {
		if err := conn.Start(ctx); err != nil {
			m.logger.Error("private ws start failed", "account", id, "err", err)
			// Continue with other accounts
		}
	}
	return nil
}

// StopAll disconnects all accounts.
func (m *PrivateWSManager) StopAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, conn := range m.conns {
		conn.Stop()
	}
}

// Conn returns the connection for an account (or nil).
func (m *PrivateWSManager) Conn(accountID string) *PrivateWSConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[accountID]
}
