package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/workspacefs"
	"github.com/gorilla/websocket"
)

const maxMessageBytes = 32 << 20

var upgrader = websocket.Upgrader{
	ReadBufferSize: 1024, WriteBufferSize: 1024,
	CheckOrigin: requestOriginAllowed,
}

type client struct {
	conn                  *websocket.Conn
	send                  chan []byte
	done                  chan struct{}
	closeOnce             sync.Once
	server                *Server
	fsMu                  sync.RWMutex
	fsSubscriptions       map[string]bool
	gitMu                 sync.RWMutex
	gitSubscriptions      map[string]bool
	terminalMu            sync.RWMutex
	terminalSubscriptions map[string]bool
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

func (h *Hub) BroadcastWorkspaceFS(workspaceID string, event any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.fsMu.RLock()
		subscribed := c.fsSubscriptions[workspaceID]
		c.fsMu.RUnlock()
		if subscribed {
			c.sendJSON(event)
		}
	}
}

func (h *Hub) BroadcastWorkspaceGit(workspaceID string, event any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.gitMu.RLock()
		subscribed := c.gitSubscriptions[workspaceID]
		c.gitMu.RUnlock()
		if subscribed {
			c.sendJSON(event)
		}
	}
}

func (h *Hub) BroadcastWorkspaceTerminal(workspaceID string, event any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.terminalMu.RLock()
		subscribed := c.terminalSubscriptions[workspaceID]
		c.terminalMu.RUnlock()
		if subscribed {
			c.sendJSON(event)
		}
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("websocket upgrade: %v", err)
		return
	}
	c := &client{
		conn: conn, send: make(chan []byte, 1024), done: make(chan struct{}), server: s,
		fsSubscriptions: make(map[string]bool), gitSubscriptions: make(map[string]bool),
		terminalSubscriptions: make(map[string]bool),
	}
	go c.writePump()
	s.hub.register <- c
	c.readPump(s.hub)
}

type inboundMessage struct {
	Type          string                `json:"type"`
	WorkspaceID   string                `json:"workspaceId,omitempty"`
	Surface       string                `json:"surface,omitempty"`
	ChatID        string                `json:"chatId,omitempty"`
	RequestID     string                `json:"requestId,omitempty"`
	Message       string                `json:"message,omitempty"`
	Model         string                `json:"model,omitempty"`
	AgentModeID   string                `json:"agentModeId,omitempty"`
	StopIfBusy    bool                  `json:"stopIfBusy,omitempty"`
	Refs          []workspacefs.FileRef `json:"refs,omitempty"`
	EditorContext *editorContext        `json:"editorContext,omitempty"`
	Images        []chatMediaInput      `json:"images,omitempty"`
	Videos        []chatMediaInput      `json:"videos,omitempty"`
}

func (c *client) readPump(h *Hub) {
	defer func() {
		c.server.sessions.unsubscribe(c)
		c.unsubscribeAllFS()
		c.unsubscribeAllGit()
		c.unsubscribeAllTerminals()
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
			c.server.sessions.subscribe(c, msg.WorkspaceID, msg.Surface)
		case "fs_subscribe":
			c.subscribeFS(msg.WorkspaceID, msg.Refs)
		case "fs_unsubscribe":
			c.unsubscribeFS(msg.WorkspaceID)
		case "git_subscribe":
			c.subscribeGit(msg.WorkspaceID)
		case "git_unsubscribe":
			c.unsubscribeGit(msg.WorkspaceID)
		case "terminal_subscribe":
			c.subscribeTerminal(msg.WorkspaceID)
		case "terminal_unsubscribe":
			c.unsubscribeTerminal(msg.WorkspaceID)
		case "chat_send":
			c.server.sessions.send(c, msg)
		case "chat_stop":
			c.server.sessions.stop(c, msg.WorkspaceID, msg.ChatID, msg.Surface)
		case "chat_clear":
			c.server.sessions.clear(c, msg.WorkspaceID, msg.ChatID, msg.Surface)
		case "chat_tab_create":
			c.server.sessions.createTab(c, msg.WorkspaceID)
		case "chat_tab_activate":
			c.server.sessions.activateTab(c, msg.WorkspaceID, msg.ChatID)
		case "chat_tab_close":
			c.server.sessions.closeTab(c, msg.WorkspaceID, msg.ChatID, msg.StopIfBusy)
		default:
			c.sendJSON(map[string]any{"type": "command_error", "workspaceId": msg.WorkspaceID, "code": "unknown_command", "error": "unsupported message type"})
		}
	}
}

