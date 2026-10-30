package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
	"github.com/brent/echo/internal/terminal"
	"github.com/gorilla/websocket"
)

func (s *Server) terminalBot(id string) (domain.Bot, error) {
	b, err := s.Bot(id)
	if err == nil && !s.Plugins.Enabled(b, "terminal") {
		err = errors.New("Terminal is disabled")
	}
	return b, err
}

func (s *Server) initTerminal() {
	s.terminalClients = map[string]map[chan terminal.Event]bool{}
	s.Terminal = terminal.New(s.terminalBot, s.Store)
	s.Terminal.SetNotifier(func(event terminal.Event) {
		s.terminalMu.Lock()
		defer s.terminalMu.Unlock()
		for ch := range s.terminalClients[event.BotID] {
			select {
			case ch <- event:
			default:
				// An explicit marker recovers even if no more output arrives.
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- terminal.Event{BotID: event.BotID, Type: "terminal_resync"}:
				default:
				}
			}
		}
	})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.Terminal.Reconcile()
			}
		}
	}()
}

func (s *Server) terminalRoutes(mux *http.ServeMux) {
	base := "/api/v1/plugins/terminal/bots/{botId}"
	for _, route := range []string{
		"GET sessions", "POST sessions", "GET sessions/{sessionId}",
		"POST sessions/{sessionId}/input", "PUT sessions/{sessionId}/size",
		"POST sessions/{sessionId}/stop", "POST sessions/{sessionId}/restart", "POST sessions/{sessionId}/folder",
		"GET saved-commands", "POST saved-commands", "PUT saved-commands/{commandId}", "DELETE saved-commands/{commandId}",
	} {
		method, path, _ := cutEditorRoute(route)
		mux.HandleFunc(method+" "+base+"/"+path, s.terminalGuard(s.terminalRequest))
	}
	mux.HandleFunc("GET "+base+"/events", s.terminalGuard(s.terminalEvents))
}

func (s *Server) terminalGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.terminalBot(r.PathValue("botId")); err != nil {
			editorError(w, http.StatusForbidden, err.Error())
			return
		}
		next(w, r)
	}
}

func (s *Server) terminalRequest(w http.ResponseWriter, r *http.Request) {
	bot, session := r.PathValue("botId"), r.PathValue("sessionId")
	var input struct {
		Cols, Rows                  int
		Data, Name, Command, Folder string
	}
	if r.Method != "GET" && r.Method != "DELETE" {
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
	}
	var result any = map[string]bool{"ok": true}
	var err error
	switch r.Pattern {
	case "GET /api/v1/plugins/terminal/bots/{botId}/sessions":
		var sessions []terminal.Snapshot
		sessions, err = s.Terminal.List(bot)
		result = map[string]any{"sessions": sessions}
	case "POST /api/v1/plugins/terminal/bots/{botId}/sessions":
		result, err = s.Terminal.Start(bot, input.Cols, input.Rows)
	case "GET /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}":
		var after uint64
		if raw := r.URL.Query().Get("afterSequence"); raw != "" {
			after, err = strconv.ParseUint(raw, 10, 64)
		}
		if err == nil {
			result, err = s.Terminal.Sync(bot, session, after)
		}
	case "POST /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}/input":
		err = s.Terminal.Write(bot, session, input.Data)
	case "PUT /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}/size":
		err = s.Terminal.Resize(bot, session, input.Cols, input.Rows)
	case "POST /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}/stop":
		err = s.Terminal.Stop(bot, session)
	case "POST /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}/restart":
		result, err = s.Terminal.Restart(bot, session, input.Cols, input.Rows)
	case "POST /api/v1/plugins/terminal/bots/{botId}/sessions/{sessionId}/folder":
		err = s.Terminal.ChangeFolder(bot, session, input.Folder)
	case "GET /api/v1/plugins/terminal/bots/{botId}/saved-commands":
		var commands []terminal.SavedCommand
		commands, err = s.Terminal.ListSavedCommands(bot)
		result = map[string]any{"commands": commands}
	case "POST /api/v1/plugins/terminal/bots/{botId}/saved-commands", "PUT /api/v1/plugins/terminal/bots/{botId}/saved-commands/{commandId}":
		var command terminal.SavedCommand
		command, err = s.Terminal.SaveCommand(bot, r.PathValue("commandId"), input.Name, input.Command)
		result = map[string]any{"command": command}
	case "DELETE /api/v1/plugins/terminal/bots/{botId}/saved-commands/{commandId}":
		err = s.Terminal.DeleteSavedCommand(bot, r.PathValue("commandId"))
	}
	if errors.Is(err, terminal.ErrSessionNotFound) {
		editorError(w, http.StatusNotFound, err.Error())
		return
	}
	respond(w, result, err)
}

func (s *Server) terminalEvents(w http.ResponseWriter, r *http.Request) {
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
	ch := make(chan terminal.Event, 64)
	s.terminalMu.Lock()
	if s.terminalClients[id] == nil {
		s.terminalClients[id] = map[chan terminal.Event]bool{}
	}
	s.terminalClients[id][ch] = true
	s.terminalMu.Unlock()
	defer func() { s.terminalMu.Lock(); delete(s.terminalClients[id], ch); s.terminalMu.Unlock() }()
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
	if conn.WriteJSON(map[string]string{"type": "terminal_subscribed", "botId": id}) != nil {
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
			if _, err := s.terminalBot(id); err != nil || !s.authenticated(r) {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)) != nil {
				return
			}
		case event := <-ch:
			if _, err := s.terminalBot(id); err != nil || !s.authenticated(r) {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteJSON(event) != nil {
				return
			}
		}
	}
}
