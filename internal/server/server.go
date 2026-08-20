// Package server implements the browser-based Echo web server. It serves the
// single-page application frontend from a static directory and exposes JSON
// API endpoints plus a WebSocket endpoint for real-time, server-to-client push.
package server

import (
	"context"
	"fmt"
	iofs "io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/auth"
	"github.com/brent/echo/internal/echoupdate"
	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/rebuild"
	"github.com/brent/echo/internal/settings"
	terminalruntime "github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
	"github.com/brent/echo/internal/workspaceskills"
	"github.com/google/uuid"
)

// Server is the Echo HTTP server. It serves the SPA frontend from a static
// directory, hosts JSON API endpoints under /api, and runs a WebSocket hub for
// real-time events.
type Server struct {
	httpServer       *http.Server
	webDir           string
	webAssets        iofs.FS
	hub              *Hub
	settingsPath     string
	plugins          *plugins.Manager
	tools            *tools.Registry
	data             *appdata.Store
	store            *settings.Store
	workspaces       *workspaces.Manager
	fs               *workspacefs.Service
	watcher          *workspacefs.WatchManager
	git              *gitservice.Service
	terminal         *terminalruntime.Service
	auth             *auth.Manager
	authDisabled     bool
	loginLimiter     *loginRateLimiter
	modes            *agentmodes.Manager
	sessions         *chatSessionManager
	llm              chatStreamer
	llmCompleter     chatCompleter
	llmSettings      llm.Settings
	researchLLM      chatStreamer
	researchSettings llm.Settings
	researchSeparate bool
	visionLLM        chatStreamer
	visionSettings   llm.Settings
	visionSeparate   bool
	skillsMu         sync.Mutex
	skills           map[string]*workspaceskills.Service
	rebuilder        rebuildCoordinator
	updateChecker    echoUpdateChecker
	restartCh        chan struct{}
	instanceID       string
	processID        int
	processArgs      []string
	workingDir       string
	// settings holds the full normalized settings (all endpoints) so the chat
	// handler can resolve a user-selected model to its owning endpoint.
	settings llm.Settings
}

// chatStreamer is the subset of the LLM client the chat endpoint needs. It is
// an interface so tests can inject a fake streamer.
type chatStreamer interface {
	StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream
}

type chatCompleter interface {
	Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error)
}

// New constructs a Server bound to addr that serves frontend assets from
// webDir. It does not start listening; call ListenAndServe.
func New(addr, webDir string) *Server {
	return NewWithSettingsPath(addr, webDir, "")
}

// NewWithSettingsPath constructs a Server like New but with an explicit path
// to the settings file. When settingsPath is empty, the platform default
// (echo/echo.json under the user config dir) is used.
func NewWithSettingsPath(addr, webDir, settingsPath string) *Server {
	return newServer(addr, webDir, nil, settingsPath, ServerOptions{})
}

// NewWithAssets constructs a Server that serves an embedded production SPA.
// The supplied filesystem's root must contain index.html.
func NewWithAssets(addr string, assets iofs.FS, settingsPath string) *Server {
	return newServer(addr, "", assets, settingsPath, ServerOptions{})
}

type ServerOptions struct {
	SafeMode bool
}

func NewWithSettingsPathOptions(addr, webDir, settingsPath string, options ServerOptions) *Server {
	return newServer(addr, webDir, nil, settingsPath, options)
}

func NewWithAssetsOptions(addr string, assets iofs.FS, settingsPath string, options ServerOptions) *Server {
	return newServer(addr, "", assets, settingsPath, options)
}

