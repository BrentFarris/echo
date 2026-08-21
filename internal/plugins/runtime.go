package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRPCMessageBytes  = 8 << 20
	maxRPCResultBytes   = 4 << 20
	runtimeStartTimeout = 10 * time.Second
	runtimeStopTimeout  = 2 * time.Second
)

type RuntimeEvent struct {
	PluginID  string `json:"pluginId"`
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
}

type RuntimeOptions struct {
	RootDir string
	LogDir  string
	Events  func(RuntimeEvent)
	Redact  func(pluginID, value string) string
}

type RuntimeManager struct {
	mu        sync.Mutex
	options   RuntimeOptions
	processes map[string]*pluginProcess
	failures  map[string][]time.Time
}

func NewRuntimeManager(options RuntimeOptions) *RuntimeManager {
	return &RuntimeManager{options: options, processes: map[string]*pluginProcess{}, failures: map[string][]time.Time{}}
}

func (m *RuntimeManager) Ensure(ctx context.Context, installed InstalledPlugin) (*pluginProcess, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if installed.Manifest.Runtime == nil {
		return nil, fmt.Errorf("plugin has no backend runtime")
	}
	m.mu.Lock()
	if process := m.processes[installed.Manifest.ID]; process != nil && process.Alive() && process.digest == installed.Digest {
		m.mu.Unlock()
		return process, nil
	}
	if process := m.processes[installed.Manifest.ID]; process != nil {
		delete(m.processes, installed.Manifest.ID)
		m.mu.Unlock()
		_ = process.Stop(context.Background())
		// Re-enter through the normal lookup so a concurrent caller that
		// started the replacement while Stop was in flight is reused instead
		// of spawning a second backend for the same plugin.
		return m.Ensure(ctx, installed)
	}
	if m.unhealthyLocked(installed.Manifest.ID) {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin runtime is unhealthy; reload it to try again")
	}
	if retryAfter := m.retryAfterLocked(installed.Manifest.ID); retryAfter > 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin runtime restart backoff is active for %s", retryAfter.Round(10*time.Millisecond))
	}
	process, err := startPluginProcess(ctx, installed, m.options, func(process *pluginProcess, waitErr error) {
		m.processExited(process, waitErr)
	})
	if err != nil {
		m.recordFailureLocked(installed.Manifest.ID)
		unhealthy := m.unhealthyLocked(installed.Manifest.ID)
		m.mu.Unlock()
		if unhealthy && m.options.Events != nil {
			m.options.Events(RuntimeEvent{PluginID: installed.Manifest.ID, Type: "runtime_unhealthy", Error: err.Error()})
		}
		return nil, err
	}
	m.processes[installed.Manifest.ID] = process
	m.mu.Unlock()
	return process, nil
}

func (m *RuntimeManager) Call(ctx context.Context, installed InstalledPlugin, method string, params any, timeout time.Duration) (any, error) {
	process, err := m.Ensure(ctx, installed)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := process.Call(callCtx, method, params)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRPCResultBytes {
		return nil, fmt.Errorf("plugin result exceeds %d bytes", maxRPCResultBytes)
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode plugin result: %w", err)
	}
	return normalizeJSONNumbers(result), nil
}

func (m *RuntimeManager) NotifyConfigChanged(pluginID, workspaceID string) error {
	m.mu.Lock()
	process := m.processes[pluginID]
	m.mu.Unlock()
	if process == nil || !process.Alive() {
		return nil
	}
	return process.Notify("echo.configChanged", map[string]any{"scope": scopeKind(workspaceID), "workspaceId": workspaceID})
}

func (m *RuntimeManager) Stop(pluginID string) error {
	m.mu.Lock()
	process := m.processes[pluginID]
	delete(m.processes, pluginID)
	delete(m.failures, pluginID)
	m.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.Stop(context.Background())
}

func (m *RuntimeManager) Unhealthy(pluginID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unhealthyLocked(pluginID)
}

