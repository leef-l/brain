package webui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // allow all origins
}

// wsMessage is the envelope for all WebSocket messages.
type wsMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
	TS   int64       `json:"ts"`
}

// client represents a single WebSocket connection.
type client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket clients and broadcasts.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]bool
	count   atomic.Int32
	logger  *slog.Logger
}

func newHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[*client]bool),
		logger:  logger,
	}
}

func (h *Hub) clientCount() int {
	return int(h.count.Load())
}

func (h *Hub) addClient(c *client) {
	h.mu.Lock()
	h.clients[c] = true
	h.count.Add(1)
	h.mu.Unlock()
	h.logger.Info("webui ws client connected", "total", h.clientCount())
}

func (h *Hub) removeClient(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
		h.count.Add(-1)
	}
	h.mu.Unlock()
	h.logger.Info("webui ws client disconnected", "total", h.clientCount())
}

// broadcast sends a message to all connected clients.
func (h *Hub) broadcast(msg wsMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// client too slow, skip
		}
	}
}

// run is the hub's main loop — handles cleanup on context cancel.
func (h *Hub) run(ctx context.Context) {
	<-ctx.Done()
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.conn.Close()
		delete(h.clients, c)
	}
}

// handleWS upgrades HTTP to WebSocket.
func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Warn("ws upgrade failed", "err", err)
		return
	}

	// Max 5 connections
	if h.clientCount() >= 5 {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "max connections"))
		conn.Close()
		return
	}

	c := &client{
		conn: conn,
		send: make(chan []byte, 64),
	}
	h.addClient(c)

	// Writer goroutine
	go func() {
		defer func() {
			h.removeClient(c)
			conn.Close()
		}()
		for msg := range c.send {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Reader goroutine (just read and discard, keeps connection alive)
	go func() {
		defer func() {
			h.removeClient(c)
			conn.Close()
		}()
		conn.SetReadLimit(512)
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Ping goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}()
}
