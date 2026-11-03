package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/languagecontribution"
	"net/http"
	"path/filepath"
	"strings"
)

func (s *Server) debugProvider(id, kind string) (debugger.Provider, error) {
	return s.Languages.Debugger(id, kind)
}
func (s *Server) leaseDebug(ctx context.Context, id, plugin string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoking[id] > 0 {
		return nil, nil, errors.New("bot scope is changing; retry")
	}
	b, err := s.editorBot(id)
	if err != nil {
		return nil, nil, err
	}
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, name := range []string{"monaco-editor", plugin} {
		m, e := s.Plugins.Get(name)
		if e != nil || !s.Plugins.Enabled(b, name) {
			release()
			return nil, nil, fmt.Errorf("%s is unavailable", name)
		}
		var done func()
		ctx, done, e = s.Plugins.Lease(ctx, b, name, m.Digest)
		if e != nil {
			release()
			return nil, nil, e
		}
		releases = append(releases, done)
	}
	return ctx, release, nil
}
func (s *Server) debugConfigurations(id string) (debugconfig.WorkspaceConfig, error) {
	roots, err := s.EditorFS.Roots(id)
	if err != nil {
		return debugconfig.WorkspaceConfig{}, err
	}
	config := debugconfig.Read(roots, func(ref editorfs.FileRef) ([]byte, error) {
		file, e := s.EditorFS.Read(id, ref)
		if e != nil {
			return nil, e
		}
		if len(file.Content) > 1<<20 {
			return nil, fmt.Errorf("debug configuration exceeds 1 MiB")
		}
		return []byte(file.Content), nil
	})
	for i, c := range config.Configurations {
		provider, e := s.debugProvider(id, c.AdapterProfileID)
		if e != nil {
			config.Configurations[i].Error = e.Error()
			continue
		}
		entry, inputs := provider.Configure(c)
		config.Configurations[i] = entry
		config.Inputs = append(config.Inputs, inputs...)
	}
	return config, nil
}
func (s *Server) initDebugger() {
	s.Languages = languagecontribution.New(s.goContribution())
	s.debugClients = map[string]map[chan debugger.Event]bool{}
	s.Debug = debugger.New(debugger.Options{FS: s.EditorFS, State: debugconfig.NewStateStore(s.Store), Resolve: func(id string) error { _, err := s.editorBot(id); return err }, Configurations: s.debugConfigurations, Provider: s.debugProvider, Lease: s.leaseDebug, AllowedSource: func(path string) bool {
		real, err := filepath.EvalSymlinks(path)
		return err == nil && s.Files.Allowed(domain.Bot{}, real)
	}})
	s.Debug.SetNotifier(func(event debugger.Event) {
		s.debugMu.Lock()
		defer s.debugMu.Unlock()
		for ch := range s.debugClients[event.WorkspaceID] {
			select {
			case ch <- event:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- debugger.Event{Type: "debug_resync", WorkspaceID: event.WorkspaceID}:
				default:
				}
			}
		}
	})
	s.Debug.SetRunInTerminal(func(ctx context.Context, id, session string, args map[string]any) (map[string]any, error) {
		if _, err := s.terminalBot(id); err != nil {
			return nil, err
		}
		return s.Terminal.StartDebugDAP(ctx, id, session, args)
	})
	s.Debug.SetStopDebugTerminals(func(id, session string) { s.Terminal.StopOwner(id, session) })
}
func (s *Server) debugRoutes(mux *http.ServeMux) {
	base := "/api/v1/plugins/monaco-editor/bots/{botId}/debug/"
	for _, route := range []string{"GET config", "GET snapshot", "GET state", "PUT state", "POST sessions", "POST sessions/{sessionId}/requests/{command}", "POST sessions/{sessionId}/{action}", "POST groups/{groupId}/{action}"} {
		method, path, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" "+base+path, s.editorGuard(s.debugRequest))
	}
	mux.HandleFunc("GET "+base+"events", s.editorGuard(s.debugEvents))
	for _, route := range []string{"GET status", "POST install", "GET processes"} {
		method, path, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /api/v1/plugins/go/bots/{botId}/debug/"+path, s.goDebugRequest)
	}
}

var debugRequests = map[string]bool{"threads": true, "stackTrace": true, "scopes": true, "variables": true, "evaluate": true, "completions": true, "source": true, "continue": true, "pause": true, "next": true, "stepIn": true, "stepOut": true, "exceptionInfo": true, "loadedSources": true, "cancel": true, "setVariable": true, "setExpression": true, "breakpointLocations": true}