func (m *RuntimeManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	processes := make([]*pluginProcess, 0, len(m.processes))
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	m.processes = map[string]*pluginProcess{}
	m.mu.Unlock()
	var first error
	for _, process := range processes {
		if err := process.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *RuntimeManager) processExited(process *pluginProcess, waitErr error) {
	m.mu.Lock()
	if m.processes[process.pluginID] == process {
		delete(m.processes, process.pluginID)
		if !process.stopping.Load() {
			m.recordFailureLocked(process.pluginID)
		}
	}
	unhealthy := m.unhealthyLocked(process.pluginID)
	m.mu.Unlock()
	if m.options.Events != nil && !process.stopping.Load() {
		message := "plugin runtime exited"
		if waitErr != nil {
			message = waitErr.Error()
		}
		kind := "runtime_exited"
		if unhealthy {
			kind = "runtime_unhealthy"
		}
		m.options.Events(RuntimeEvent{PluginID: process.pluginID, Type: kind, Error: message})
	}
}

func (m *RuntimeManager) recordFailureLocked(pluginID string) {
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	recent := m.failures[pluginID][:0]
	for _, failure := range m.failures[pluginID] {
		if failure.After(cutoff) {
			recent = append(recent, failure)
		}
	}
	m.failures[pluginID] = append(recent, now)
}

func (m *RuntimeManager) unhealthyLocked(pluginID string) bool { return len(m.failures[pluginID]) >= 3 }

func (m *RuntimeManager) retryAfterLocked(pluginID string) time.Duration {
	failures := m.failures[pluginID]
	if len(failures) == 0 {
		return 0
	}
	shift := len(failures) - 1
	if shift > 5 {
		shift = 5
	}
	delay := 100 * time.Millisecond * time.Duration(1<<shift)
	remaining := time.Until(failures[len(failures)-1].Add(delay))
	if remaining < 0 {
		return 0
	}
	return remaining
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type pluginProcess struct {
	pluginID  string
	digest    string
	command   *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[uint64]chan rpcResponse
	nextID    atomic.Uint64
	done      chan struct{}
	alive     atomic.Bool
	stopping  atomic.Bool
	events    func(RuntimeEvent)
	onExit    func(*pluginProcess, error)
}

func startPluginProcess(ctx context.Context, installed InstalledPlugin, options RuntimeOptions, onExit func(*pluginProcess, error)) (*pluginProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actualDigest, err := HashPackage(installed.PackagePath)
	if err != nil {
		return nil, fmt.Errorf("verify plugin runtime snapshot: %w", err)
	}
	if !validDigest(installed.Digest) || actualDigest != installed.Digest {
		return nil, fmt.Errorf("plugin runtime snapshot no longer matches its approved digest")
	}
	targetKey := runtime.GOOS + "-" + runtime.GOARCH
	target, ok := installed.Manifest.Runtime.Targets[targetKey]
	if !ok {
		return nil, fmt.Errorf("plugin has no runtime target for %s", targetKey)
	}
	executable, err := packagePath(installed.PackagePath, target.Path)
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(options.RootDir, installed.Manifest.ID)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin data directory: %w", err)
	}
	if err := os.MkdirAll(options.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("create plugin log directory: %w", err)
	}
	logPath := filepath.Join(options.LogDir, installed.Manifest.ID+".log")
	logFile, err := newRotatingLog(logPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin log: %w", err)
	}
	command := exec.Command(executable, target.Args...)
	command.Dir = dataDir
	command.Env = minimalPluginEnvironment(installed)
	pluginLog := &pluginLogWriter{pluginID: installed.Manifest.ID, target: logFile, redact: options.Redact}
	command.Stderr = pluginLog
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = pluginLog.Close()
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = pluginLog.Close()
		return nil, err
	}
	process := &pluginProcess{
		pluginID: installed.Manifest.ID, digest: installed.Digest, command: command, stdin: stdin,
		pending: map[uint64]chan rpcResponse{}, done: make(chan struct{}), events: options.Events, onExit: onExit,
	}
	if err := ctx.Err(); err != nil {
		_ = stdin.Close()
		_ = pluginLog.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = pluginLog.Close()
		return nil, fmt.Errorf("start plugin runtime: %w", err)
	}
	process.alive.Store(true)
	go process.readLoop(stdout)
	go func() {
		err := command.Wait()
		process.alive.Store(false)
		close(process.done)
		process.failPending(fmt.Errorf("plugin runtime exited"))
		_ = pluginLog.Close()
		if process.onExit != nil {
			process.onExit(process, err)
		}
	}()
	startCtx, cancel := context.WithTimeout(ctx, runtimeStartTimeout)
	defer cancel()
	var response struct {
		Protocol string `json:"protocol"`
	}
	raw, err := process.Call(startCtx, "echo.initialize", map[string]any{
		"protocol":     RPCProtocol,
		"echo":         map[string]any{"api": HostAPIMajor, "version": "0.1.0"},
		"plugin":       map[string]any{"id": installed.Manifest.ID, "version": installed.Manifest.Version, "digest": installed.Digest},
		"capabilities": []string{"tool.invoke", "ui.invoke", "ui.events", "cancellation"},
	})
	if err != nil {
		_ = process.Stop(context.Background())
		return nil, fmt.Errorf("initialize plugin runtime: %w", err)
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Protocol != RPCProtocol {
		_ = process.Stop(context.Background())
		return nil, fmt.Errorf("plugin runtime returned an incompatible handshake")
	}
	return process, nil
}

func (p *pluginProcess) Alive() bool { return p.alive.Load() }

func (p *pluginProcess) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !p.Alive() {
		return nil, fmt.Errorf("plugin runtime is not running")
	}
	id := p.nextID.Add(1)
	response := make(chan rpcResponse, 1)
	p.pendingMu.Lock()
	p.pending[id] = response
	p.pendingMu.Unlock()
	if err := p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		p.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		p.removePending(id)
		_ = p.Notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	case <-p.done:
		p.removePending(id)
		return nil, fmt.Errorf("plugin runtime exited")
	case result := <-response:
		return result.result, result.err
	}
}

