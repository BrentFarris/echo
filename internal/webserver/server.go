package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/services"
)

const (
	rpcPrefix       = "/api/rpc/SystemService/"
	eventsRoute     = "/api/events"
	maxRPCBodyBytes = 64 << 20
)

type Server struct {
	service *services.SystemService
	assets  fs.FS

	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	address    string
	status     services.WebAccessStatus
}

func New(service *services.SystemService, assets fs.FS) *Server {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err == nil {
		assets = dist
	}
	return &Server{
		service: service,
		assets:  assets,
		status:  services.WebAccessStatus{LANURLs: []string{}},
	}
}

func (s *Server) ApplyWebAccessSettings(settings services.WebAccessSettings) (services.WebAccessStatus, error) {
	if !settings.Enabled {
		s.stopCurrent(context.Background())
		status := statusFromSettings(settings, false, "")
		s.setStatus(status)
		return status, nil
	}

	address := net.JoinHostPort(settings.BindHost, strconv.Itoa(settings.Port))
	s.mu.Lock()
	if s.httpServer != nil && s.address == address {
		status := statusFromSettings(settings, true, "")
		s.status = status
		s.mu.Unlock()
		return status, nil
	}
	s.mu.Unlock()

	listener, err := net.Listen("tcp", address)
	if err != nil {
		status := statusFromSettings(settings, false, err.Error())
		s.mu.Lock()
		if s.httpServer != nil {
			status.Running = true
		}
		s.status = status
		s.mu.Unlock()
		return status, fmt.Errorf("start web access server: %w", err)
	}

	server := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.mu.Lock()
	oldServer := s.httpServer
	oldListener := s.listener
	s.httpServer = server
	s.listener = listener
	s.address = address
	status := statusFromSettings(settings, true, "")
	s.status = status
	s.mu.Unlock()

	go func() {
		var err error
		if settings.EnableTLS {
			configDir, dirErr := services.WebAccessConfigDir()
			if dirErr != nil {
				s.setLastError("TLS config dir: " + dirErr.Error())
				return
			}
			certPath := filepath.Join(configDir, "echo-cert.pem")
			keyPath := filepath.Join(configDir, "echo-key.pem")

			if _, statErr := os.Stat(certPath); os.IsNotExist(statErr) {
				if genErr := services.GenerateSelfSignedCert(certPath, keyPath); genErr != nil {
					s.setLastError("TLS cert generation: " + genErr.Error())
					return
				}
			}

			err = server.ServeTLS(listener, certPath, keyPath)
		} else {
			err = server.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.setLastError(err.Error())
		}
	}()

	if oldServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = oldServer.Shutdown(ctx)
		cancel()
	}
	if oldListener != nil {
		_ = oldListener.Close()
	}
	return status, nil
}

func (s *Server) LoadWebAccessStatus(settings services.WebAccessSettings) services.WebAccessStatus {
	s.mu.Lock()
	status := s.status
	running := s.httpServer != nil
	s.mu.Unlock()
	if status.BindHost == "" || status.Port == 0 || status.AccessToken != settings.AccessToken || status.Enabled != settings.Enabled {
		status = statusFromSettings(settings, running, status.LastError)
	}
	status.AccessToken = settings.AccessToken
	return status
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.stopCurrent(ctx)
}

func (s *Server) stopCurrent(ctx context.Context) error {
	s.mu.Lock()
	server := s.httpServer
	listener := s.listener
	s.httpServer = nil
	s.listener = nil
	s.address = ""
	s.mu.Unlock()

	if server == nil {
		if listener != nil {
			_ = listener.Close()
		}
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := server.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
	}
	return err
}

