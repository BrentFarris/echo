package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/fossil"
	"github.com/brent/echo/internal/sourcecontrol/gitprovider"
	"github.com/brent/echo/internal/sourcecontrol/p4"
)

type sourceControlSettings struct {
	SearchParentRepositories bool `json:"searchParentRepositories"`
}

func (s *Server) scmSettings(id string) sourceControlSettings {
	var value sourceControlSettings
	_ = s.Store.Get("source-control-settings", id, &value)
	return value
}
func (s *Server) initSourceControl() {
	run := &scmexec.Runtime{Execute: s.executeSCM, Available: s.scmAvailable,
		ValidateFossilRepository: s.validateFossilRepository,
		Defaults: func(bot, id string) map[string]string {
			m, e := s.Plugins.Get(id)
			out := map[string]string{}
			if e == nil {
				for k, v := range m.Config {
					if value, ok := v.(string); ok && k != "credential" {
						out[k] = value
					}
				}
			}
			return out
		},
		ValidatePath: func(id, path string) (string, error) {
			b, e := s.Bot(id)
			if e != nil {
				return "", e
			}
			if !s.Files.Allowed(b, path) {
				return "", errors.New("repository metadata is outside the bot's folders")
			}
			return path, nil
		},
	}
	workspaces := &scmexec.Workspaces{Get: func(id string) (scmexec.Workspace, bool, error) {
		_, e := s.Bot(id)
		return scmexec.Workspace{SearchParentRepositories: s.scmSettings(id).SearchParentRepositories}, e == nil, e
	}}
	s.SourceControl = sourcecontrol.New()
	s.Git = gitservice.New(workspaces, s.EditorFS)
	s.Git.SetRuntime(run)
	s.P4 = p4.New(s.EditorFS, run, filepath.Join(s.Dir, "source-control", "p4"))
	_ = s.SourceControl.Register(gitprovider.New(s.Git))
	s.Fossil = fossil.New(workspaces, s.EditorFS, run, filepath.Join(s.Dir, "source-control", "fossil"))
	_ = s.SourceControl.Register(s.Fossil)
	_ = s.SourceControl.Register(s.P4)
	s.EditorFS.SetMutationCoordinator(s.SourceControl)
	s.Files.Coordinator = s.SourceControl
	s.P4.SetWatcher(func(id string, enabled bool) error {
		if enabled {
			return s.EditorWatch.SubscribeTracking(id)
		}
		s.EditorWatch.UnsubscribeTracking(id)
		return nil
	})
	s.SourceControl.SetNotifier(func(event sourcecontrol.Event) {
		raw, _ := json.Marshal(event)
		value := map[string]any{}
		_ = json.Unmarshal(raw, &value)
		value["botId"] = event.WorkspaceID
		s.editorBroadcast(event.WorkspaceID, value)
	})
}

func (s *Server) scmAvailable(botID, id string) error {
	b, e := s.Bot(botID)
	if e != nil {
		return e
	}
	if !s.Plugins.Enabled(b, id) {
		return fmt.Errorf("%s plugin is disabled for this bot", id)
	}
	m, e := s.Plugins.Get(id)
	if e != nil {
		return e
	}
	binary := text(m.Config, "executable")
	if binary == "" {
		binary = id
	}
	if _, e = exec.LookPath(binary); e != nil {
		return fmt.Errorf("install %s or configure its executable in plugin settings", id)
	}
	return nil
}