func (s *Server) debugRequest(w http.ResponseWriter, r *http.Request) {
	id, session := r.PathValue("botId"), r.PathValue("sessionId")
	var result any
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "/config"):
		var config debugconfig.WorkspaceConfig
		config, err = s.debugConfigurations(id)
		profiles := []debugconfig.AdapterProfile{}
		for _, contribution := range s.Languages.Contributions() {
			for _, kind := range contribution.DebugTypes {
				if p, e := s.debugProvider(id, kind); e == nil {
					profiles = append(profiles, p.Metadata())
				}
			}
		}
		roots, _ := s.EditorFS.Roots(id)
		result = map[string]any{"config": config, "profiles": profiles, "roots": roots, "contributions": s.Languages.Contributions()}
	case strings.HasSuffix(r.URL.Path, "/snapshot"):
		var snapshot debugger.Snapshot
		snapshot, err = s.Debug.Snapshot(id)
		result = map[string]any{"snapshot": snapshot}
	case strings.HasSuffix(r.URL.Path, "/state"):
		var state debugconfig.State
		if r.Method == "GET" {
			state, err = s.Debug.State(id)
		} else {
			var body struct {
				ExpectedRevision uint64            `json:"expectedRevision"`
				State            debugconfig.State `json:"state"`
			}
			if editorDecode(w, r, &body, 1<<20) != nil {
				return
			}
			state, err = s.Debug.SetState(id, body.ExpectedRevision, body.State)
		}
		result = map[string]any{"state": state}
	case strings.HasSuffix(r.URL.Path, "/sessions"):
		var body debugger.StartRequest
		if editorDecode(w, r, &body, 1<<20) != nil {
			return
		}
		var snapshot debugger.Snapshot
		snapshot, err = s.Debug.Start(r.Context(), id, body)
		result = map[string]any{"snapshot": snapshot}
	case r.PathValue("command") != "":
		command := r.PathValue("command")
		if !debugRequests[command] {
			editorError(w, 400, "this DAP request is managed by Echo or unsupported")
			return
		}
		var body debugger.ControlRequest
		if editorDecode(w, r, &body, 1<<20) != nil {
			return
		}
		var response debugger.RequestResponse
		response, err = s.Debug.Request(r.Context(), id, session, command, body)
		result = map[string]any{"response": response}
	default:
		var body struct {
			ExpectedRevision  uint64                 `json:"expectedRevision"`
			ExpectedRevisions map[string]uint64      `json:"expectedRevisions"`
			TerminateDebuggee *bool                  `json:"terminateDebuggee"`
			Enabled           bool                   `json:"enabled"`
			Restart           *debugger.StartRequest `json:"restart"`
		}
		if editorDecode(w, r, &body, 64<<10) != nil {
			return
		}
		var fresh []debugger.StartRequest
		if body.Restart != nil {
			fresh = append(fresh, *body.Restart)
		}
		action := r.PathValue("action")
		group := r.PathValue("groupId")
		var snapshot debugger.Snapshot
		if group != "" {
			switch action {
			case "stop":
				err = s.Debug.StopGroupChecked(id, group, body.ExpectedRevisions)
			case "restart":
				snapshot, err = s.Debug.RestartGroup(r.Context(), id, group, body.ExpectedRevisions, fresh...)
			default:
				err = errors.New("unknown debug group action")
			}
		} else {
			switch action {
			case "stop":
				err = s.Debug.StopChecked(id, session, body.ExpectedRevision, body.TerminateDebuggee)
			case "disconnect":
				err = s.Debug.DisconnectChecked(id, session, body.ExpectedRevision)
			case "terminate":
				err = s.Debug.TerminateChecked(id, session, body.ExpectedRevision)
			case "restart":
				snapshot, err = s.Debug.RestartChecked(r.Context(), id, session, body.ExpectedRevision, fresh...)
			case "trace":
				var state debugger.SessionSnapshot
				state, err = s.Debug.SetTrace(id, session, body.ExpectedRevision, body.Enabled)
				result = map[string]any{"session": state}
			default:
				err = errors.New("unknown debug action")
			}
		}
		if result == nil {
			if err == nil && snapshot.WorkspaceID == "" {
				snapshot, err = s.Debug.Snapshot(id)
			}
			result = map[string]any{"snapshot": snapshot}
		}
	}
	if err != nil {
		var revision *debugconfig.RevisionConflict
		if errors.As(err, &revision) || errors.Is(err, debugger.ErrStaleSession) || errors.Is(err, debugger.ErrStaleStop) {
			editorError(w, 409, err.Error())
		} else if errors.Is(err, debugger.ErrSessionNotFound) {
			editorError(w, 404, err.Error())
		} else {
			editorError(w, 400, err.Error())
		}
		return
	}
	editorData(w, 200, result)
}