func (s *Server) setStatus(status services.WebAccessStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *Server) setLastError(message string) {
	s.mu.Lock()
	s.status.LastError = message
	s.status.Running = false
	s.httpServer = nil
	s.listener = nil
	s.address = ""
	s.mu.Unlock()
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(eventsRoute, s.handleEvents)
	mux.HandleFunc(rpcPrefix, s.handleRPC)
	mux.Handle("/", s.service.WorkspaceIconMiddleware(s.staticHandler()))
	return mux
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, rpcPrefix)
	if method == "" || strings.Contains(method, "/") || !allowedRPCMethods[method] {
		writeRPCError(w, http.StatusNotFound, "method is not available")
		return
	}

	var payload struct {
		Args []json.RawMessage `json:"args"`
	}
	body := http.MaxBytesReader(w, r.Body, maxRPCBodyBytes)
	defer body.Close()
	if err := json.NewDecoder(body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeRPCError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := callServiceMethod(s.service, method, payload.Args)
	if err != nil {
		writeRPCError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeRPCResult(w, result)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := requestToken(r)
	if !services.WebAccessTokenAllowed(s.service, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, unsubscribe := services.SubscribeEvents(s.service, 256)
	defer unsubscribe()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok || !services.WebAccessTokenAllowed(s.service, token) {
				return
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if !services.WebAccessTokenAllowed(s.service, token) {
				return
			}
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) authorized(r *http.Request) bool {
	return services.WebAccessTokenAllowed(s.service, requestToken(r))
}

func requestToken(r *http.Request) string {
	if header := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[len("bearer "):])
	}
	if header := strings.TrimSpace(r.Header.Get("X-Echo-Access-Token")); header != "" {
		return header
	}
	return strings.TrimSpace(r.URL.Query().Get("access_token"))
}

func writeSSE(w io.Writer, event services.RuntimeEvent) error {
	data, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func callServiceMethod(service *services.SystemService, method string, args []json.RawMessage) (any, error) {
	value := reflect.ValueOf(service).MethodByName(method)
	if !value.IsValid() {
		return nil, fmt.Errorf("method is not available")
	}
	methodType := value.Type()
	if methodType.NumIn() != len(args) {
		return nil, fmt.Errorf("method %s expects %d arguments, got %d", method, methodType.NumIn(), len(args))
	}
	values := make([]reflect.Value, methodType.NumIn())
	for i := range values {
		arg := reflect.New(methodType.In(i))
		if err := json.Unmarshal(args[i], arg.Interface()); err != nil {
			return nil, fmt.Errorf("argument %d is invalid: %w", i+1, err)
		}
		values[i] = arg.Elem()
	}

	output := value.Call(values)
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if len(output) > 0 && output[len(output)-1].Type().Implements(errorType) {
		errValue := output[len(output)-1]
		output = output[:len(output)-1]
		if !errValue.IsNil() {
			return nil, errValue.Interface().(error)
		}
	}
	switch len(output) {
	case 0:
		return nil, nil
	case 1:
		return output[0].Interface(), nil
	default:
		values := make([]any, len(output))
		for i := range output {
			values[i] = output[i].Interface()
		}
		return values, nil
	}
}

func writeRPCResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Result any `json:"result"`
	}{Result: result})
}

func writeRPCError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}{
		Error: struct {
			Message string `json:"message"`
		}{Message: message},
	})
}

func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		requestPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if requestPath == "" {
			requestPath = "index.html"
		}
		if s.serveStaticFile(w, r, requestPath) {
			return
		}
		s.serveStaticFile(w, r, "index.html")
	})
}

func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request, name string) bool {
	file, err := s.assets.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		return false
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, name, stat.ModTime(), bytes.NewReader(data))
	return true
}

func statusFromSettings(settings services.WebAccessSettings, running bool, lastError string) services.WebAccessStatus {
	urls := publicURLs(settings)
	primary := ""
	if len(urls) > 0 {
		primary = urls[0]
	}
	return services.WebAccessStatus{
		Enabled:     settings.Enabled,
		Running:     running,
		BindHost:    settings.BindHost,
		Port:        settings.Port,
		AccessToken: settings.AccessToken,
		PrimaryURL:  primary,
		LANURLs:     urls,
		EnableTLS:   settings.EnableTLS,
		LastError:   lastError,
	}
}

func publicURLs(settings services.WebAccessSettings) []string {
	hosts := publicHosts(settings.BindHost)
	output := make([]string, 0, len(hosts))
	token := url.QueryEscape(settings.AccessToken)
	scheme := "http"
	if settings.EnableTLS {
		scheme = "https"
	}
	for _, host := range hosts {
		hostPort := net.JoinHostPort(host, strconv.Itoa(settings.Port))
		output = append(output, scheme+"://"+hostPort+"/#token="+token)
	}
	return output
}

func publicHosts(bindHost string) []string {
	host := strings.TrimSpace(strings.ToLower(bindHost))
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		hosts := lanIPv4Hosts()
		if len(hosts) == 0 {
			return []string{"localhost"}
		}
		return hosts
	case "127.0.0.1", "::1", "localhost":
		return []string{"localhost"}
	default:
		return []string{bindHost}
	}
}

func lanIPv4Hosts() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	hosts := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil && !ipv4.IsLoopback() {
				hosts = append(hosts, ipv4.String())
			}
		}
	}
	sort.Strings(hosts)
	return hosts
}

