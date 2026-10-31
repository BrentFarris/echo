package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/gotool"
	"github.com/brent/echo/internal/languageservice"
)

type goLanguageSession struct {
	id            string
	bot           domain.Bot
	request       broker.ProcessRequest
	client        *languageservice.Client
	process       *broker.ProcessConnection
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex // Serializes saved-file requests and their overlays.
	meta          sync.Mutex
	logs          string
	loadError     string
	last          time.Time
	sources       map[string]string
	versions      map[string]int
	fs            *editorfs.Service
	files         *broker.Files
	send          func(languageservice.Message)
	savedSnapshot broker.FileSnapshot // Protected by mu; saved-file sessions only.
}

func goSettings(m domain.PluginManifest) gotool.Settings {
	return gotool.Settings{Executable: text(m.Config, "executable"), Toolchains: stringsValue(m.Config["toolchains"]), Internet: boolean(m.Config, "internet"), PrivateNetwork: boolean(m.Config, "privateNetwork"), Proxy: text(m.Config, "proxy"), Private: text(m.Config, "private")}
}

func (s *Server) lockGoTools(ctx context.Context) (func(), error) {
	s.goInstallMu.Lock()
	if s.goToolGate == nil {
		s.goToolGate = make(chan struct{}, 1)
	}
	gate := s.goToolGate
	s.goInstallMu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Server) goLanguageBot(id string, editor bool) (domain.Bot, domain.PluginManifest, error) {
	b, err := s.Bot(id)
	if err != nil {
		return b, domain.PluginManifest{}, err
	}
	m, err := s.Plugins.Get("go")
	if err != nil {
		return b, m, err
	}
	if !s.Plugins.Enabled(b, "go") || m.Config["languageServices"] == false {
		return b, m, errors.New("Go language services are disabled")
	}
	if editor && !s.Plugins.Enabled(b, "monaco-editor") {
		return b, m, errors.New("Monaco Editor is disabled")
	}
	return b, m, nil
}

// Register revocation before preparing grants, then reload configuration under
// the lease. A settings save cannot leave a new process using an old grant.
func (s *Server) leaseGoLanguage(ctx context.Context, id string, editor bool) (context.Context, domain.Bot, domain.PluginManifest, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoking[id] > 0 {
		return nil, domain.Bot{}, domain.PluginManifest{}, nil, errors.New("bot scope is changing; retry")
	}
	bot, m, err := s.goLanguageBot(id, editor)
	if err != nil {
		return nil, bot, m, nil, err
	}
	ctx, release, err := s.Plugins.Lease(ctx, bot, "go", m.Digest)
	if err != nil {
		return nil, bot, m, nil, err
	}
	if editor {
		monaco, e := s.Plugins.Get("monaco-editor")
		if e != nil {
			release()
			return nil, bot, m, nil, e
		}
		var second func()
		ctx, second, err = s.Plugins.Lease(ctx, bot, "monaco-editor", monaco.Digest)
		if err != nil {
			release()
			return nil, bot, m, nil, err
		}
		first := release
		release = func() { second(); first() }
	}
	bot, m, err = s.goLanguageBot(id, editor)
	if err != nil {
		release()
		return nil, bot, m, nil, err
	}
	return ctx, bot, m, release, nil
}