func (c *client) subscribeTerminal(workspaceID string) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		c.sendJSON(map[string]any{"type": "command_error", "code": "missing_workspace", "error": "workspaceId is required"})
		return
	}
	if _, ok, err := c.server.workspaces.Get(workspaceID); err != nil || !ok {
		c.sendJSON(map[string]any{"type": "command_error", "workspaceId": workspaceID, "code": "terminal_subscribe_failed", "error": "workspace was not found"})
		return
	}
	c.terminalMu.Lock()
	c.terminalSubscriptions[workspaceID] = true
	c.terminalMu.Unlock()
	c.sendJSON(map[string]any{"type": "terminal_subscribed", "workspaceId": workspaceID})
}

func (c *client) unsubscribeTerminal(workspaceID string) {
	c.terminalMu.Lock()
	delete(c.terminalSubscriptions, workspaceID)
	c.terminalMu.Unlock()
}

func (c *client) unsubscribeAllTerminals() {
	c.terminalMu.Lock()
	c.terminalSubscriptions = make(map[string]bool)
	c.terminalMu.Unlock()
}

func (c *client) subscribeFS(workspaceID string, refs []workspacefs.FileRef) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		c.sendJSON(map[string]any{"type": "command_error", "code": "missing_workspace", "error": "workspaceId is required"})
		return
	}
	c.fsMu.RLock()
	already := c.fsSubscriptions[workspaceID]
	c.fsMu.RUnlock()
	if already {
		c.server.watcher.AddReferences(workspaceID, refs)
		return
	}
	// Mark the client subscribed before constructing the watcher. Watcher setup
	// can synchronously emit fs_resync_required (for example on a network
	// filesystem), and that fallback event must reach the first subscriber.
	c.fsMu.Lock()
	c.fsSubscriptions[workspaceID] = true
	c.fsMu.Unlock()
	if err := c.server.watcher.Subscribe(workspaceID); err != nil {
		c.fsMu.Lock()
		delete(c.fsSubscriptions, workspaceID)
		c.fsMu.Unlock()
		c.sendJSON(map[string]any{"type": "command_error", "workspaceId": workspaceID, "code": "watch_failed", "error": "failed to watch workspace"})
		c.sendJSON(map[string]any{"type": "fs_resync_required", "workspaceId": workspaceID, "sequence": 0, "resyncRequired": true})
		return
	}
	c.server.watcher.AddReferences(workspaceID, refs)
	c.sendJSON(map[string]any{"type": "fs_subscribed", "workspaceId": workspaceID})
}

func (c *client) unsubscribeFS(workspaceID string) {
	c.fsMu.Lock()
	if !c.fsSubscriptions[workspaceID] {
		c.fsMu.Unlock()
		return
	}
	delete(c.fsSubscriptions, workspaceID)
	c.fsMu.Unlock()
	c.server.watcher.Unsubscribe(workspaceID)
}

func (c *client) unsubscribeAllFS() {
	c.fsMu.Lock()
	workspaceIDs := make([]string, 0, len(c.fsSubscriptions))
	for workspaceID := range c.fsSubscriptions {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	c.fsSubscriptions = make(map[string]bool)
	c.fsMu.Unlock()
	for _, workspaceID := range workspaceIDs {
		c.server.watcher.Unsubscribe(workspaceID)
	}
}

func (c *client) subscribeGit(workspaceID string) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		c.sendJSON(map[string]any{"type": "command_error", "code": "missing_workspace", "error": "workspaceId is required"})
		return
	}
	c.gitMu.RLock()
	already := c.gitSubscriptions[workspaceID]
	c.gitMu.RUnlock()
	if already {
		return
	}
	// Mark the subscription first so an initial status emitted during watcher
	// setup cannot race past the first client.
	c.gitMu.Lock()
	c.gitSubscriptions[workspaceID] = true
	c.gitMu.Unlock()
	if err := c.server.git.Subscribe(context.Background(), workspaceID); err != nil {
		c.gitMu.Lock()
		delete(c.gitSubscriptions, workspaceID)
		c.gitMu.Unlock()
		c.sendJSON(map[string]any{"type": "command_error", "workspaceId": workspaceID, "code": "git_watch_failed", "error": err.Error()})
		c.sendJSON(map[string]any{"type": "git_resync_required", "workspaceId": workspaceID})
		return
	}
	c.sendJSON(map[string]any{"type": "git_subscribed", "workspaceId": workspaceID})
}

func (c *client) unsubscribeGit(workspaceID string) {
	c.gitMu.Lock()
	if !c.gitSubscriptions[workspaceID] {
		c.gitMu.Unlock()
		return
	}
	delete(c.gitSubscriptions, workspaceID)
	c.gitMu.Unlock()
	c.server.git.Unsubscribe(workspaceID)
}

func (c *client) unsubscribeAllGit() {
	c.gitMu.Lock()
	workspaceIDs := make([]string, 0, len(c.gitSubscriptions))
	for workspaceID := range c.gitSubscriptions {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	c.gitSubscriptions = make(map[string]bool)
	c.gitMu.Unlock()
	for _, workspaceID := range workspaceIDs {
		c.server.git.Unsubscribe(workspaceID)
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