func newServer(addr, webDir string, assets iofs.FS, settingsPath string, options ServerOptions) *Server {
	workingDir, _ := os.Getwd()
	s := &Server{
		webDir:        webDir,
		webAssets:     assets,
		hub:           NewHub(),
		skills:        make(map[string]*workspaceskills.Service),
		rebuilder:     rebuild.NewCoordinator(),
		updateChecker: echoupdate.NewChecker(),
		restartCh:     make(chan struct{}, 1),
		instanceID:    uuid.NewString(),
		processID:     os.Getpid(),
		processArgs:   append([]string(nil), os.Args[1:]...),
		workingDir:    workingDir,
		tools:         tools.CloneDefaultRegistry(),
	}
	if settingsPath == "" {
		path, err := settings.DefaultStorePath()
		if err != nil {
			logf("resolve settings path: %v", err)
			path = "echo.json"
		}
		settingsPath = path
	}
	s.settingsPath = settingsPath
	s.data = appdata.NewStore(settingsPath)
	s.store = settings.NewStoreWithData(s.data)
	s.workspaces = workspaces.NewManagerWithData(s.data)
	s.terminal = terminalruntime.New(s.workspaces, s.data)
	s.terminal.SetNotifier(func(event terminalruntime.Event) {
		s.hub.BroadcastWorkspaceTerminal(event.WorkspaceID, event)
	})
	s.fs = workspacefs.New(s.workspaces, settingsPath)
	s.git = gitservice.New(s.workspaces, s.fs)
	s.git.SetNotifier(func(event gitservice.Event) {
		s.hub.BroadcastWorkspaceGit(event.WorkspaceID, event)
	})
	s.watcher = workspacefs.NewWatchManager(s.fs, func(event workspacefs.WatchEvent) {
		s.hub.BroadcastWorkspaceFS(event.WorkspaceID, event)
		s.git.InvalidateWorkspace(event.WorkspaceID)
	})
	coreToolNames := map[string]bool{}
	for _, tool := range s.tools.Registered() {
		coreToolNames[tool.Metadata().Name] = true
	}
	pluginManager, pluginErr := plugins.NewManager(plugins.Options{
		RootDir: filepath.Join(filepath.Dir(settingsPath), "plugins"), CoreToolNames: coreToolNames,
		SafeMode: options.SafeMode, Builtins: plugins.BuiltinPackages(),
		WorkspacePath: func(workspaceID string) (string, error) {
			workspace, ok, err := s.workspaces.Get(workspaceID)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("workspace %q was not found", workspaceID)
			}
			return workspace.MainPath, nil
		},
		WorkspaceIDs: func() []string {
			workspaces, err := s.workspaces.List()
			if err != nil {
				return nil
			}
			ids := make([]string, 0, len(workspaces))
			for _, workspace := range workspaces {
				ids = append(ids, workspace.ID)
			}
			return ids
		},
		Notify: func() { s.hub.Broadcast(map[string]any{"type": "plugins_changed"}) },
		RuntimeEvent: func(event plugins.RuntimeEvent) {
			s.hub.Broadcast(map[string]any{"type": "plugin_runtime_event", "event": event})
		},
	})
	if pluginErr != nil {
		logf("initialize plugins: %v", pluginErr)
	} else {
		s.plugins = pluginManager
		if err := s.plugins.BindTools(s.tools); err != nil {
			logf("register plugin tools: %v", err)
		}
	}
	authManager, authErr := auth.New(s.data)
	s.auth = authManager
	if authErr != nil {
		log.Printf("initialize authentication: %v", authErr)
	}
	s.loginLimiter = newLoginRateLimiter()
	s.modes = agentmodes.NewManager()
	s.sessions = newChatSessionManager(s)
	s.initLLM()
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	return s
}

// initLLM builds the LLM client used by the chat endpoint. It loads settings
// from the store (falling back to defaults) and selects the chat endpoint.
func (s *Server) initLLM() {
	cfg, err := s.store.Load()
	if err != nil {
		logf("load settings: %v", err)
		cfg = llm.DefaultSettings()
	}
	cfg = cfg.NormalizedEndpointProfiles()
	s.settings = cfg
	s.llmSettings = cfg.ForInteraction(llm.InteractionChat)
	s.researchSettings = cfg.ForInteraction(llm.InteractionResearch)
	s.visionSettings = cfg.ForInteraction(llm.InteractionVision)
	s.llm = nil
	s.llmCompleter = nil
	s.researchLLM = nil
	s.visionLLM = nil
	s.researchSeparate = cfg.EndpointSelection.Research != cfg.EndpointSelection.Chat
	s.visionSeparate = cfg.EndpointSelection.Vision != cfg.EndpointSelection.Chat
	client, err := llm.NewClient(s.llmSettings)
	if err != nil {
		logf("init llm client: %v", err)
		return
	}
	s.llm = client
	s.llmCompleter = client
	if !s.researchSeparate {
		s.researchLLM = client
	} else {
		researchClient, researchErr := llm.NewClient(s.researchSettings)
		if researchErr != nil {
			logf("init research llm client: %v", researchErr)
		} else {
			s.researchLLM = researchClient
		}
	}
	if !s.visionSeparate {
		s.visionLLM = client
		return
	}
	visionClient, visionErr := llm.NewClient(s.visionSettings)
	if visionErr != nil {
		logf("init vision llm client: %v", visionErr)
		return
	}
	s.visionLLM = visionClient
}

