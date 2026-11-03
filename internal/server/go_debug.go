package server

import (
	"context"
	"debug/buildinfo"
	"errors"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/godebug"
	"github.com/brent/echo/internal/gotool"
	"github.com/brent/echo/internal/languagecontribution"
	"net/http"
	"strings"
	"time"
)

func (s *Server) goContribution() languagecontribution.Contribution {
	return languagecontribution.Contribution{PluginID: "go", Languages: []string{"go"}, DebugTypes: []string{"go"}, LanguageService: "/api/v1/plugins/go/bots/{botId}/language", Capability: "go-language/1", Debugger: s.goDebugger}
}

// The registry belongs to the host. Installed manifests cannot turn a browser
// app or a WASI tool into an unrestricted native debugger.
func (s *Server) goDebugger(id string) (debugger.Provider, error) {
	if _, _, err := s.goDebugBot(id); err != nil {
		return nil, err
	}
	return &godebug.Provider{Executable: s.resolveDelve, Project: func(ctx context.Context, directory string) (broker.ProcessRequest, error) {
		b, m, err := s.goDebugBot(id)
		if err != nil {
			return broker.ProcessRequest{}, err
		}
		return gotool.PrepareProject(ctx, s.Files, b, goSettings(m), directory)
	}}, nil
}
func (s *Server) goDebugBot(id string) (domain.Bot, domain.PluginManifest, error) {
	b, err := s.editorBot(id)
	if err != nil {
		return b, domain.PluginManifest{}, err
	}
	m, err := s.Plugins.Get("go")
	if err == nil && (!s.Plugins.Enabled(b, "go") || m.Config["debugging"] == false) {
		err = errors.New("Go debugging is disabled")
	}
	return b, m, err
}
func (s *Server) goDebugRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("botId")
	ctx, release, err := s.leaseDebug(r.Context(), id, "go")
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	defer release()
	r = r.WithContext(ctx)
	b, m, err := s.goDebugBot(id)
	if err != nil {
		editorError(w, 403, err.Error())
		return
	}
	if strings.HasSuffix(r.URL.Path, "/processes") {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		processes, e := godebug.Processes(ctx, r.URL.Query().Get("filter"), r.URL.Query().Get("goOnly") == "true")
		if e != nil {
			editorError(w, 400, e.Error())
			return
		}
		editorData(w, 200, map[string]any{"processes": processes})
		return
	}
	directory := r.URL.Query().Get("directory")
	if directory == "" && len(b.Roots) > 0 {
		directory = b.Roots[0]
	}
	if r.Method == "POST" {
		var body struct {
			Directory string `json:"directory"`
		}
		if editorDecode(w, r, &body, 4096) != nil {
			return
		}
		if body.Directory != "" {
			directory = body.Directory
		}
	}
	project, err := gotool.PrepareProject(r.Context(), s.Files, b, goSettings(m), directory)
	if err != nil {
		editorError(w, 400, err.Error())
		return
	}
	roots, _ := s.EditorFS.Roots(id)
	config := debugconfig.Configuration{Folder: directory, Settings: map[string]any{}}
	for _, root := range roots {
		if broker.Within(root.HostPath, directory) {
			config.Folder = root.HostPath
			file, e := s.EditorFS.Read(id, editorfs.FileRef{RootID: root.ID, Path: ".vscode/settings.json"})
			if e == nil {
				if e = debugconfig.DecodeJSONC([]byte(file.Content), &config.Settings); e != nil {
					editorError(w, 400, e.Error())
					return
				}
			}
			break
		}
	}
	if r.Method == "POST" {
		if tools, ok := config.Settings["go.alternateTools"].(map[string]any); ok && tools["dlv"] != "" && tools["dlv"] != nil {
			editorError(w, 400, "A workspace Delve executable is configured; update that installation or remove go.alternateTools.dlv")
			return
		}
		unlock, e := s.lockGoTools(r.Context())
		if e != nil {
			editorError(w, 400, e.Error())
			return
		}
		defer unlock()
		project.Args = []string{"install", "github.com/go-delve/delve/cmd/dlv@" + godebug.DelveVersion}
		ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
		defer cancel()
		result, e := s.runTrackedProcess(ctx, b, project, nil)
		if e != nil {
			editorError(w, 400, e.Error())
			return
		}
		if result.ExitCode != 0 {
			editorError(w, 422, "Delve installation failed: "+result.Output)
			return
		}
	}
	executable, err := s.resolveDelve(project, config)
	state, message := "ready", ""
	if err != nil {
		state = "installation-required"
		message = err.Error()
	}
	version := ""
	if info, e := buildinfo.ReadFile(executable); e == nil {
		version = info.Main.Version
	}
	editorData(w, 200, map[string]any{"state": state, "message": message, "executable": executable, "version": version, "managedVersion": godebug.DelveVersion})
}

func (s *Server) resolveDelve(project broker.ProcessRequest, config debugconfig.Configuration) (string, error) {
	path, err := godebug.ResolveExecutable(project, config)
	if err == nil && !s.Files.Allowed(domain.Bot{}, path) {
		return "", errors.New("the selected Delve executable is inside private Echo state")
	}
	return path, err
}
