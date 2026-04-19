package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leef-l/brain/sdk/events"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// WSMessage is the envelope for all WebSocket messages.
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// WSHub manages WebSocket connections and broadcasts events.
type WSHub struct {
	bus   *events.MemEventBus
	conns map[*websocket.Conn]context.CancelFunc
	mu    sync.Mutex
}

// NewWSHub creates a new WebSocket hub connected to the event bus.
func NewWSHub(bus *events.MemEventBus) *WSHub {
	return &WSHub{
		bus:   bus,
		conns: make(map[*websocket.Conn]context.CancelFunc),
	}
}

// Start subscribes to the event bus and broadcasts to all connected clients.
func (h *WSHub) Start(ctx context.Context) {
	if h.bus == nil {
		return
	}
	ch, cancel := h.bus.Subscribe(ctx, "")
	go func() {
		defer cancel()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				msg := WSMessage{Type: "event", Data: data}
				h.broadcast(msg)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (h *WSHub) broadcast(msg WSMessage) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for conn, cancel := range h.conns {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			conn.Close()
			cancel()
			delete(h.conns, conn)
		}
	}
}

func (h *WSHub) register(conn *websocket.Conn, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[conn] = cancel
}

func (h *WSHub) unregister(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cancel, ok := h.conns[conn]; ok {
		cancel()
		delete(h.conns, conn)
	}
}

// HandleWS upgrades an HTTP connection to WebSocket and keeps it alive.
func (h *WSHub) HandleWS(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		http.Error(w, `{"error":"event bus not available"}`, http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("dashboard/ws: upgrade error: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	h.register(conn, cancel)

	// Send initial connected message
	welcome := WSMessage{Type: "connected", Data: json.RawMessage(`{"status":"ok"}`)}
	if data, err := json.Marshal(welcome); err == nil {
		conn.WriteMessage(websocket.TextMessage, data)
	}

	// Read pump: handle pings and client messages, detect disconnect
	go func() {
		defer func() {
			h.unregister(conn)
			conn.Close()
		}()
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

	// Ping pump: keep connection alive
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ConnectionCount returns the number of active WebSocket connections.
func (h *WSHub) ConnectionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}