func (s *Server) researchChat() (llm.Settings, chatStreamer) {
	if !s.researchSeparate {
		return s.researchSettings, s.llm
	}
	return s.researchSettings, s.researchLLM
}

// settingsForModel returns the settings for the endpoint that owns the given
// model, if one is configured. It is used to route a chat prompt to a
// user-selected model from the landing page. If no endpoint declares the model,
// ok is false and the caller should fall back to the default chat endpoint.
func (s *Server) settingsForModel(model string) (llm.Settings, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return llm.Settings{}, false
	}
	for _, endpoint := range s.settings.Endpoints {
		if strings.TrimSpace(endpoint.Model) == model {
			return endpoint.ApplyToSettings(s.settings), true
		}
	}
	return llm.Settings{}, false
}

// routes builds the HTTP handler tree for the server.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// JSON API endpoints.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("GET /api/auth/sessions", s.handleAuthSessions)
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", s.handleAuthRevokeSession)
	mux.HandleFunc("PUT /api/auth/password", s.handleAuthChangePassword)
	mux.HandleFunc("GET /api/echo", s.handleEcho)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/development/update-status", s.handleEchoUpdateStatus)
	mux.HandleFunc("POST /api/development/update", s.handleEchoUpdate)
	mux.HandleFunc("POST /api/development/rebuild-relaunch", s.handleRebuildRelaunch)
	mux.HandleFunc("GET /api/agent-modes", s.handleGetAgentModes)
	mux.HandleFunc("POST /api/agent-modes", s.handleCreateAgentMode)
	mux.HandleFunc("PUT /api/agent-modes/{id}", s.handleUpdateAgentMode)
	mux.HandleFunc("DELETE /api/agent-modes/{id}", s.handleDeleteAgentMode)
	mux.HandleFunc("GET /api/plugins", s.handlePluginCatalog)
	mux.HandleFunc("POST /api/plugins/stages", s.handlePluginStage)
	mux.HandleFunc("POST /api/plugins/stages/{stageId}/approve", s.handlePluginApprove)
	mux.HandleFunc("DELETE /api/plugins/stages/{stageId}", s.handlePluginReject)
	mux.HandleFunc("POST /api/plugins/{id}/actions", s.handlePluginAction)
	mux.HandleFunc("PUT /api/plugins/{id}/config", s.handlePluginConfig)
	mux.HandleFunc("GET /api/plugins/{id}/logs", s.handlePluginLog)
	mux.HandleFunc("GET /api/plugins/{id}/icon/{viewId}", s.handlePluginIcon)
	mux.HandleFunc("POST /api/plugins/{id}/views/{viewId}/sessions", s.handlePluginUISession)
	mux.HandleFunc("POST /api/plugins/ui-sessions/{token}/bridge", s.handlePluginUIBridge)
	mux.HandleFunc("DELETE /api/plugins/ui-sessions/{token}", s.handlePluginUIClose)
	mux.HandleFunc("POST /api/plugins/{id}/remove-data", s.handlePluginRemoveData)

	// Workspace endpoints.
	mux.HandleFunc("GET /api/workspaces", s.handleGetWorkspaces)
	mux.HandleFunc("POST /api/workspaces", s.handleCreateWorkspace)
	mux.HandleFunc("PUT /api/workspaces/active", s.handleSetActiveWorkspace)
	mux.HandleFunc("GET /api/workspaces/{id}/icon", s.handleGetWorkspaceIcon)
	mux.HandleFunc("GET /api/workspaces/{id}/fs/roots", s.handleFSRoots)
	mux.HandleFunc("GET /api/workspaces/{id}/fs/entries", s.handleFSEntries)
	mux.HandleFunc("POST /api/workspaces/{id}/fs/entries", s.handleFSCreateEntry)
	mux.HandleFunc("GET /api/workspaces/{id}/fs/file", s.handleFSReadFile)
	mux.HandleFunc("PUT /api/workspaces/{id}/fs/file", s.handleFSSaveFile)
	mux.HandleFunc("PATCH /api/workspaces/{id}/fs/entry", s.handleFSRenameEntry)
	mux.HandleFunc("DELETE /api/workspaces/{id}/fs/entry", s.handleFSTrashEntry)
	mux.HandleFunc("GET /api/workspaces/{id}/fs/trash", s.handleFSListTrash)
	mux.HandleFunc("POST /api/workspaces/{id}/fs/trash/{trashId}/restore", s.handleFSRestoreTrash)
	mux.HandleFunc("DELETE /api/workspaces/{id}/fs/trash/{trashId}", s.handleFSPurgeTrash)
	mux.HandleFunc("POST /api/workspaces/{id}/fs/reveal", s.handleFSReveal)
	mux.HandleFunc("GET /api/workspaces/{id}/fs/search", s.handleFSSearch)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories", s.handleGitRepositories)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories/{repositoryId}/status", s.handleGitStatus)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories/{repositoryId}/diff", s.handleGitDiff)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories/{repositoryId}/metadata", s.handleGitMetadata)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories/{repositoryId}/history", s.handleGitHistory)
	mux.HandleFunc("GET /api/workspaces/{id}/git/repositories/{repositoryId}/detail", s.handleGitCommitDetail)
	mux.HandleFunc("POST /api/workspaces/{id}/git/repositories/{repositoryId}/actions", s.handleGitAction)
	mux.HandleFunc("POST /api/workspaces/{id}/git/clone", s.handleGitClone)
	mux.HandleFunc("POST /api/workspaces/{id}/git/initialize", s.handleGitInitialize)
	mux.HandleFunc("PUT /api/workspaces/{id}/git/settings", s.handleGitSettings)
	mux.HandleFunc("POST /api/workspaces/{id}/chats/{chatId}/skills", s.handleCreateSkillFromChat)
	mux.HandleFunc("GET /api/workspaces/{id}/chats/{chatId}/trajectory", s.handleGetTrajectory)
	mux.HandleFunc("GET /api/workspaces/{id}/chats/{chatId}/trajectory/search", s.handleSearchTrajectory)
	mux.HandleFunc("GET /api/workspaces/{id}/chats/{chatId}/trajectory/export", s.handleExportTrajectory)
	mux.HandleFunc("POST /api/workspaces/{id}/terminal/sessions", s.handleStartTerminalSession)
	mux.HandleFunc("GET /api/workspaces/{id}/terminal/sessions/{sessionId}", s.handleSyncTerminalSession)
	mux.HandleFunc("POST /api/workspaces/{id}/terminal/sessions/{sessionId}/input", s.handleWriteTerminalSession)
	mux.HandleFunc("PUT /api/workspaces/{id}/terminal/sessions/{sessionId}/size", s.handleResizeTerminalSession)
	mux.HandleFunc("POST /api/workspaces/{id}/terminal/sessions/{sessionId}/stop", s.handleStopTerminalSession)
	mux.HandleFunc("POST /api/workspaces/{id}/terminal/sessions/{sessionId}/restart", s.handleRestartTerminalSession)
	mux.HandleFunc("GET /api/workspaces/{id}/terminal/saved-commands", s.handleListSavedCommands)
	mux.HandleFunc("POST /api/workspaces/{id}/terminal/saved-commands", s.handleCreateSavedCommand)
	mux.HandleFunc("PUT /api/workspaces/{id}/terminal/saved-commands/{commandId}", s.handleUpdateSavedCommand)
	mux.HandleFunc("DELETE /api/workspaces/{id}/terminal/saved-commands/{commandId}", s.handleDeleteSavedCommand)

	// WebSocket endpoint for real-time push.
	mux.HandleFunc("GET /ws", s.handleWebSocket)
	mux.HandleFunc("GET /plugin-ui/{token}/{path...}", s.handlePluginUIAsset)

	// Static assets from the web directory.
	var fileServer http.Handler
	if s.webAssets != nil {
		fileServer = http.FileServer(http.FS(s.webAssets))
	} else {
		fileServer = http.FileServer(http.Dir(s.webDir))
	}
	mux.Handle("/", fileServer)

	// Wrap the mux with SPA fallback so unknown non-API paths serve index.html,
	// enabling client-side routing.
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never rewrite API or WebSocket paths.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/plugin-ui/") || r.URL.Path == "/ws" {
			mux.ServeHTTP(w, r)
			return
		}
		// Let the file server handle real files; otherwise fall back to index.html.
		if s.isStaticAsset(r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		s.serveIndex(w, r)
	})
	return s.securityHeaders(s.requireAuthentication(spa))
}

