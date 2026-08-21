package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/lspconfig"
	"github.com/brent/echo/internal/workspaces"
)

type Status struct {
	WorkspaceID  string          `json:"workspaceId"`
	ProfileID    string          `json:"profileId"`
	Name         string          `json:"name"`
	State        string          `json:"state"`
	Message      string          `json:"message,omitempty"`
	Stderr       string          `json:"stderr,omitempty"`
	RestartCount int             `json:"restartCount,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type serverRuntime struct {
	service   *Service
	workspace workspaces.Workspace
	profile   lspconfig.Profile

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu           sync.Mutex
	peer         *rpcPeer
	command      *exec.Cmd
	state        string
	message      string
	stderr       []byte
	restarts     int
	capabilities json.RawMessage
	intentional  bool
}

func newServerRuntime(service *Service, workspace workspaces.Workspace, profile lspconfig.Profile) *serverRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &serverRuntime{
		service: service, workspace: workspace, profile: profile.Clone(),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), state: "stopped",
	}
}

func (r *serverRuntime) start() {
	go r.run()
}

func (r *serverRuntime) run() {
	defer close(r.done)
	backoff := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	for attempt := 0; ; attempt++ {
		if r.ctx.Err() != nil {
			r.setStatus("stopped", "")
			return
		}
		r.setStatus("starting", "")
		startedAt, err := r.launch()
		if err == nil {
			err = r.wait()
		}
		r.mu.Lock()
		intentional := r.intentional || r.ctx.Err() != nil
		r.peer = nil
		r.command = nil
		r.capabilities = nil
		r.mu.Unlock()
		if intentional {
			r.setStatus("stopped", "")
			return
		}
		if !startedAt.IsZero() && time.Since(startedAt) >= time.Minute {
			attempt = 0
		}
		r.mu.Lock()
		r.restarts++
		r.mu.Unlock()
		if attempt >= len(backoff)-1 {
			r.setStatus("failed", errorMessage(err))
			return
		}
		r.setStatus("restarting", errorMessage(err))
		select {
		case <-time.After(backoff[attempt]):
		case <-r.ctx.Done():
			r.setStatus("stopped", "")
			return
		}
	}
}

func (r *serverRuntime) launch() (time.Time, error) {
	command := exec.CommandContext(r.ctx, r.profile.Command, r.profile.Args...)
	command.Dir = r.workspace.MainPath
	command.Env = processEnvironment(r.profile.Environment)
	stdin, err := command.StdinPipe()
	if err != nil {
		return time.Time{}, fmt.Errorf("open stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return time.Time{}, fmt.Errorf("open stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return time.Time{}, fmt.Errorf("open stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return time.Time{}, fmt.Errorf("start %s: %w", r.profile.Command, err)
	}
	peer := newRPCPeer(stdout, stdin, &pipeCloser{stdin: stdin, stdout: stdout}, r.handleRequest, r.handleNotification)
	r.mu.Lock()
	r.peer = peer
	r.command = command
	r.stderr = nil
	r.mu.Unlock()
	go r.captureStderr(stderr)

	initializeContext, cancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer cancel()
	result, err := peer.Call(initializeContext, "initialize", r.initializeParams())
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = peer.Close()
		return time.Time{}, fmt.Errorf("initialize %s: %w", r.profile.Name, err)
	}
	var initialized struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = peer.Close()
		return time.Time{}, fmt.Errorf("decode initialize response: %w", err)
	}
	if err := peer.Notify("initialized", map[string]any{}); err != nil {
		return time.Time{}, err
	}
	if len(r.profile.Settings) > 0 {
		_ = peer.Notify("workspace/didChangeConfiguration", map[string]any{"settings": r.profile.Settings})
	}
	r.mu.Lock()
	r.capabilities = append(json.RawMessage(nil), initialized.Capabilities...)
	r.mu.Unlock()
	r.setStatus("running", "")
	return time.Now(), nil
}

func (r *serverRuntime) wait() error {
	r.mu.Lock()
	command := r.command
	peer := r.peer
	r.mu.Unlock()
	if command == nil {
		return errors.New("language server process is unavailable")
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	var err error
	if peer == nil {
		err = <-processDone
	} else {
		select {
		case err = <-processDone:
		case <-peer.Done():
			transportErr := peer.Err()
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			err = <-processDone
			if transportErr != nil {
				err = fmt.Errorf("language server transport failed: %w", transportErr)
			}
		}
		_ = peer.Close()
	}
	if err == nil {
		return errors.New("language server exited")
	}
	return err
}

func (r *serverRuntime) stop(ctx context.Context) {
	r.mu.Lock()
	if r.intentional {
		r.mu.Unlock()
		select {
		case <-r.done:
		case <-ctx.Done():
		}
		return
	}
	r.intentional = true
	peer := r.peer
	command := r.command
	r.mu.Unlock()
	r.setStatus("stopping", "")
	if peer != nil {
		shutdownContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, _ = peer.Call(shutdownContext, "shutdown", nil)
		cancel()
		_ = peer.Notify("exit", nil)
	}
	r.cancel()
	if command != nil && command.Process != nil {
		select {
		case <-r.done:
		case <-time.After(500 * time.Millisecond):
			_ = command.Process.Kill()
		case <-ctx.Done():
			_ = command.Process.Kill()
		}
	}
	select {
	case <-r.done:
	case <-ctx.Done():
	}
}

func (r *serverRuntime) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	peer := r.peer
	state := r.state
	r.mu.Unlock()
	if peer == nil || state != "running" {
		return nil, fmt.Errorf("language server %q is not running", r.profile.Name)
	}
	return peer.Call(ctx, method, params)
}

func (r *serverRuntime) notify(method string, params any) error {
	r.mu.Lock()
	peer := r.peer
	state := r.state
	r.mu.Unlock()
	if peer == nil || state != "running" {
		return fmt.Errorf("language server %q is not running", r.profile.Name)
	}
	return peer.Notify(method, params)
}

func (r *serverRuntime) updateSettings(settings map[string]any) {
	r.mu.Lock()
	r.profile.Settings = cloneMap(settings)
	peer := r.peer
	state := r.state
	r.mu.Unlock()
	if peer != nil && state == "running" {
		_ = peer.Notify("workspace/didChangeConfiguration", map[string]any{"settings": settings})
	}
}

func (r *serverRuntime) updateName(name string) {
	r.mu.Lock()
	r.profile.Name = name
	r.mu.Unlock()
}

func (r *serverRuntime) status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		WorkspaceID: r.workspace.ID, ProfileID: r.profile.ID, Name: r.profile.Name,
		State: r.state, Message: r.message, Stderr: string(r.stderr), RestartCount: r.restarts,
		Capabilities: append(json.RawMessage(nil), r.capabilities...),
	}
}

func (r *serverRuntime) setStatus(state, message string) {
	r.mu.Lock()
	r.state = state
	r.message = message
	r.mu.Unlock()
	r.service.runtimeStatusChanged(r)
}

func (r *serverRuntime) captureStderr(reader io.ReadCloser) {
	defer reader.Close()
	buffer := make([]byte, 4096)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			r.mu.Lock()
			r.stderr = append(r.stderr, buffer[:count]...)
			if len(r.stderr) > 64<<10 {
				r.stderr = append([]byte(nil), r.stderr[len(r.stderr)-(64<<10):]...)
			}
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (r *serverRuntime) initializeParams() map[string]any {
	folders := workspaceFolders(r.workspace)
	rootURI := fileURI(r.workspace.MainPath)
	return map[string]any{
		"processId":             os.Getpid(),
		"clientInfo":            map[string]any{"name": "Echo", "version": "0.1.0"},
		"locale":                "en-US",
		"rootUri":               rootURI,
		"workspaceFolders":      folders,
		"initializationOptions": r.profile.InitializationOptions,
		"capabilities":          clientCapabilities(),
		"trace":                 "off",
	}
}

func (r *serverRuntime) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "workspace/configuration":
		var request struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
		result := make([]any, len(request.Items))
		for index, item := range request.Items {
			result[index] = settingSection(r.profile.Settings, item.Section)
		}
		return result, nil
	case "workspace/workspaceFolders":
		return workspaceFolders(r.workspace), nil
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		return nil, nil
	case "workspace/applyEdit":
		result, err := r.service.forwardServerRequest(ctx, r, method, params)
		if err != nil {
			return map[string]any{"applied": false, "failureReason": err.Error()}, nil
		}
		return result, nil
	default:
		return nil, &RPCError{Code: -32601, Message: "method not supported by Echo"}
	}
}

func (r *serverRuntime) handleNotification(method string, params json.RawMessage) {
	r.service.runtimeNotification(r, method, params)
}

func clientCapabilities() map[string]any {
	return map[string]any{
		"general": map[string]any{"positionEncodings": []string{"utf-16"}},
		"workspace": map[string]any{
			"applyEdit": true, "configuration": true,
			"workspaceFolders": true, "workspaceEdit": map[string]any{"documentChanges": true},
			"symbol": map[string]any{"dynamicRegistration": false},
		},
		"window": map[string]any{"workDoneProgress": true, "showDocument": map[string]any{"support": false}},
		"textDocument": map[string]any{
			"synchronization": map[string]any{"dynamicRegistration": false, "willSave": false, "didSave": true},
			"completion": map[string]any{"dynamicRegistration": false, "completionItem": map[string]any{
				"snippetSupport": true, "documentationFormat": []string{"markdown", "plaintext"},
				"resolveSupport": map[string]any{"properties": []string{"documentation", "detail", "additionalTextEdits"}},
			}},
			"hover":              map[string]any{"dynamicRegistration": false, "contentFormat": []string{"markdown", "plaintext"}},
			"signatureHelp":      map[string]any{"dynamicRegistration": false},
			"declaration":        map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"definition":         map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"typeDefinition":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"implementation":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"references":         map[string]any{"dynamicRegistration": false},
			"documentSymbol":     map[string]any{"dynamicRegistration": false, "hierarchicalDocumentSymbolSupport": true},
			"rename":             map[string]any{"dynamicRegistration": false, "prepareSupport": true},
			"codeAction":         map[string]any{"dynamicRegistration": false, "resolveSupport": map[string]any{"properties": []string{"edit", "command"}}},
			"formatting":         map[string]any{"dynamicRegistration": false},
			"rangeFormatting":    map[string]any{"dynamicRegistration": false},
			"publishDiagnostics": map[string]any{"relatedInformation": true, "versionSupport": true},
		},
	}
}

func workspaceFolders(workspace workspaces.Workspace) []map[string]any {
	paths := append([]string(nil), workspace.Folders...)
	if len(paths) == 0 && workspace.MainPath != "" {
		paths = []string{workspace.MainPath}
	}
	result := make([]map[string]any, 0, len(paths))
	usedNames := map[string]int{}
	for _, folder := range paths {
		info, err := os.Stat(folder)
		if err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(folder)
		if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
			name = "workspace"
		}
		usedNames[name]++
		if usedNames[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, usedNames[name])
		}
		result = append(result, map[string]any{"uri": fileURI(folder), "name": name})
	}
	return result
}

func fileURI(path string) string {
	path = filepath.Clean(path)
	slash := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	return (&url.URL{Scheme: "file", Path: slash}).String()
}

func settingSection(settings map[string]any, section string) any {
	if strings.TrimSpace(section) == "" {
		return cloneMap(settings)
	}
	var current any = settings
	for _, part := range strings.Split(section, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[part]
		if !ok {
			return nil
		}
	}
	return current
}

func processEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	keys := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		identity := key
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(identity)
		}
		keys[identity] = key
		values[identity] = value
	}
	for key, value := range overrides {
		identity := key
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(identity)
		}
		keys[identity] = key
		values[identity] = value
	}
	identities := make([]string, 0, len(values))
	for identity := range values {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	result := make([]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, keys[identity]+"="+values[identity])
	}
	return result
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	data, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func errorMessage(err error) string {
	if err == nil {
		return "language server exited"
	}
	return err.Error()
}

func runtimeFingerprint(profile lspconfig.Profile, workspace workspaces.Workspace) string {
	profile = profile.Clone()
	profile.Name = ""
	profile.Settings = nil
	payload := struct {
		Profile lspconfig.Profile `json:"profile"`
		Main    string            `json:"main"`
		Folders []string          `json:"folders"`
	}{profile, workspace.MainPath, append([]string(nil), workspace.Folders...)}
	data, _ := json.Marshal(payload)
	return string(data)
}

func sameSettings(left, right map[string]any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

type pipeCloser struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (p *pipeCloser) Close() error {
	first := p.stdin.Close()
	if second := p.stdout.Close(); first == nil {
		first = second
	}
	return first
}
