package server

import (
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
	"net/http"
	"net/url"
	"time"
)

func (s *Server) debugEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("botId")
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		if swarm.Owner(r.Context()) {
			return true
		}
		u, err := url.Parse(r.Header.Get("Origin"))
		return err == nil && u.Host == r.Host
	}}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	key := sessionKey(r)
	if caller, ok := swarm.Caller(r.Context()); ok {
		key = "swarm:" + caller.NodeID
	}
	s.authMu.Lock()
	if s.sessionSockets == nil {
		s.sessionSockets = map[string]map[*websocket.Conn]bool{}
	}
	if s.sessionSockets[key] == nil {
		s.sessionSockets[key] = map[*websocket.Conn]bool{}
	}
	s.sessionSockets[key][conn] = true
	s.authMu.Unlock()
	defer func() { s.authMu.Lock(); delete(s.sessionSockets[key], conn); s.authMu.Unlock() }()
	ch := make(chan debugger.Event, 64)
	s.debugMu.Lock()
	if s.debugClients[id] == nil {
		s.debugClients[id] = map[chan debugger.Event]bool{}
	}
	s.debugClients[id][ch] = true
	s.debugMu.Unlock()
	defer func() { s.debugMu.Lock(); delete(s.debugClients[id], ch); s.debugMu.Unlock() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(1024)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if conn.WriteJSON(map[string]string{"type": "debug_subscribed", "botId": id}) != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.editorBot(id); err != nil || !s.authenticated(r) {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)) != nil {
				return
			}
		case event := <-ch:
			if _, err := s.editorBot(id); err != nil || !s.authenticated(r) {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteJSON(event) != nil {
				return
			}
		}
	}
}