func (p *pluginProcess) Notify(method string, params any) error {
	if !p.Alive() {
		return fmt.Errorf("plugin runtime is not running")
	}
	return p.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *pluginProcess) Stop(ctx context.Context) error {
	if !p.Alive() {
		return nil
	}
	p.stopping.Store(true)
	p.cancelPending()
	stopCtx, cancel := context.WithTimeout(ctx, runtimeStopTimeout)
	defer cancel()
	_, _ = p.Call(stopCtx, "echo.shutdown", map[string]any{})
	_ = p.stdin.Close()
	select {
	case <-p.done:
		return nil
	case <-stopCtx.Done():
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
		return nil
	}
}

func (p *pluginProcess) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxRPCMessageBytes {
		return fmt.Errorf("plugin RPC message exceeds %d bytes", maxRPCMessageBytes)
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write plugin RPC: %w", err)
	}
	return nil
}

func (p *pluginProcess) readLoop(reader io.Reader) {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(line)+len(fragment) > maxRPCMessageBytes {
			p.failPending(fmt.Errorf("plugin RPC message exceeds limit"))
			if p.command.Process != nil {
				_ = p.command.Process.Kill()
			}
			return
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 && !p.handleLine(bytes.TrimSpace(line)) {
			p.failPending(fmt.Errorf("plugin wrote a malformed RPC message"))
			if p.command.Process != nil {
				_ = p.command.Process.Kill()
			}
			return
		}
		if err != nil {
			return
		}
		line = line[:0]
	}
}

func (p *pluginProcess) handleLine(line []byte) bool {
	var message rpcMessage
	if err := json.Unmarshal(line, &message); err != nil || message.JSONRPC != "2.0" {
		return false
	}
	if len(message.ID) > 0 {
		id, err := strconv.ParseUint(strings.Trim(string(message.ID), `"`), 10, 64)
		if err != nil {
			return false
		}
		p.pendingMu.Lock()
		response := p.pending[id]
		delete(p.pending, id)
		p.pendingMu.Unlock()
		if response == nil {
			return true
		}
		if message.Error != nil {
			response <- rpcResponse{err: fmt.Errorf("plugin RPC error %d: %s", message.Error.Code, message.Error.Message)}
		} else {
			response <- rpcResponse{result: append(json.RawMessage(nil), message.Result...)}
		}
		return true
	}
	if (message.Method == "echo.uiEvent" || message.Method == "echo.ui.event") && p.events != nil {
		var event struct {
			SessionID string `json:"sessionId"`
			Topic     string `json:"topic"`
			Data      any    `json:"data"`
		}
		if json.Unmarshal(message.Params, &event) == nil && event.SessionID != "" && event.Topic != "" {
			p.events(RuntimeEvent{PluginID: p.pluginID, Type: "ui_event", SessionID: event.SessionID, Topic: event.Topic, Data: event.Data})
		}
	}
	return true
}