func (s *Server) startGoLanguage(ctx context.Context, bot domain.Bot, m domain.PluginManifest, directory string, editor bool, send func(languageservice.Message)) (*goLanguageSession, error) {
	unlock, err := s.lockGoTools(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.goLanguageMu.Lock()
	full := len(s.goLanguages) >= 32
	s.goLanguageMu.Unlock()
	if full {
		return nil, errors.New("Go language session limit reached; close unused editor sessions")
	}
	ctx, bot, m, release, err := s.leaseGoLanguage(ctx, bot.ID, editor)
	if err != nil {
		return nil, err
	}
	r, err := gotool.PrepareLanguage(ctx, s.Files, bot, goSettings(m), directory, text(m.Config, "goplsExecutable"))
	if err != nil {
		release()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	startupDeadline := time.AfterFunc(60*time.Second, cancel)
	defer startupDeadline.Stop()
	g := &goLanguageSession{id: domain.ID(), bot: bot, request: r, cancel: cancel, done: make(chan struct{}), last: time.Now(), sources: map[string]string{}, versions: map[string]int{}, fs: editorfs.New(s.Bot, s.Dir), files: s.Files, send: send}
	if len(bot.Roots) == 0 {
		g.fs = editorfs.New(func(id string) (domain.Bot, error) {
			current, e := s.Bot(id)
			if e == nil && len(current.Roots) == 0 {
				current.Roots = []string{r.Directory}
			}
			return current, e
		}, s.Dir)
	}
	p, err := s.Process.Start(ctx, r, func(chunk string) {
		g.meta.Lock()
		g.logs += chunk
		if len(g.logs) > 65536 {
			g.logs = g.logs[len(g.logs)-65536:]
		}
		g.meta.Unlock()
	})
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	g.process = p
	g.client = languageservice.New(ctx, p.Stdin, p.Stdout, p)
	s.goLanguageMu.Lock()
	if s.goLanguages == nil {
		s.goLanguages = map[string]*goLanguageSession{}
	}
	s.goLanguages[g.id] = g
	s.goLanguageMu.Unlock()
	go func() {
		defer close(g.done)
		defer release()
		defer cancel()
		<-g.client.Done()
		p.Close()
		p.Wait()
		s.goLanguageMu.Lock()
		for key, value := range s.goLanguages {
			if value == g {
				delete(s.goLanguages, key)
			}
		}
		s.goLanguageMu.Unlock()
		if s.EditorWatch != nil {
			s.EditorWatch.Refresh(bot.ID)
		}
	}()
	go func() {
		for event := range g.client.Events {
			if event.Method == "window/logMessage" || event.Method == "window/showMessage" {
				g.meta.Lock()
				var message struct {
					Type    int    `json:"type"`
					Message string `json:"message"`
				}
				json.Unmarshal(event.Params, &message)
				if message.Type == 1 && strings.Contains(message.Message, "Error loading workspace") {
					g.loadError = message.Message
				}
				g.logs += string(event.Params) + "\n"
				if len(g.logs) > 65536 {
					g.logs = g.logs[len(g.logs)-65536:]
				}
				g.meta.Unlock()
			}
			if send != nil {
				params, e := g.translate(event.Params, false)
				if e != nil {
					continue
				}
				event.Params = params
				send(event)
			}
		}
	}()
	root := filepath.Dir(filepath.Dir(r.Environment["GOMODCACHE"]))
	if work := r.Environment["GOWORK"]; work != "off" && work != "" {
		root = filepath.Dir(work)
	}
	initialize := map[string]any{"processId": nil, "rootUri": g.processURI(root), "workspaceFolders": []any{map[string]any{"uri": g.processURI(root), "name": filepath.Base(root)}}, "capabilities": map[string]any{
		"window":       map[string]any{"workDoneProgress": true},
		"general":      map[string]any{"positionEncodings": []string{"utf-16"}},
		"workspace":    map[string]any{"configuration": true, "workspaceFolders": true, "didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
		"textDocument": map[string]any{"synchronization": map[string]any{"didSave": true}, "publishDiagnostics": map[string]any{"versionSupport": true}, "completion": map[string]any{"completionItem": map[string]any{"snippetSupport": true, "resolveSupport": map[string]any{"properties": []string{"documentation", "detail", "additionalTextEdits"}}}}, "rename": map[string]any{"prepareSupport": true}, "documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true}, "diagnostic": map[string]any{}},
	}, "initializationOptions": map[string]any{"pullDiagnostics": !editor, "directoryFilters": []string{"-.cache", "-.git", "-node_modules"}}}
	initCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	if _, err = g.client.Call(initCtx, "initialize", initialize); err == nil {
		err = g.client.Notify(initCtx, "initialized", map[string]any{})
	}
	if err != nil {
		g.close()
		return nil, fmt.Errorf("start Go language server: %w; %s", err, g.log())
	}
	watch := editorfs.NewFilteredWatchManager(g.fs, func(event editorfs.WatchEvent) {
		if event.ResyncRequired {
			g.close()
			return
		}
		var changes []any
		for _, change := range event.Changes {
			if change.Op == "metadata" {
				continue
			}
			path, e := g.fs.ResolveEntryHostPath(bot.ID, change.Ref)
			if e != nil {
				g.close()
				return
			}
			kind := 2
			if change.Op == "create" {
				kind = 1
			}
			if change.Op == "delete" || change.Op == "rename" {
				kind = 3
			}
			changes = append(changes, map[string]any{"uri": g.processURI(path), "type": kind})
		}
		if len(changes) > 0 {
			g.client.Notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{"changes": changes})
		}
	}, func(path string) bool {
		for _, part := range strings.Split(filepath.ToSlash(path), "/") {
			if part == ".cache" || part == ".git" || part == "node_modules" {
				return true
			}
		}
		return false
	})
	if err = watch.SubscribeTracking(bot.ID); err != nil {
		watch.Close()
		g.close()
		return nil, err
	}
	go func() { <-ctx.Done(); watch.Close(); g.fs.Close() }()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.meta.Lock()
				idle := time.Since(g.last) > 5*time.Minute
				g.meta.Unlock()
				if idle {
					g.shutdown()
					return
				}
			}
		}
	}()
	return g, nil
}
func (g *goLanguageSession) touch()      { g.meta.Lock(); g.last = time.Now(); g.meta.Unlock() }
func (g *goLanguageSession) log() string { g.meta.Lock(); defer g.meta.Unlock(); return g.logs }
func (g *goLanguageSession) close()      { g.cancel(); g.client.Close(); g.process.Close() }
func (g *goLanguageSession) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := g.client.Call(ctx, "shutdown", nil); err == nil {
		g.client.Notify(ctx, "exit", nil)
	}
	g.close()
}