func (s *Server) executeSCM(ctx context.Context, botID string, request scmexec.Request) (scmexec.Result, error) {
	var out scmexec.Result
	if len(request.Command) == 0 {
		return out, errors.New("source control command required")
	}
	id := request.Command[0]
	if !sourceControl(id) {
		return out, errors.New("unknown source control provider")
	}
	if err := s.scmAvailable(botID, id); err != nil {
		return out, err
	}
	bot, err := s.Bot(botID)
	if err != nil {
		return out, err
	}
	m, err := s.Plugins.Get(id)
	if err != nil {
		return out, err
	}
	if !s.Files.Allowed(bot, request.WorkingDirectory) {
		return out, errors.New("repository directory is outside the bot's folders")
	}
	roots := append([]string(nil), bot.Roots...)
	if id == "fossil" {
		_, metadataRoot, err := s.fossilRepositoryAccess(ctx, bot, request.WorkingDirectory)
		if err != nil {
			return out, err
		}
		if metadataRoot != "" {
			// This grant belongs only to the host-owned Fossil invocation. It is
			// never saved as a bot root or exposed through editor/file APIs.
			roots = []string{metadataRoot}
			for _, root := range bot.Roots {
				if !broker.Within(metadataRoot, root) {
					roots = append(roots, root)
				}
			}
		}
	}
	config := map[string]any{}
	for k, v := range m.Config {
		config[k] = v
	}
	args := append([]string{}, request.Command[1:]...)
	if id == "p4" {
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "-p":
				config["port"] = args[i+1]
				i++
			case "-u":
				config["user"] = args[i+1]
				i++
			case "-c":
				config["client"] = args[i+1]
				i++
			}
		}
		if !boolean(config, "internet") && !boolean(config, "privateNetwork") {
			return out, errors.New("grant network access in P4 plugin settings")
		}
	}
	_, env, err := scmArguments(id, "version", nil, config)
	if err != nil {
		return out, err
	}
	for _, entry := range request.Environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	env["GIT_CONFIG_GLOBAL"] = ""
	env["P4CONFIG"] = "__echo_disabled__"
	env["P4PASSWD"] = "echo-no-credential"
	env["P4EDITOR"] = "echo-editor-disabled"
	if path := text(config, "trustFile"); path != "" {
		env["P4TRUST"] = path
	}
	secret := ""
	if id == "p4" {
		port := strings.ToLower(text(config, "port"))
		if strings.HasPrefix(port, "rsh:") || strings.ContainsAny(port, " \t\r\n") {
			return out, errors.New("P4 server must be a network address")
		}
		ref := ""
		if text(config, "port") == text(m.Config, "port") && text(config, "user") == text(m.Config, "user") {
			ref = text(m.Config, "credential")
		}
		var saved struct {
			Credential string `json:"credential"`
		}
		_ = s.Store.Get("p4-connections", p4CredentialKey(text(config, "port"), text(config, "user")), &saved)
		if saved.Credential != "" {
			ref = saved.Credential
		}
		if ref != "" {
			secret, err = s.Secrets.Get(ctx, ref)
			if err != nil {
				return out, err
			}
			env["P4PASSWD"] = secret
		}
	}
	binary := text(config, "executable")
	if binary == "" {
		binary = id
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		binary, err = exec.LookPath(binary)
		if err != nil {
			return out, err
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return out, err
	}
	runtimePath := filepath.Dir(binary)
	if id == "git" && (strings.EqualFold(filepath.Base(runtimePath), "cmd") || strings.EqualFold(filepath.Base(runtimePath), "bin")) {
		runtimePath = filepath.Dir(runtimePath)
	}
	toolchains := append([]string{runtimePath}, stringsValue(config["toolchains"])...)
	for _, path := range toolchains {
		if broker.Within(path, s.Dir) || broker.Within(s.Dir, path) {
			return out, errors.New("CLI runtime cannot contain Echo private data")
		}
	}
	if path := text(config, "trustFile"); path != "" && !s.Files.Allowed(bot, path) {
		return out, errors.New("P4 trust file is outside the bot's folders")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	captureArgument := ""
	if request.CaptureOutputFile {
		captureArgument = scmexec.OutputFileToken
	}
	result, err := s.Process.Run(ctx, broker.ProcessRequest{SandboxSession: "source-control:" + id, OutputFileArgument: captureArgument, BotID: bot.ID, Roots: roots, Executable: binary, Args: args, Directory: request.WorkingDirectory, Input: string(request.Input), Environment: env, Toolchains: toolchains, Internet: boolean(config, "internet"), PrivateNetwork: boolean(config, "privateNetwork"), SeparateOutput: true, OutputLimit: request.OutputLimit, TimeoutSeconds: 300}, nil)
	filter := &secretFilter{secret: secret}
	out = scmexec.Result{Stdout: []byte(result.Stdout), Stderr: []byte(filter.redact(result.Stderr)), ExitCode: int(result.ExitCode), StdoutTruncated: result.Truncated}
	if err != nil {
		return out, errors.New(filter.redact(err.Error()))
	}
	return out, nil
}

func p4CredentialKey(server, user string) string {
	return sourcecontrol.RepositoryID("p4-credential", server, user)
}

func (s *Server) sourceControlEditorRoutes(mux *http.ServeMux) {
	base := "/api/v1/plugins/monaco-editor/bots/{botId}/source-control"
	handle := func(method, path string, fn http.HandlerFunc) {
		mux.HandleFunc(method+" "+base+path, s.editorGuard(fn))
	}
	reply := func(w http.ResponseWriter, value any, err error) {
		if err != nil {
			var typed *sourcecontrol.Error
			if errors.As(err, &typed) {
				editorData(w, 409, map[string]any{"error": typed.Message, "code": typed.Code})
				return
			}
			editorError(w, 400, err.Error())
			return
		}
		editorData(w, 200, value)
	}
	handle("GET", "/repositories/{repositoryId}/ui/{path...}", func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.TrimSuffix(r.URL.Path, r.PathValue("path"))
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		response, e := s.Fossil.WebUI(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"), scheme+"://"+r.Host+strings.TrimSuffix(prefix, "/"), r.PathValue("path")+func() string {
			if r.URL.RawQuery != "" {
				return "?" + r.URL.RawQuery
			}
			return ""
		}())
		if e != nil {
			reply(w, nil, e)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'self' 'unsafe-inline' data:; form-action 'none'; frame-ancestors 'self'")
		if location := response.Header.Get("Location"); location != "" {
			parsed, e := url.Parse(location)
			if e != nil || parsed.IsAbs() && parsed.Host != r.Host {
				reply(w, nil, errors.New("Fossil UI redirect outside Echo"))
				return
			}
			w.Header().Set("Location", location)
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	handle("GET", "/repositories", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("botId")
		repos, e := s.SourceControl.Repositories(r.Context(), id)
		reply(w, map[string]any{"repositories": repos, "providers": s.SourceControl.Providers(r.Context(), id), "searchParentRepositories": s.scmSettings(id).SearchParentRepositories}, e)
	})
	handle("GET", "/repositories/{repositoryId}/status", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.SourceControl.Status(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"))
		reply(w, v, e)
	})
	handle("GET", "/repositories/{repositoryId}/metadata", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.SourceControl.Metadata(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"))
		reply(w, v, e)
	})
	handle("GET", "/repositories/{repositoryId}/diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, e := s.SourceControl.Diff(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"), sourcecontrol.DiffTarget{Kind: q.Get("kind"), GroupID: q.Get("groupId"), Path: q.Get("path"), OldPath: q.Get("oldPath"), BaseRef: q.Get("baseRef"), Ref: q.Get("ref")})
		reply(w, v, e)
	})
	handle("GET", "/repositories/{repositoryId}/detail", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kind") != "stash" {
			reply(w, nil, errors.New("repository history is not enabled"))
			return
		}
		v, e := s.SourceControl.RevisionDetail(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"), r.URL.Query().Get("ref"), "stash")
		reply(w, v, e)
	})
	handle("POST", "/repositories/{repositoryId}/actions", func(w http.ResponseWriter, r *http.Request) {
		var request sourcecontrol.ActionRequest
		if e := editorDecode(w, r, &request, 2<<20); e != nil {
			reply(w, nil, e)
			return
		}
		if strings.Contains(request.Action, "protect") {
			reply(w, nil, errors.New("Fossil protection is not enabled"))
			return
		}
		if request.Action == "open_ui" {
			_, e := s.Fossil.Metadata(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"))
			reply(w, map[string]any{"requestId": request.RequestID, "repositoryId": r.PathValue("repositoryId"), "url": strings.TrimSuffix(r.URL.Path, "actions") + "ui/"}, e)
			return
		}
		v, e := s.SourceControl.Action(r.Context(), r.PathValue("botId"), r.PathValue("repositoryId"), request)
		reply(w, v, e)
	})
	handle("GET", "/settings", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("botId")
		reply(w, map[string]any{"p4": s.P4.Settings(id), "searchParentRepositories": s.scmSettings(id).SearchParentRepositories}, nil)
	})
	handle("PUT", "/settings", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			SearchParentRepositories *bool        `json:"searchParentRepositories"`
			P4                       *p4.Settings `json:"p4"`
		}
		if e := editorDecode(w, r, &request, 1<<20); e != nil {
			reply(w, nil, e)
			return
		}
		id := r.PathValue("botId")
		var e error
		if request.SearchParentRepositories != nil {
			e = s.Store.Put("source-control-settings", id, sourceControlSettings{*request.SearchParentRepositories})
		}
		if e == nil && request.P4 != nil {
			e = s.P4.Configure(id, *request.P4)
		}
		if e != nil {
			reply(w, nil, e)
			return
		}
		repos, e := s.SourceControl.Repositories(r.Context(), id)
		reply(w, map[string]any{"repositories": repos}, e)
	})
	handle("POST", "/initialize", func(w http.ResponseWriter, r *http.Request) {
		var req gitservice.InitRequest
		e := editorDecode(w, r, &req, 64<<10)
		if e == nil {
			_, e = s.Git.Initialize(r.Context(), r.PathValue("botId"), req)
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	handle("POST", "/clone", func(w http.ResponseWriter, r *http.Request) {
		var req gitservice.CloneRequest
		e := editorDecode(w, r, &req, 64<<10)
		if e == nil {
			_, e = s.Git.Clone(r.Context(), r.PathValue("botId"), req)
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	handle("POST", "/p4/credential", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Server, User, Secret string }
		e := editorDecode(w, r, &req, 64<<10)
		if e != nil {
			reply(w, nil, e)
			return
		}
		if req.Server == "" || req.User == "" || len(req.Secret) > 4096 || strings.ContainsAny(req.Server+req.User+req.Secret, "\r\n\x00") {
			reply(w, nil, errors.New("valid server, user, and single-line credential required"))
			return
		}
		key := p4CredentialKey(req.Server, req.User)
		var old struct {
			Credential string `json:"credential"`
		}
		_ = s.Store.Get("p4-connections", key, &old)
		ref := ""
		if req.Secret != "" {
			ref = "Echo2/" + s.Dir + "/p4/" + domain.ID()
			e = s.Secrets.Set(r.Context(), ref, req.Secret)
		}
		if e == nil {
			e = s.Store.Put("p4-connections", key, map[string]string{"credential": ref})
		}
		if e != nil && ref != "" {
			_ = s.Secrets.Delete(r.Context(), ref)
		}
		if e == nil && old.Credential != "" {
			_ = s.Secrets.Delete(r.Context(), old.Credential)
		}
		reply(w, map[string]bool{"saved": ref != ""}, e)
	})
}