func (p *pluginProcess) removePending(id uint64) {
	p.pendingMu.Lock()
	delete(p.pending, id)
	p.pendingMu.Unlock()
}

func (p *pluginProcess) failPending(err error) {
	p.pendingMu.Lock()
	pending := p.pending
	p.pending = map[uint64]chan rpcResponse{}
	p.pendingMu.Unlock()
	for _, response := range pending {
		response <- rpcResponse{err: err}
	}
}

func (p *pluginProcess) cancelPending() {
	p.pendingMu.Lock()
	ids := make([]uint64, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	p.pendingMu.Unlock()
	for _, id := range ids {
		_ = p.Notify("$/cancelRequest", map[string]any{"id": id})
	}
}

func minimalPluginEnvironment(installed InstalledPlugin) []string {
	allowed := []string{"PATH", "SystemRoot", "WINDIR", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL"}
	environment := make([]string, 0, len(allowed)+3)
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	environment = append(environment,
		"ECHO_PLUGIN_ID="+installed.Manifest.ID,
		"ECHO_PLUGIN_VERSION="+installed.Manifest.Version,
		"ECHO_PLUGIN_PROTOCOL="+RPCProtocol,
	)
	return environment
}

type rotatingLog struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

type pluginLogWriter struct {
	mu       sync.Mutex
	pluginID string
	target   *rotatingLog
	redact   func(string, string) string
	buffer   []byte
}

func (w *pluginLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(data)
	w.buffer = append(w.buffer, data...)
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := append([]byte(nil), w.buffer[:index+1]...)
		w.buffer = w.buffer[index+1:]
		if err := w.writeRedacted(line); err != nil {
			return 0, err
		}
	}
	if len(w.buffer) > 128<<10 {
		line := append([]byte(nil), w.buffer...)
		w.buffer = w.buffer[:0]
		if err := w.writeRedacted(line); err != nil {
			return 0, err
		}
	}
	return original, nil
}

func (w *pluginLogWriter) writeRedacted(data []byte) error {
	value := string(data)
	if w.redact != nil {
		value = w.redact(w.pluginID, value)
	}
	_, err := w.target.Write([]byte(value))
	return err
}

func (w *pluginLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) > 0 {
		if err := w.writeRedacted(w.buffer); err != nil {
			return err
		}
		w.buffer = nil
	}
	return w.target.Close()
}

