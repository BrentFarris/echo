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
	"github.com/brent/echo/internal/sandbox"
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
	Sandbox      bool            `json:"sandbox,omitempty"`
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
	command      lspProcess
	mapper       *sandbox.PathMapper
	state        string
	message      string
	stderr       []byte
	restarts     int
	capabilities json.RawMessage
	intentional  bool
}

type lspProcess interface {
	Wait() (int, error)
	Kill() error
}

type localLSPProcess struct{ command *exec.Cmd }

func (p *localLSPProcess) Wait() (int, error) {
	err := p.command.Wait()
	if p.command.ProcessState != nil {
		return p.command.ProcessState.ExitCode(), err
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
func (p *localLSPProcess) Kill() error {
	if p.command.Process == nil {
		return nil
	}
	return p.command.Process.Kill()
}

func newServerRuntime(service *Service, workspace workspaces.Workspace, profile lspconfig.Profile) *serverRuntime {
	return newServerRuntimeWithSandbox(service, workspace, profile, service.sandboxManager(workspace.ID))
}

func newServerRuntimeWithSandbox(service *Service, workspace workspaces.Workspace, profile lspconfig.Profile, manager *sandbox.Manager) *serverRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &serverRuntime{
		service: service, workspace: workspace, profile: profile.Clone(),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), state: "stopped",
	}
	if manager != nil {
		if mapper, err := manager.PathMapper(workspace.ID); err == nil {
			runtime.mapper = &mapper
		}
	}
	return runtime
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
	var command lspProcess
	var stdin io.WriteCloser
	var stdout, stderr io.ReadCloser
	if manager := r.service.sandboxManager(r.workspace.ID); manager != nil {
		guestDirectory, err := manager.HostToGuest(r.workspace.ID, r.workspace.MainPath)
		if err != nil {
			return time.Time{}, err
		}
		commandName, commandArgs, err := r.sandboxCommand(manager)
		if err != nil {
			return time.Time{}, err
		}
		process, err := manager.OpenProcess(r.ctx, r.workspace.ID, sandbox.ExecRequest{
			Command: append([]string{commandName}, commandArgs...), WorkingDirectory: guestDirectory,
			Environment: sandboxProcessEnvironment(r.profile.Environment),
		})
		if err != nil {
			return time.Time{}, fmt.Errorf("start %s in sandbox: %w", r.profile.Command, err)
		}
		command, stdin, stdout, stderr = process, process.Stdin(), process.Stdout(), process.Stderr()
	} else {
		localCommand := exec.CommandContext(r.ctx, r.profile.Command, r.profile.Args...)
		localCommand.Dir = r.workspace.MainPath
		localCommand.Env = processEnvironment(r.profile.Environment)
		var err error
		stdin, err = localCommand.StdinPipe()
		if err != nil {
			return time.Time{}, fmt.Errorf("open stdin: %w", err)
		}
		stdout, err = localCommand.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return time.Time{}, fmt.Errorf("open stdout: %w", err)
		}
		stderr, err = localCommand.StderrPipe()
		if err != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			return time.Time{}, fmt.Errorf("open stderr: %w", err)
		}
		if err := localCommand.Start(); err != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			_ = stderr.Close()
			return time.Time{}, fmt.Errorf("start %s: %w", r.profile.Command, err)
		}
		command = &localLSPProcess{command: localCommand}
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
		_ = command.Kill()
		_, _ = command.Wait()
		_ = peer.Close()
		return time.Time{}, fmt.Errorf("initialize %s: %w", r.profile.Name, err)
	}
	var initialized struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil {
		_ = command.Kill()
		_, _ = command.Wait()
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
	go func() {
		code, waitErr := command.Wait()
		if waitErr == nil && code != 0 {
			waitErr = fmt.Errorf("language server exited with code %d", code)
		}
		processDone <- waitErr
	}()
	var err error
	if peer == nil {
		err = <-processDone
	} else {
		select {
		case err = <-processDone:
		case <-peer.Done():
			transportErr := peer.Err()
			_ = command.Kill()
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
	if command != nil {
		select {
		case <-r.done:
		case <-time.After(500 * time.Millisecond):
			_ = command.Kill()
		case <-ctx.Done():
			_ = command.Kill()
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
	translated, err := r.translateJSON(params, true)
	if err != nil {
		return nil, fmt.Errorf("translate LSP request paths: %w", err)
	}
	result, err := peer.Call(ctx, method, translated)
	if err != nil {
		return nil, err
	}
	return r.translateJSON(result, false)
}

func (r *serverRuntime) notify(method string, params any) error {
	r.mu.Lock()
	peer := r.peer
	state := r.state
	r.mu.Unlock()
	if peer == nil || state != "running" {
		return fmt.Errorf("language server %q is not running", r.profile.Name)
	}
	translated, err := r.translateValue(params, true)
	if err != nil {
		return fmt.Errorf("translate LSP notification paths: %w", err)
	}
	return peer.Notify(method, translated)
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
		Capabilities: append(json.RawMessage(nil), r.capabilities...), Sandbox: r.mapper != nil,
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
	params := map[string]any{
		"processId":             os.Getpid(),
		"clientInfo":            map[string]any{"name": "Echo", "version": "0.1.0"},
		"locale":                "en-US",
		"rootUri":               rootURI,
		"workspaceFolders":      folders,
		"initializationOptions": r.profile.InitializationOptions,
		"capabilities":          clientCapabilities(),
		"trace":                 "off",
	}
	if r.mapper != nil {
		params["processId"] = nil
		if translated, err := r.translateValue(params, true); err == nil {
			if object, ok := translated.(map[string]any); ok {
				return object
			}
		}
	}
	return params
}

func (r *serverRuntime) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	if translated, err := r.translateJSON(params, false); err == nil {
		params = translated
	} else {
		return nil, &RPCError{Code: -32602, Message: err.Error()}
	}
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
		folders := any(workspaceFolders(r.workspace))
		if translated, err := r.translateValue(folders, true); err == nil {
			folders = translated
		}
		return folders, nil
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		return nil, nil
	case "workspace/applyEdit", "workspace/codeLens/refresh":
		result, err := r.service.forwardServerRequest(ctx, r, method, params)
		if err != nil {
			if method == "workspace/codeLens/refresh" {
				return nil, &RPCError{Code: -32603, Message: err.Error()}
			}
			return map[string]any{"applied": false, "failureReason": err.Error()}, nil
		}
		translated, translateErr := r.translateValue(result, true)
		if translateErr != nil {
			return nil, &RPCError{Code: -32603, Message: translateErr.Error()}
		}
		return translated, nil
	default:
		return nil, &RPCError{Code: -32601, Message: "method not supported by Echo"}
	}
}

func (r *serverRuntime) handleNotification(method string, params json.RawMessage) {
	if translated, err := r.translateJSON(params, false); err == nil {
		params = translated
	} else {
		return
	}
	r.service.runtimeNotification(r, method, params)
}

func (r *serverRuntime) translateJSON(data json.RawMessage, hostToGuest bool) (json.RawMessage, error) {
	if r.mapper == nil {
		return data, nil
	}
	return r.mapper.TranslateJSON(data, hostToGuest)
}

func (r *serverRuntime) translateValue(value any, hostToGuest bool) (any, error) {
	if r.mapper == nil {
		return value, nil
	}
	return r.mapper.TranslateValue(value, hostToGuest)
}

func (r *serverRuntime) sandboxCommand(manager *sandbox.Manager) (string, []string, error) {
	command := strings.TrimSpace(r.profile.Command)
	if filepath.IsAbs(command) {
		mapped, err := manager.HostToGuest(r.workspace.ID, command)
		if err != nil {
			return "", nil, fmt.Errorf("language server executable is outside registered workspace roots")
		}
		command = mapped
	}
	args := append([]string(nil), r.profile.Args...)
	for index, argument := range args {
		if strings.HasPrefix(strings.ToLower(argument), "file:") {
			mapper, err := manager.PathMapper(r.workspace.ID)
			if err != nil {
				return "", nil, err
			}
			mapped, err := mapper.HostURIToGuest(argument)
			if err != nil {
				return "", nil, err
			}
			args[index] = mapped
		} else if filepath.IsAbs(argument) {
			mapped, err := manager.HostToGuest(r.workspace.ID, argument)
			if err != nil {
				return "", nil, fmt.Errorf("language server argument path is outside registered workspace roots")
			}
			args[index] = mapped
		}
	}
	return command, args, nil
}

func clientCapabilities() map[string]any {
	return map[string]any{
		"general": map[string]any{"positionEncodings": []string{"utf-16"}},
		"workspace": map[string]any{
			"applyEdit": true, "configuration": true,
			"workspaceFolders": true, "workspaceEdit": map[string]any{"documentChanges": true},
			"symbol":   map[string]any{"dynamicRegistration": false},
			"codeLens": map[string]any{"refreshSupport": true},
		},
		"window": map[string]any{"workDoneProgress": true, "showDocument": map[string]any{"support": false}},
		"textDocument": map[string]any{
			"synchronization": map[string]any{"dynamicRegistration": false, "willSave": false, "didSave": true},
			"completion": map[string]any{"dynamicRegistration": false, "completionItem": map[string]any{
				"snippetSupport": true, "documentationFormat": []string{"markdown", "plaintext"},
				"resolveSupport": map[string]any{"properties": []string{"documentation", "detail", "additionalTextEdits"}},
			}},
			"hover":          map[string]any{"dynamicRegistration": false, "contentFormat": []string{"markdown", "plaintext"}},
			"signatureHelp":  map[string]any{"dynamicRegistration": false},
			"declaration":    map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"definition":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"typeDefinition": map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"implementation": map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"references":     map[string]any{"dynamicRegistration": false},
			"documentSymbol": map[string]any{"dynamicRegistration": false, "hierarchicalDocumentSymbolSupport": true},
			"rename":         map[string]any{"dynamicRegistration": false, "prepareSupport": true},
			"codeLens": map[string]any{
				"dynamicRegistration": false,
				"resolveSupport":      map[string]any{"properties": []string{"command"}},
			},
			"codeAction": map[string]any{
				"dynamicRegistration": false,
				"isPreferredSupport":  true,
				"disabledSupport":     true,
				"dataSupport":         true,
				"resolveSupport":      map[string]any{"properties": []string{"edit", "command"}},
				"codeActionLiteralSupport": map[string]any{"codeActionKind": map[string]any{"valueSet": []string{
					"", "quickfix", "refactor", "refactor.extract", "refactor.inline", "refactor.rewrite",
					"source", "source.organizeImports", "source.fixAll",
				}}},
			},
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

func sandboxProcessEnvironment(overrides map[string]string) []string {
	values := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/home/echo/go/bin",
		"HOME": "/home/echo", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "ECHO_SANDBOX": "1",
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
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
