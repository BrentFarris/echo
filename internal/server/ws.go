package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxMessageBytes = 1 << 20

var upgrader = websocket.Upgrader{
	ReadBufferSize: 1024, WriteBufferSize: 1024,
	CheckOrigin: func(*http.Request) bool { return true },
}

type client struct {
	conn      *websocket.Conn
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
	server    *Server
}

func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *client) sendJSON(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		logf("client send marshal: %v", err)
		return
	}
	select {
	case <-c.done:
		return
	case c.send <- data:
	default:
		// A sequence gap makes the browser request a fresh snapshot. Avoid
		// blocking a model stream on a slow or suspended browser tab.
	}
}

type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client
	shutdown   chan struct{}
	done       chan struct{}
}

func NewHub() *Hub {
	h := &Hub{
		clients: make(map[*client]struct{}), register: make(chan *client),
		unregister: make(chan *client), shutdown: make(chan struct{}), done: make(chan struct{}),
	}
	go h.run()
	return h
}

func (h *Hub) run() {
	defer close(h.done)
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			count := len(h.clients)
			h.mu.Unlock()
			c.sendJSON(map[string]any{"type": "welcome", "clients": count, "time": time.Now().UTC().Format(time.RFC3339)})
		case c := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			c.close()
		case <-h.shutdown:
			h.mu.Lock()
			for c := range h.clients {
				delete(h.clients, c)
				c.close()
			}
			h.mu.Unlock()
			return
		}
	}
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) Shutdown() {
	select {
	case <-h.shutdown:
		<-h.done
		return
	default:
		close(h.shutdown)
	}
	<-h.done
}

func (h *Hub) Broadcast(event any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.sendJSON(event)
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("websocket upgrade: %v", err)
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 1024), done: make(chan struct{}), server: s}
	go c.writePump()
	s.hub.register <- c
	c.readPump(s.hub)
}

type inboundMessage struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	Message     string `json:"message,omitempty"`
	Model       string `json:"model,omitempty"`
}

func (c *client) readPump(h *Hub) {
	defer func() {
		c.server.sessions.unsubscribe(c)
		select {
		case h.unregister <- c:
		case <-h.shutdown:
			c.close()
		}
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageBytes)
	c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg inboundMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendJSON(map[string]any{"type": "command_error", "code": "invalid_json", "error": err.Error()})
			continue
		}
		switch msg.Type {
		case "session_subscribe":
			c.server.sessions.subscribe(c, msg.WorkspaceID)
		case "chat_send":
			c.server.sessions.send(c, msg)
		case "chat_stop":
			c.server.sessions.stop(c, msg.WorkspaceID)
		default:
			c.sendJSON(map[string]any{"type": "command_error", "workspaceId": msg.WorkspaceID, "code": "unknown_command", "error": "unsupported message type"})
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case <-c.done:
			_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		case message := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
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