// isStaticAsset reports whether path maps to an existing file under webDir.
func (s *Server) isStaticAsset(requestPath string) bool {
	if requestPath == "/" {
		return false
	}
	if s.webAssets != nil {
		clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(requestPath)), "/")
		if clean == "." || !iofs.ValidPath(clean) {
			return false
		}
		info, err := iofs.Stat(s.webAssets, clean)
		return err == nil && !info.IsDir()
	}
	clean := filepath.Clean(filepath.FromSlash(requestPath))
	full := filepath.Join(s.webDir, clean)
	info, err := os.Stat(full)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// serveIndex writes the SPA entry point for the given request.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if s.webAssets != nil {
		index, err := iofs.ReadFile(s.webAssets, "index.html")
		if err != nil {
			http.Error(w, "frontend assets are unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
		return
	}
	index := filepath.Join(s.webDir, "index.html")
	http.ServeFile(w, r, index)
}

// ListenAndServe starts serving HTTP requests on the configured address.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// RestartRequested is consumed by the host lifecycle. A value is sent only
// after a replacement binary and detached launcher are ready.
func (s *Server) RestartRequested() <-chan struct{} {
	return s.restartCh
}

func (s *Server) requestRestart() {
	select {
	case s.restartCh <- struct{}{}:
	default:
	}
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.watcher.Close()
	s.git.Close()
	s.fs.Close()
	s.sessions.shutdown(ctx)
	if err := s.terminal.Shutdown(ctx); err != nil && ctx.Err() == nil {
		logf("terminal shutdown: %v", err)
	}
	if s.plugins != nil {
		if err := s.plugins.Shutdown(ctx); err != nil && ctx.Err() == nil {
			logf("plugin shutdown: %v", err)
		}
	}
	s.hub.Shutdown()
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) refreshWorkspaceCaches(ctx context.Context, workspaceID string) {
	s.sessions.invalidate(workspaceID)
	s.terminal.StopWorkspace(workspaceID)
	s.watcher.Refresh(workspaceID)
	if err := s.git.ResetWorkspace(ctx, workspaceID); err != nil {
		logf("refresh git workspace %s: %v", workspaceID, err)
	}
	s.fs.RefreshWorkspace(workspaceID)
	s.skillsMu.Lock()
	delete(s.skills, workspaceID)
	s.skillsMu.Unlock()
}

// ResetAuthentication clears the configured owner password and remembered
// devices, returning the new one-time setup code.
func (s *Server) ResetAuthentication() (string, error) {
	if s.auth == nil {
		return "", fmt.Errorf("authentication manager is unavailable")
	}
	return s.auth.Reset()
}

// AuthenticationSetupCode returns the memory-only first-run code, if Echo is
// still in setup mode. It is intended for display by the host process only.
func (s *Server) AuthenticationSetupCode() string {
	if s.auth == nil {
		return ""
	}
	return s.auth.SetupCode()
}

// logf is a small logging helper so handlers can log without importing log
// everywhere.
func logf(format string, args ...any) {
	log.Printf(format, args...)
}