var goLSPMethods = map[string]bool{
	"textDocument/completion": true, "completionItem/resolve": true, "textDocument/hover": true, "textDocument/signatureHelp": true,
	"textDocument/definition": true, "textDocument/references": true, "textDocument/documentSymbol": true, "workspace/symbol": true,
	"textDocument/formatting": true, "textDocument/prepareRename": true, "textDocument/rename": true,
}

func (g *goLanguageSession) callEditor(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if !goLSPMethods[method] {
		return nil, errors.New("unsupported language service method")
	}
	g.touch()
	p, err := g.translate(params, true)
	if err != nil {
		return nil, err
	}
	editing := method == "textDocument/rename" || method == "textDocument/formatting"
	paths := g.bot.Roots
	if len(paths) == 0 {
		paths = []string{g.request.Directory}
	}
	var before broker.FileSnapshot
	if editing {
		var document struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		json.Unmarshal(p, &document)
		path, e := g.hostPath(document.TextDocument.URI)
		if e != nil || g.dependency(path) {
			return nil, errors.New("Go dependency sources are read-only")
		}
		if method == "textDocument/formatting" {
			paths = []string{path}
		}
		before = g.files.SnapshotFiles(ctx, g.bot, paths)
		if !before.Complete {
			return nil, errors.New("Cannot verify all edit targets; narrow the bot's folder scope and retry")
		}
	}
	result, err := g.client.Call(ctx, method, p)
	if err != nil {
		return nil, err
	}
	if !editing {
		return g.translate(result, false)
	}
	if len(result) == 0 || string(result) == "null" {
		return json.RawMessage("null"), nil
	}
	if method == "textDocument/formatting" {
		var document struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		json.Unmarshal(p, &document)
		result, _ = json.Marshal(map[string]any{"changes": map[string]any{document.TextDocument.URI: result}})
	}
	var edit struct {
		Changes         map[string]json.RawMessage `json:"changes"`
		DocumentChanges []struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		} `json:"documentChanges"`
	}
	if err = json.Unmarshal(result, &edit); err != nil {
		return nil, err
	}
	targets := map[string]bool{}
	for uri := range edit.Changes {
		targets[uri] = true
	}
	for _, change := range edit.DocumentChanges {
		targets[change.TextDocument.URI] = true
	}
	revisions := map[string]string{}
	for uri := range targets {
		path, e := g.hostPath(uri)
		if e != nil || g.dependency(path) {
			return nil, errors.New("Only workspace text edits are supported")
		}
		ref, e := g.fs.ReferenceForHostPath(g.bot.ID, path)
		if e != nil {
			return nil, e
		}
		snapshot, e := g.fs.Read(g.bot.ID, ref)
		if e != nil {
			return nil, e
		}
		wire, e := g.mapURI(uri, false)
		if e != nil {
			return nil, e
		}
		revisions[wire] = snapshot.Revision
	}
	after := g.files.SnapshotFiles(ctx, g.bot, paths)
	if !after.Complete || !maps.Equal(before.Files, after.Files) {
		return nil, errors.New("Files changed while preparing Go edits; retry")
	}
	translated, err := g.translate(result, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"edit": translated, "revisions": revisions})
}