var allowedRPCMethods = map[string]bool{
	"AddKanbanCardMessage":                    true,
	"AddWorkspace":                            true,
	"AddWorkspaceFolder":                      true,
	"AppInfo":                                 true,
	"ClearChat":                               true,
	"ClearDoneKanbanCards":                    true,
	"ClearWorkspaceChangeReview":              true,
	"ClearWorkspaceIcon":                      true,
	"CloneWorkspaceGitRepository":             true,
	"CloseKanbanCardDetail":                   true,
	"CommitWorkspaceGitChanges":               true,
	"CompleteWorkspaceFile":                   true,
	"CreateAgentMode":                         true,
	"CreateAgentModeFromChat":                 true,
	"CreateAgentModePerTool":                  true,
	"CreateKanbanCardFromChatMessage":         true,
	"CreateKanbanCardFromTask":                true,
	"CreateReadyKanbanCard":                   true,
	"CreateSkillFromChat":                     true,
	"CreateWorkspaceGitBranch":                true,
	"CreateWorkspaceFile":                     true,
	"CreateWorkspaceFolder":                   true,
	"CreateWorkspaceTask":                     true,
	"DeleteAgentMode":                         true,
	"DeleteKanbanCard":                        true,
	"DeleteWorkspacePaths":                    true,
	"DeleteWorkspaceTask":                     true,
	"DeleteWorkspace":                         true,
	"EditChatMessage":                         true,
	"ExecutePlan":                             true,
	"FindWorkspaceFileDefinition":             true,
	"FindWorkspaceFileImplementations":        true,
	"FindWorkspaceFileReferences":             true,
	"GetDashboardLayouts":                     true,
	"GetHeartbeatConfig":                      true,
	"GetLivenessConfig":                       true,
	"GetTokenBudget":                          true,
	"GetWatchdogConfig":                       true,
	"ListAgentModes":                          true,
	"ListWorkspaceDirectory":                  true,
	"LoadChatSession":                         true,
	"LoadKanbanBoard":                         true,
	"LoadRuntimeStatus":                       true,
	"LoadState":                               true,
	"LoadTaskBoard":                           true,
	"LoadWebAccessStatus":                     true,
	"LoadWorkspaceChangeReview":               true,
	"LoadWorkspaceGitChanges":                 true,
	"LoadWorkspaceGitCommit":                  true,
	"LoadWorkspaceGitFileDiff":                true,
	"LoadWorkspaceGitRepository":              true,
	"LoadWorkspaceGitStash":                   true,
	"MergeWorkspaceGitBranch":                 true,
	"MoveKanbanCard":                          true,
	"MoveWorkspacePath":                       true,
	"MoveWorkspaceTask":                       true,
	"OpenKanbanCardDetail":                    true,
	"OpenWorkspaceExplorer":                   true,
	"OpenWorkspacePathExplorer":               true,
	"PrepareRebuildAndRelaunch":               true,
	"PrepareWorkspaceSymbolRename":            true,
	"PruneChatMessage":                        true,
	"ReadWorkspaceFile":                       true,
	"ReadWorkspaceMediaFile":                  true,
	"RemoveWorkspaceFolder":                   true,
	"RenameWorkspacePath":                     true,
	"RenameWorkspaceSymbol":                   true,
	"ReorderWorkspaces":                       true,
	"ResetKanbanCard":                         true,
	"ResetTokenBudget":                        true,
	"ResolveWorkspaceTextFilePath":            true,
	"RetryChatMessage":                        true,
	"RotateWebAccessToken":                    true,
	"SaveDashboardLayout":                     true,
	"RunWorkspaceGitAction":                   true,
	"SaveSettings":                            true,
	"SaveWebAccessSettings":                   true,
	"SaveWorkspaceFile":                       true,
	"SaveWorkspaceFileAs":                     true,
	"SearchWorkspaceFiles":                    true,
	"SendChatMessage":                         true,
	"SendChatMessageWithAttachments":          true,
	"SendChatMessageWithPlanMode":             true,
	"SetActiveWorkspace":                      true,
	"SetLivenessConfig":                       true,
	"SetTokenBudget":                          true,
	"SetWorkspaceBuildCommand":                true,
	"SetWorkspaceDefaultPlanMode":             true,
	"SetWorkspaceFolderUseAgents":             true,
	"SetWorkspaceIconFromPath":                true,
	"SetWorkspaceIconFromUpload":              true,
	"SetWorkspaceLetter":                      true,
	"SetWorkspaceSearchParentGitRepositories": true,
	"SetWorkspaceTaskCompleted":               true,
	"StartHeartbeat":                          true,
	"StartKanbanExecution":                    true,
	"StageWorkspaceGitChanges":                true,
	"StageWorkspaceGitFile":                   true,
	"StopChatStream":                          true,
	"StopHeartbeat":                           true,
	"StopKanbanCard":                          true,
	"StopKanbanExecution":                     true,
	"StopWatchdog":                            true,
	"SubmitInlineCodePrompt":                  true,
	"SwitchWorkspaceGitBranch":                true,
	"SyncLSPDocument":                         true,
	"SyncWorkspaceGitBranch":                  true,
	"UnstageWorkspaceGitChanges":              true,
	"UnstageWorkspaceGitFile":                 true,
	"UpdateAgentMode":                         true,
	"UpdateAgentModePerTool":                  true,
	"UpdateKanbanCardDescription":             true,
	"UpdateKanbanCardDirection":               true,
	"UpdateWorkspaceTask":                     true,
	"LoadWorkspaceDebugSettings":              true,
	"SaveWorkspaceDebugSettings":              true,
	"SetWorkspaceSelectedDebugConfiguration":  true,
	"LoadDebugState":                          true,
	"StartDebugSession":                       true,
	"ContinueDebugSession":                    true,
	"PauseDebugSession":                       true,
	"StepOverDebugSession":                    true,
	"StepIntoDebugSession":                    true,
	"StepOutDebugSession":                     true,
	"StopDebugSession":                        true,
	"SetDebugBreakpoints":                     true,
	"LoadDebugThreads":                        true,
	"LoadDebugStackTrace":                     true,
	"LoadDebugScopes":                         true,
	"LoadDebugVariables":                      true,
	"EvaluateDebugExpression":                 true,
}