func newRotatingLog(path string) (*rotatingLog, error) {
	log := &rotatingLog{path: path}
	if info, err := os.Stat(path); err == nil {
		log.size = info.Size()
	}
	if log.size >= 2<<20 {
		if err := log.rotateLocked(); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	log.file = file
	return log, nil
}

func (l *rotatingLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	original := len(data)
	if len(data) > 2<<20 {
		data = data[len(data)-(2<<20):]
	}
	if l.size+int64(len(data)) > 2<<20 {
		if err := l.rotateLocked(); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		l.file = file
	}
	written, err := l.file.Write(data)
	l.size += int64(written)
	if err == nil && written == len(data) {
		return original, nil
	}
	return written, err
}

func (l *rotatingLog) rotateLocked() error {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	backup := l.path + ".1"
	_ = os.Remove(backup)
	if err := os.Rename(l.path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l.size = 0
	return nil
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

type InvocationScope struct {
	Kind        string         `json:"kind"`
	WorkspaceID string         `json:"workspaceId,omitempty"`
	Workspace   map[string]any `json:"workspace,omitempty"`
}

func (m *Manager) InvokeTool(ctx context.Context, pluginID, toolName, workspaceID string, arguments json.RawMessage, workspace map[string]any) (any, error) {
	callCtx, finish, err := m.beginPluginCall(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx = callCtx
	if !m.IsToolAllowed(pluginID, toolName, workspaceID) {
		return nil, fmt.Errorf("plugin tool is not enabled or approved for this workspace")
	}
	installed, ok, err := m.Installed(pluginID)
	if err != nil || !ok {
		return nil, fmt.Errorf("plugin was not found")
	}
	tool, ok := installed.Manifest.Tool(toolName)
	if !ok {
		return nil, fmt.Errorf("plugin tool was not found")
	}
	validated, err := DecodeAndValidateArguments(tool.InputSchema, arguments)
	if err != nil {
		return nil, fmt.Errorf("%s", m.redactText(pluginID, err.Error()))
	}
	config, err := m.ResolvedConfig(ctx, installed, workspaceID)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(tool.TimeoutSeconds) * time.Second
	deadline := invocationDeadline(ctx, timeout)
	workspace = permittedToolWorkspaceMetadata(installed, workspace)
	result, err := m.runtimes.Call(ctx, installed, tool.Method, map[string]any{
		"invocationId": randomID("invoke-"), "arguments": validated,
		"scope":  InvocationScope{Kind: scopeKind(workspaceID), WorkspaceID: workspaceID, Workspace: workspace},
		"config": config, "deadline": deadline,
	}, timeout)
	if err != nil {
		return nil, fmt.Errorf("%s", m.redactText(pluginID, err.Error()))
	}
	if len(tool.OutputSchema) > 0 {
		if err := ValidateJSONSchema(tool.OutputSchema, result); err != nil {
			return nil, fmt.Errorf("plugin returned an invalid result: %w", err)
		}
	}
	return m.redactValue(pluginID, result), nil
}

func (m *Manager) InvokeUI(ctx context.Context, pluginID, method, workspaceID, sessionID string, params any) (any, error) {
	callCtx, finish, err := m.beginPluginCall(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx = callCtx
	if !m.IsEnabled(pluginID, workspaceID) {
		return nil, fmt.Errorf("plugin is not enabled")
	}
	installed, ok, err := m.Installed(pluginID)
	if err != nil || !ok {
		return nil, fmt.Errorf("plugin was not found")
	}
	contribution, ok := installed.Manifest.RPCMethods()[method]
	if !ok {
		return nil, fmt.Errorf("plugin RPC method is not declared")
	}
	config, err := m.ResolvedConfig(ctx, installed, workspaceID)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(contribution.TimeoutSeconds) * time.Second
	deadline := invocationDeadline(ctx, timeout)
	result, err := m.runtimes.Call(ctx, installed, method, map[string]any{
		"invocationId": randomID("invoke-"), "sessionId": sessionID, "params": params,
		"scope": InvocationScope{Kind: scopeKind(workspaceID), WorkspaceID: workspaceID, Workspace: m.uiWorkspaceMetadata(installed, workspaceID)}, "config": config, "deadline": deadline,
	}, timeout)
	if err != nil {
		return nil, fmt.Errorf("%s", m.redactText(pluginID, err.Error()))
	}
	return m.redactValue(pluginID, result), nil
}

func permittedToolWorkspaceMetadata(installed InstalledPlugin, input map[string]any) map[string]any {
	result := map[string]any{}
	if id, ok := input["id"].(string); ok {
		result["id"] = id
	}
	includePaths := containsString(installed.ApprovedPermissions, "filesystem")
	if roots, ok := input["roots"].([]map[string]string); ok {
		filtered := make([]map[string]string, 0, len(roots))
		for _, root := range roots {
			entry := map[string]string{"id": root["id"], "label": root["label"]}
			if includePaths {
				entry["path"] = root["path"]
			}
			filtered = append(filtered, entry)
		}
		result["roots"] = filtered
	}
	return result
}

func (m *Manager) uiWorkspaceMetadata(installed InstalledPlugin, workspaceID string) map[string]any {
	if workspaceID == "" {
		return nil
	}
	result := map[string]any{"id": workspaceID}
	if containsString(installed.ApprovedPermissions, "filesystem") && m.workspacePath != nil {
		if path, err := m.workspacePath(workspaceID); err == nil {
			result["path"] = path
		}
	}
	return result
}

func invocationDeadline(ctx context.Context, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	if existing, ok := ctx.Deadline(); ok && existing.Before(deadline) {
		deadline = existing
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}

func scopeKind(workspaceID string) string {
	if workspaceID == "" {
		return "global"
	}
	return "workspace"
}
