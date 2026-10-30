package server

import (
	"errors"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
	"net/http"
	"net/url"
	"reflect"
	"time"
)

func (s *Server) editorBot(id string) (domain.Bot, error) {
	b, err := s.Bot(id)
	if err != nil {
		return b, err
	}
	if !s.Plugins.Enabled(b, "monaco-editor") {
		return b, errors.New("Monaco Editor is disabled")
	}
	return b, nil
}

func (s *Server) initEditor() {
	s.editorClients = map[string]map[chan any]bool{}
	s.EditorFS = editorfs.New(s.editorBot, s.Dir)
	s.EditorWatch = editorfs.NewWatchManager(s.EditorFS, func(event editorfs.WatchEvent) {
		s.editorBroadcast(event.BotID, event)
		if s.SourceControl != nil {
			go s.SourceControl.HandleFileEvent(event)
		}
	})
}
func (s *Server) editorBroadcast(botID string, event any) {
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	for ch := range s.editorClients[botID] {
		select {
		case ch <- event:
		default:
			// Replace one queued event with a resync marker even if no later
			// filesystem event arrives to reveal the sequence gap.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- map[string]any{"type": "fs_resync_required", "botId": botID}:
			default:
			}
		}
	}
}
func (s *Server) editorRoutes(mux *http.ServeMux) {
	s.sourceControlEditorRoutes(mux)
	mux.HandleFunc("POST /api/v1/plugins/monaco-editor/bots/{botId}/inline-chat", s.editorGuard(s.handleInlineChat))
	routes := map[string]http.HandlerFunc{
		"GET roots": s.handleFSRoots, "GET entries": s.handleFSEntries, "GET file": s.handleFSReadFile,
		"PUT file": s.handleFSSaveFile, "POST entries": s.handleFSCreateEntry, "PATCH entry": s.handleFSRenameEntry,
		"DELETE entry": s.handleFSTrashEntry, "GET trash": s.handleFSListTrash, "POST trash/{trashId}/restore": s.handleFSRestoreTrash,
		"DELETE trash/{trashId}": s.handleFSPurgeTrash, "POST reveal": s.handleFSReveal, "GET media": s.handleFSMedia,
		"GET search": s.handleFSSearch, "POST text-search": s.handleFSTextSearch, "POST text-replace": s.handleFSTextReplace,
	}
	for spec, handler := range routes {
		method, path, _ := cutEditorRoute(spec)
		mux.HandleFunc(method+" /api/v1/plugins/monaco-editor/bots/{botId}/fs/"+path, s.editorGuard(handler))
	}
	mux.HandleFunc("GET /api/v1/plugins/monaco-editor/bots/{botId}/events", s.editorGuard(s.editorEvents))
	mux.HandleFunc("GET /api/v1/plugins/monaco-editor/bots/{botId}/bookmarks", s.editorGuard(s.editorBookmarks))
	mux.HandleFunc("POST /api/v1/plugins/monaco-editor/bots/{botId}/bookmarks", s.editorGuard(s.editorBookmarks))
}
func cutEditorRoute(s string) (string, string, bool) {
	for i, c := range s {
		if c == ' ' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
func (s *Server) editorGuard(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.editorBot(r.PathValue("botId")); err != nil {
			editorError(w, http.StatusForbidden, err.Error())
			return
		}
		handler(w, r)
	}
}
func (s *Server) editorEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("botId")
	bot, err := s.editorBot(id)
	if err != nil {
		return
	}
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		if swarm.Owner(r.Context()) {
			return true
		}
		u, e := url.Parse(r.Header.Get("Origin"))
		return e == nil && u.Host == r.Host
	}}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	sessionID := sessionKey(r)
	if caller, ok := swarm.Caller(r.Context()); ok {
		sessionID = "swarm:" + caller.NodeID
	}
	s.authMu.Lock()
	if s.sessionSockets == nil {
		s.sessionSockets = map[string]map[*websocket.Conn]bool{}
	}
	if s.sessionSockets[sessionID] == nil {
		s.sessionSockets[sessionID] = map[*websocket.Conn]bool{}
	}
	s.sessionSockets[sessionID][conn] = true
	s.authMu.Unlock()
	defer func() { s.authMu.Lock(); delete(s.sessionSockets[sessionID], conn); s.authMu.Unlock() }()
	ch := make(chan any, 64)
	s.editorMu.Lock()
	if s.editorClients[id] == nil {
		s.editorClients[id] = map[chan any]bool{}
	}
	s.editorClients[id][ch] = true
	s.editorMu.Unlock()
	defer func() {
		s.editorMu.Lock()
		delete(s.editorClients[id], ch)
		s.editorMu.Unlock()
		s.EditorWatch.Unsubscribe(id)
	}()
	if err = s.EditorWatch.Subscribe(id); err != nil {
		conn.WriteJSON(map[string]any{"type": "fs_resync_required", "botId": id})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scmSubscribed := false
		defer func() {
			if scmSubscribed {
				s.SourceControl.Unsubscribe(id)
			}
		}()
		conn.SetReadLimit(64 << 10)
		for {
			var msg struct {
				Refs          []editorfs.FileRef `json:"refs"`
				SourceControl bool               `json:"sourceControl"`
			}
			if conn.ReadJSON(&msg) != nil {
				return
			}
			s.EditorWatch.AddReferences(id, msg.Refs)
			if s.SourceControl != nil && msg.SourceControl != scmSubscribed {
				if msg.SourceControl {
					scmSubscribed = s.SourceControl.Subscribe(r.Context(), id) == nil
				} else {
					s.SourceControl.Unsubscribe(id)
					scmSubscribed = false
				}
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-s.ctx.Done():
			return
		case event := <-ch:
			if _, err := s.editorBot(id); err != nil {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteJSON(event) != nil {
				return
			}
		case <-ticker.C:
			current, err := s.editorBot(id)
			if err != nil || !s.authenticated(r) {
				return
			}
			if !reflect.DeepEqual(bot.Roots, current.Roots) {
				bot = current
				s.EditorWatch.Refresh(id)
				s.EditorFS.Invalidate(id)
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if conn.WriteJSON(map[string]any{"type": "fs_resync_required", "botId": id}) != nil {
					return
				}
			}
		}
	}
}
