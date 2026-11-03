package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/brent/echo/internal/gotool"
	"github.com/brent/echo/internal/languageservice"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
)

func (s *Server) goLanguageRoutes(mux *http.ServeMux) {
	base := "/api/v1/plugins/go/bots/{botId}/language/"
	mux.HandleFunc("GET "+base+"status", s.goLanguageStatus)
	mux.HandleFunc("POST "+base+"install", s.goLanguageInstall)
	mux.HandleFunc("GET "+base+"session", s.goLanguageSocket)
	mux.HandleFunc("GET "+base+"source", s.goLanguageSource)
}
func (s *Server) goLanguageStatus(w http.ResponseWriter, r *http.Request) {
	b, m, err := s.goLanguageBot(r.PathValue("botId"), true)
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	request, err := gotool.PrepareLanguage(r.Context(), s.Files, b, goSettings(m), r.URL.Query().Get("directory"), text(m.Config, "goplsExecutable"))
	state := "ready"
	message := ""
	if err != nil {
		state = "installation-required"
		message = err.Error()
	}
	directory := filepath.Dir(filepath.Dir(request.Environment["GOMODCACHE"]))
	if work := request.Environment["GOWORK"]; work != "" && work != "off" {
		directory = filepath.Dir(work)
	}
	editorData(w, 200, map[string]any{"state": state, "message": message, "version": gotool.GoplsVersion, "directory": directory})
}
func (s *Server) goLanguageInstall(w http.ResponseWriter, r *http.Request) {
	b, m, err := s.goLanguageBot(r.PathValue("botId"), true)
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	if text(m.Config, "goplsExecutable") != "" {
		editorError(w, 400, "A custom gopls executable is configured; update that installation or clear the setting")
		return
	}
	var body struct {
		Directory string `json:"directory"`
	}
	if editorDecode(w, r, &body, 4096) != nil {
		return
	}
	// Installations serialize before stopping users of the destination executable.
	unlock, err := s.lockGoTools(r.Context())
	if err != nil {
		editorError(w, 408, err.Error())
		return
	}
	defer unlock()
	ctx, b, m, release, err := s.leaseGoLanguage(r.Context(), b.ID, true)
	if err != nil {
		editorError(w, 409, err.Error())
		return
	}
	defer release()
	req, err := gotool.PrepareLanguageProject(ctx, s.Files, b, goSettings(m), body.Directory)
	req.Args = []string{"install", "golang.org/x/tools/gopls@" + gotool.GoplsVersion}
	if err != nil {
		editorError(w, 400, err.Error())
		return
	}
	s.goLanguageMu.Lock()
	var stopped []*goLanguageSession
	for _, g := range s.goLanguages {
		if g.request.Executable == gotool.GoplsPath(req) {
			g.close()
			stopped = append(stopped, g)
		}
	}
	s.goLanguageMu.Unlock()
	for _, g := range stopped {
		<-g.done
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	result, err := s.runTrackedProcess(ctx, b, req, nil)
	if err != nil {
		editorError(w, 500, err.Error())
		return
	}
	if result.ExitCode != 0 {
		editorError(w, 422, "gopls installation failed: "+result.Output)
		return
	}
	editorData(w, 200, map[string]any{"state": "installed", "version": gotool.GoplsVersion, "result": result})
}
func (s *Server) goLanguageSource(w http.ResponseWriter, r *http.Request) {
	b, _, err := s.goLanguageBot(r.PathValue("botId"), true)
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	u, err := url.Parse(r.URL.Query().Get("uri"))
	if err != nil || u.Scheme != "echo-go" {
		editorError(w, 400, "invalid source handle")
		return
	}
	s.goLanguageMu.Lock()
	g := s.goLanguages[u.Host]
	s.goLanguageMu.Unlock()
	if g == nil || g.bot.ID != b.ID {
		editorError(w, 404, "source session ended; repeat Go navigation")
		return
	}
	host, err := g.mapURI(u.String(), true)
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	path, err := g.hostPath(host)
	if err != nil {
		editorError(w, 400, err.Error())
		return
	}
	// Handles are issued only for the exact selected SDK/cache. The unbound read
	// still rejects Echo private data and uses the confined file reader.
	readBot := b
	if len(g.request.PathPrepend) > 1 {
		readBot.Roots = append(append([]string{}, b.Roots...), filepath.Dir(g.request.PathPrepend[1]))
	}
	file, err := s.Files.Read(readBot, path)
	if err != nil {
		editorError(w, 400, err.Error())
		return
	}
	editorData(w, 200, map[string]any{"content": file["content"], "uri": u.String(), "readOnly": true})
}
func (s *Server) goLanguageSocket(w http.ResponseWriter, r *http.Request) {
	b, m, err := s.goLanguageBot(r.PathValue("botId"), true)
	if err != nil {
		editorError(w, 403, err.Error())
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
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	stopSocket := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopSocket()
	type outgoing struct {
		message languageservice.Message
		written chan struct{}
	}
	out := make(chan outgoing, 64)
	send := func(msg languageservice.Message) {
		select {
		case out <- outgoing{message: msg}:
		case <-ctx.Done():
		default:
			cancel()
		}
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case next := <-out:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if conn.WriteJSON(next.message) != nil {
					cancel()
					return
				}
				if next.written != nil {
					close(next.written)
				}
			}
		}
	}()
	defer func() { cancel(); <-writerDone }()
	status := func(state, message string) {
		params, _ := json.Marshal(map[string]any{"state": state, "message": message})
		written := make(chan struct{})
		select {
		case out <- outgoing{message: languageservice.Message{Method: "echo/status", Params: params}, written: written}:
		case <-ctx.Done():
			return
		}
		select {
		case <-written:
		case <-ctx.Done():
		}
	}
	in := make(chan languageservice.Message, 64)
	conn.SetReadLimit(languageservice.MaxMessage)
	go func() {
		for {
			var message languageservice.Message
			if conn.ReadJSON(&message) != nil {
				cancel()
				return
			}
			select {
			case in <- message:
			case <-ctx.Done():
				return
			default:
				cancel()
				return
			}
		}
	}()
	status("starting", "Loading Go language services")
	g, err := s.startGoLanguage(ctx, b, m, r.URL.Query().Get("directory"), true, send)
	if err != nil {
		status("failed", err.Error())
		return
	}
	defer g.close()
	status("ready", "")
	go func() { <-g.client.Done(); cancel() }()
	requests := map[string]context.CancelFunc{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	limit := make(chan struct{}, 16)
	for {
		var message languageservice.Message
		select {
		case message = <-in:
		case <-ctx.Done():
			return
		}
		if _, _, err = s.goLanguageBot(b.ID, true); err != nil || !s.authenticated(r) {
			return
		}
		g.touch()
		if message.Method == "echo/ping" {
			continue
		}
		if message.Method == "$/cancelRequest" {
			var p struct {
				ID json.RawMessage `json:"id"`
			}
			json.Unmarshal(message.Params, &p)
			mu.Lock()
			if stop := requests[string(p.ID)]; stop != nil {
				stop()
			}
			mu.Unlock()
			continue
		}
		if len(message.ID) == 0 {
			if err = g.notifyEditor(ctx, message.Method, message.Params); err != nil {
				status("failed", err.Error())
				return
			}
			continue
		}
		select {
		case limit <- struct{}{}:
		default:
			status("failed", "too many language requests")
			return
		}
		requestCtx, stop := context.WithTimeout(ctx, 60*time.Second)
		id := string(message.ID)
		mu.Lock()
		if _, exists := requests[id]; exists {
			mu.Unlock()
			stop()
			return
		}
		requests[id] = stop
		mu.Unlock()
		wg.Add(1)
		go func(message languageservice.Message) {
			defer wg.Done()
			defer func() { <-limit; stop(); mu.Lock(); delete(requests, id); mu.Unlock() }()
			result, e := g.callEditor(requestCtx, message.Method, message.Params)
			reply := languageservice.Message{ID: message.ID, Result: result}
			if e != nil {
				reply.Error = &languageservice.RPCError{Code: -32000, Message: e.Error()}
				reply.Result = nil
			}
			send(reply)
		}(message)
	}
}
func (g *goLanguageSession) notifyEditor(ctx context.Context, method string, raw json.RawMessage) error {
	switch method {
	case "textDocument/didOpen", "textDocument/didChange", "textDocument/didSave", "textDocument/didClose":
	default:
		return errors.New("unsupported language notification")
	}
	p, err := g.translate(raw, true)
	if err != nil {
		return err
	}
	var doc struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int    `json:"version"`
		} `json:"textDocument"`
	}
	if err = json.Unmarshal(p, &doc); err != nil || doc.TextDocument.URI == "" {
		return errors.New("missing language document")
	}
	path, err := g.hostPath(doc.TextDocument.URI)
	if err != nil {
		return err
	}
	if g.dependency(path) {
		return errors.New("dependency sources are read-only")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	old, open := g.versions[path]
	switch method {
	case "textDocument/didOpen":
		if open {
			return errors.New("document already open")
		}
		g.versions[path] = doc.TextDocument.Version
	case "textDocument/didChange":
		if !open || doc.TextDocument.Version <= old {
			return errors.New("stale document version")
		}
		g.versions[path] = doc.TextDocument.Version
	case "textDocument/didClose":
		delete(g.versions, path)
	case "textDocument/didSave":
		if !open {
			return errors.New("document not open")
		}
	}
	return g.client.Notify(ctx, method, p)
}
