package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// maxMessageBytes caps the size of a single WebSocket message.
const maxMessageBytes = 1 << 20 // 1 MiB

// upgrader upgrades HTTP connections to WebSockets. CheckOrigin is permissive
// because the server is bound to localhost and is the origin for the SPA.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// client represents a single connected WebSocket peer.
type client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub manages all connected WebSocket clients and broadcasts events to them.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client
	shutdown   chan struct{}
	done       chan struct{}
}

// NewHub creates an empty Hub and starts its event loop.
func NewHub() *Hub {
	h := &Hub{
		clients:    make(map[*client]struct{}),
		register:   make(chan *client),
		unregister: make(chan *client),
		shutdown:   make(chan struct{}),
		done:       make(chan struct{}),
	}
	go h.run()
	return h
}

// run is the Hub's event loop. It processes registrations, unregistrations,
// and broadcasts until Shutdown is called.
func (h *Hub) run() {
	defer close(h.done)
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			// Queue the welcome event directly to the newly registered client.
			// Doing this here (after the map insert) avoids a race where the
			// handler broadcasts before registration completes.
			c.send <- welcomeEvent(h.ClientCount())
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case <-h.shutdown:
			h.mu.Lock()
			for c := range h.clients {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
			return
		}
	}
}

// welcomeEvent builds the JSON welcome message sent to a client on connect.
func welcomeEvent(clientCount int) []byte {
	data, err := json.Marshal(map[string]any{
		"type":    "welcome",
		"clients": clientCount,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		logf("welcome marshal: %v", err)
		return []byte(`{"type":"welcome"}`)
	}
	return data
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Shutdown stops the Hub's event loop and closes all client connections.
func (h *Hub) Shutdown() {
	select {
	case <-h.shutdown:
		return
	default:
		close(h.shutdown)
	}
	<-h.done
}

// Broadcast marshals event to JSON and sends it to every connected client.
// It is safe to call from any goroutine.
func (h *Hub) Broadcast(event any) {
	data, err := json.Marshal(event)
	if err != nil {
		logf("broadcast marshal: %v", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// Client's send buffer is full; drop it. The write pump will
			// eventually close the connection if it cannot keep up.
		}
	}
}

// handleWebSocket upgrades the request to a WebSocket and registers the client
// with the Hub. It blocks for the lifetime of the connection.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("websocket upgrade: %v", err)
		return
	}
	c := &client{
		conn: conn,
		send: make(chan []byte, 256),
	}
	go c.writePump()
	s.hub.register <- c
	c.readPump(s.hub)
}

// readPump reads messages from the client until the connection closes, then
// unregisters it. The application currently does not act on inbound messages;
// this loop keeps the connection alive and detects disconnects.
func (c *client) readPump(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageBytes)
	c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

// writePump writes queued messages to the client. It sends periodic pings to
// keep the connection alive and detect dead peers.
func (c *client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
