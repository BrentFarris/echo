package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/gotool"
)

func (s *Server) inspectGo(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, a map[string]any) (any, error) {
	if !m.Builtin || m.ID != "go" || scope.PluginID != "go" {
		return nil, errors.New("Go inspection belongs to its built-in plugin")
	}
	b, m, err := s.goLanguageBot(scope.Bot.ID, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	project, err := gotool.PrepareProject(ctx, s.Files, b, goSettings(m), text(a, "directory"))
	if err != nil {
		return nil, err
	}
	key := "bot:" + b.ID + ":" + project.Directory
	s.goInspectMu.Lock()
	s.goLanguageMu.Lock()
	g := s.goLanguages[key]
	s.goLanguageMu.Unlock()
	if g != nil {
		select {
		case <-g.client.Done():
			g = nil
		default:
		}
	}
	if g == nil {
		lifetime, stopLifetime := context.WithCancel(s.ctx)
		stopStartup := context.AfterFunc(ctx, stopLifetime)
		g, err = s.startGoLanguage(lifetime, b, m, project.Directory, false, nil)
		stopStartup()
		if err != nil {
			stopLifetime()
		} else {
			go func(session *goLanguageSession) { <-session.done; stopLifetime() }(g)
		}
		if err == nil {
			s.goLanguageMu.Lock()
			s.goLanguages[key] = g
			s.goLanguageMu.Unlock()
		}
	}
	s.goInspectMu.Unlock()
	if err != nil {
		return nil, err
	}
	// Serialize overlays within this bot's saved-file session. Browser overlays
	// never enter this process. Cancellation also applies while waiting for it.
	locked := make(chan struct{})
	go func() { g.mu.Lock(); close(locked) }()
	select {
	case <-ctx.Done():
		go func() { <-locked; g.mu.Unlock() }()
		return nil, ctx.Err()
	case <-locked:
	}
	defer g.mu.Unlock()
	g.touch()
	defer g.touch()
	action := text(a, "action")
	file := text(a, "file")
	files := stringsValue(a["files"])
	if action == "diagnostics" {
		if len(files) == 0 {
			return nil, errors.New("diagnostics requires an explicit files list")
		}
	} else if file != "" {
		files = []string{file}
	}
	if len(files) > 64 {
		return nil, errors.New("inspect at most 64 files per request")
	}
	paths := b.Roots
	if len(paths) == 0 {
		paths = []string{project.Directory}
	}
	beforeFiles := s.Files.SnapshotFiles(ctx, b, paths)
	if !beforeFiles.Complete {
		return map[string]any{"incomplete": true, "reason": "Workspace snapshot exceeded its bounds; narrow the bot's folders before inspecting"}, nil
	}
	previous := g.savedSnapshot
	if previous.Files == nil {
		previous = broker.FileSnapshot{Files: map[string]string{}, Complete: true}
	}
	var changes []any
	for _, change := range broker.DiffFiles(b, previous, beforeFiles) {
		kind := 2
		if change.Operation == "created" {
			kind = 1
		}
		if change.Operation == "deleted" {
			kind = 3
		}
		changes = append(changes, map[string]any{"uri": g.processURI(change.Path), "type": kind})
	}
	if len(changes) > 0 {
		if err = g.client.Notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{"changes": changes}); err != nil {
			return nil, err
		}
	}
	g.savedSnapshot = beforeFiles
	snapshots := map[string]map[string]any{}
	opened := []string{}
	defer func() {
		for _, uri := range opened {
			g.client.Notify(context.Background(), "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
		}
	}()
	for i, path := range files {
		if !filepath.IsAbs(path) {
			path = filepath.Join(project.Directory, path)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		value, e := s.Files.Read(b, path)
		if e != nil {
			return nil, e
		}
		snapshots[path] = value
		files[i] = path
		uri := g.processURI(path)
		opened = append(opened, uri)
		language := "go"
		if filepath.Base(path) == "go.mod" {
			language = "go.mod"
		}
		if filepath.Base(path) == "go.work" {
			language = "go.work"
		}
		if e = g.client.Notify(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": 1, "text": value["content"]}}); e != nil {
			return nil, e
		}
	}
	params := map[string]any{}
	if len(files) > 0 {
		params["textDocument"] = map[string]any{"uri": g.processURI(files[0])}
	}
	method := ""
	switch action {
	case "diagnostics":
		method = "textDocument/diagnostic"
	case "symbols":
		if len(files) > 0 {
			method = "textDocument/documentSymbol"
		} else {
			method = "workspace/symbol"
			params["query"] = text(a, "query")
		}
	case "definition", "references", "hover":
		if len(files) == 0 || integer(a, "line") < 1 || integer(a, "column") < 1 {
			return nil, errors.New("provide file and one-based line/UTF-16 column")
		}
		method = "textDocument/" + action
		params["position"] = map[string]int{"line": integer(a, "line") - 1, "character": integer(a, "column") - 1}
		if action == "references" {
			params["context"] = map[string]any{"includeDeclaration": true}
		}
	default:
		return nil, errors.New("unsupported Go inspection action")
	}
	results := []any{}
	query := func(p map[string]any) (any, error) {
		raw, e := g.client.Call(ctx, method, p)
		if e != nil {
			return nil, e
		}
		var value any
		e = json.Unmarshal(raw, &value)
		if e != nil {
			return nil, e
		}
		return g.botResult(value)
	}
	if action == "diagnostics" {
		for _, path := range files {
			value, e := query(map[string]any{"textDocument": map[string]any{"uri": g.processURI(path)}})
			if e != nil {
				return nil, e
			}
			results = append(results, map[string]any{"path": path, "diagnostics": value, "revision": snapshots[path]["revision"]})
		}
	} else {
		value, e := query(params)
		if e != nil {
			return nil, e
		}
		results = append(results, value)
	}
	for path, value := range snapshots {
		current, e := s.Files.Read(b, path)
		if e != nil || current["revision"] != value["revision"] {
			return map[string]any{"incomplete": true, "reason": "Files changed during analysis; repeat the request"}, nil
		}
	}
	afterFiles := s.Files.SnapshotFiles(ctx, b, paths)
	if !afterFiles.Complete || !maps.Equal(beforeFiles.Files, afterFiles.Files) {
		return map[string]any{"incomplete": true, "reason": "Workspace changed during analysis; repeat the request"}, nil
	}
	data, _ := json.Marshal(results)
	if len(data) > 1<<20 {
		return map[string]any{"incomplete": true, "truncated": true, "reason": "Results exceed 1 MiB; narrow the query"}, nil
	}
	g.meta.Lock()
	loadError := g.loadError
	g.meta.Unlock()
	incomplete := loadError != "" || strings.Contains(string(data), `"source":"go list"`) || strings.Contains(string(data), "could not import")
	if incomplete && loadError == "" {
		loadError = "Package loading is incomplete; check dependency availability and Go network settings"
	}
	return map[string]any{"results": results, "incomplete": incomplete, "reason": loadError, "truncated": false, "savedFiles": true}, nil
}
func (g *goLanguageSession) botResult(v any) (any, error) {
	switch x := v.(type) {
	case []any:
		for i, item := range x {
			value, err := g.botResult(item)
			if err != nil {
				return nil, err
			}
			x[i] = value
		}
	case map[string]any:
		for key, item := range x {
			if key == "uri" || key == "targetUri" {
				if str, ok := item.(string); ok {
					path, err := g.hostPath(str)
					if err != nil {
						return nil, err
					}
					if _, err = g.mapURI(str, false); err != nil {
						return nil, err
					}
					delete(x, key)
					x[strings.Replace(key, "Uri", "Path", 1)] = path
					if key == "uri" {
						delete(x, "uri")
						x["path"] = path
					}
					continue
				}
			}
			if key == "line" {
				if n, ok := item.(float64); ok {
					x[key] = n + 1
					continue
				}
			}
			if key == "character" {
				if n, ok := item.(float64); ok {
					delete(x, key)
					x["column"] = n + 1
					continue
				}
			}
			value, err := g.botResult(item)
			if err != nil {
				return nil, err
			}
			x[key] = value
		}
	}
	return v, nil
}
