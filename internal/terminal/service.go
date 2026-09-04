// Package terminal owns Echo's shared workspace-scoped interactive terminals.
package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	ptylib "github.com/aymanbagabas/go-pty"
	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

const (
	ReplayBytes = 2 * 1024 * 1024
	MaxInput    = 64 * 1024
	readBytes   = 32 * 1024
	MinCols     = 2
	MaxCols     = 500
	MinRows     = 2
	MaxRows     = 200
)

var (
	ErrWorkspaceNotFound    = errors.New("workspace not found")
	ErrWorkspaceUnavailable = errors.New("workspace has no available folders")
	ErrSessionNotFound      = errors.New("terminal session was not found")
	ErrSessionNotRunning    = errors.New("terminal session is not running")
	ErrExternalUnsupported  = errors.New("external debug terminals are not supported; use an integrated terminal")
	ErrInputTooLarge        = errors.New("terminal input is too large")
	ErrSavedCommandNotFound = errors.New("saved command was not found")
)

type OutputChunk struct {
	Sequence uint64 `json:"sequence"`
	Data     string `json:"data"`
}

type Snapshot struct {
	WorkspaceID      string        `json:"workspaceId"`
	ID               string        `json:"id"`
	Name             string        `json:"name,omitempty"`
	Kind             string        `json:"kind,omitempty"`
	OwnerSessionID   string        `json:"ownerSessionId,omitempty"`
	Shell            string        `json:"shell"`
	WorkingDirectory string        `json:"workingDirectory"`
	Status           string        `json:"status"`
	ExitCode         *int          `json:"exitCode,omitempty"`
	Message          string        `json:"message,omitempty"`
	TaskStatus       string        `json:"taskStatus,omitempty"`
	LastSequence     uint64        `json:"lastSequence"`
	Reset            bool          `json:"reset,omitempty"`
	Output           []OutputChunk `json:"output"`
}

type Event struct {
	Type           string `json:"type"`
	WorkspaceID    string `json:"workspaceId"`
	SessionID      string `json:"sessionId"`
	Name           string `json:"name,omitempty"`
	Kind           string `json:"kind,omitempty"`
	OwnerSessionID string `json:"ownerSessionId,omitempty"`
	Event          string `json:"event"`
	Sequence       uint64 `json:"sequence,omitempty"`
	Data           string `json:"data,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Message        string `json:"message,omitempty"`
	TaskStatus     string `json:"taskStatus,omitempty"`
}

type SavedCommand struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Order   int    `json:"order"`
}

type CommandSpec struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

// TaskRequest is a trusted, server-constructed direct process invocation.
// Browser APIs never accept this shape directly.
type TaskRequest struct {
	Name             string
	Command          string
	Args             []string
	WorkingDirectory string
	Environment      map[string]string
	DisplayCommand   string
}

type Process interface {
	Wait() (int, error)
	Kill() error
}

type Backend interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Start(context.Context, CommandSpec) (Process, error)
}

type realBackend struct{ pty ptylib.Pty }
type realProcess struct{ cmd *ptylib.Cmd }

type sandboxBackend struct {
	manager     *sandbox.Manager
	workspaceID string
	pty         sandbox.PTY
	cols        int
	rows        int
}

type sandboxProcess struct{ pty sandbox.PTY }

func (b *sandboxBackend) Read(buffer []byte) (int, error) {
	if b.pty == nil {
		return 0, io.ErrClosedPipe
	}
	return b.pty.Read(buffer)
}

func (b *sandboxBackend) Write(buffer []byte) (int, error) {
	if b.pty == nil {
		return 0, io.ErrClosedPipe
	}
	return b.pty.Write(buffer)
}

func (b *sandboxBackend) Close() error {
	if b.pty == nil {
		return nil
	}
	return b.pty.Close()
}

func (b *sandboxBackend) Resize(cols, rows int) error {
	b.cols, b.rows = cols, rows
	if b.pty == nil {
		return nil
	}
	return b.pty.Resize(cols, rows)
}

func (b *sandboxBackend) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	command := append([]string{spec.Name}, spec.Args...)
	pty, err := b.manager.OpenPTY(ctx, b.workspaceID, sandbox.ExecRequest{
		Command: command, WorkingDirectory: spec.Dir, Environment: spec.Env,
		TTY: true, Columns: b.cols, Rows: b.rows,
	})
	if err != nil {
		return nil, err
	}
	b.pty = pty
	return &sandboxProcess{pty: pty}, nil
}

func (p *sandboxProcess) Wait() (int, error) { return p.pty.Wait() }
func (p *sandboxProcess) Kill() error        { return p.pty.Kill() }

func newRealBackend() (Backend, error) {
	value, err := ptylib.New()
	if err != nil {
		return nil, err
	}
	return &realBackend{pty: value}, nil
}

func (b *realBackend) Read(buffer []byte) (int, error)  { return b.pty.Read(buffer) }
func (b *realBackend) Write(buffer []byte) (int, error) { return b.pty.Write(buffer) }
func (b *realBackend) Close() error                     { return b.pty.Close() }
func (b *realBackend) Resize(cols, rows int) error      { return b.pty.Resize(cols, rows) }
func (b *realBackend) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	command := b.pty.CommandContext(ctx, spec.Name, spec.Args...)
	configurePTYCommand(command)
	command.Dir = spec.Dir
	command.Env = spec.Env
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &realProcess{cmd: command}, nil
}

func (p *realProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), err
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (p *realProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := killPTYCommand(p.cmd)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
func (p *realProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

type bufferedChunk struct {
	sequence uint64
	data     []byte
}

type session struct {
	workspaceID string
	id          string
	name        string
	kind        string
	ownerID     string
	shell       string
	workingDir  string
	backend     Backend
	process     Process
	cancel      context.CancelFunc

	mu          sync.Mutex
	writeMu     sync.Mutex
	stopOnce    sync.Once
	closeOnce   sync.Once
	done        chan struct{}
	status      string
	exitCode    *int
	message     string
	taskStatus  string
	sequence    uint64
	output      []bufferedChunk
	outputBytes int
}

type workspaceResolver interface {
	Get(id string) (workspaces.Workspace, bool, error)
}

type Service struct {
	workspaces workspaceResolver
	data       *appdata.Store

	mu         sync.Mutex
	sessions   map[string]*session
	defaults   map[string]string
	tasks      map[string]string
	taskMu     sync.Mutex
	newBackend func() (Backend, error)
	sandbox    *sandbox.Manager
	notify     func(Event)
}

func New(workspaceManager workspaceResolver, data *appdata.Store) *Service {
	return &Service{
		workspaces: workspaceManager,
		data:       data,
		sessions:   make(map[string]*session),
		defaults:   make(map[string]string),
		tasks:      make(map[string]string),
		newBackend: newRealBackend,
	}
}

// StartTask starts one server-owned, non-interactive task for a workspace.
// Only the latest task is retained; a new task stops and replaces its predecessor.
func (s *Service) StartTask(workspaceID string, request TaskRequest) (Snapshot, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(request.Command) == "" {
		return Snapshot{}, errors.New("task command is required")
	}
	workingDir := filepath.Clean(strings.TrimSpace(request.WorkingDirectory))
	if workingDir == "." || workingDir == "" {
		workingDir, err = availableWorkingDirectory(workspace)
		if err != nil {
			return Snapshot{}, err
		}
	}

	s.mu.Lock()
	previous := s.sessions[s.tasks[workspaceID]]
	sandboxManager := s.sandbox
	factory := s.newBackend
	s.mu.Unlock()
	if previous != nil {
		previous.stopAndWait()
	}

	sandboxEnabled := workspace.Sandbox.Enabled || (sandboxManager != nil && sandboxManager.IsEnabled(workspaceID))
	if sandboxEnabled && sandboxManager == nil {
		return Snapshot{}, errors.New("sandbox task runtime is unavailable")
	}
	command := request.Command
	if !sandboxEnabled {
		command, err = exec.LookPath(command)
		if err != nil {
			return Snapshot{}, fmt.Errorf("resolve task command %q: %w", request.Command, err)
		}
	}
	if sandboxEnabled {
		workingDir, err = sandboxManager.HostToGuest(workspaceID, workingDir)
		if err != nil {
			return Snapshot{}, fmt.Errorf("map task working directory: %w", err)
		}
	}
	cols, rows := ClampSize(120, 30)
	var backend Backend
	if sandboxEnabled {
		backend = &sandboxBackend{manager: sandboxManager, workspaceID: workspaceID}
	} else {
		backend, err = factory()
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("create task terminal: %w", err)
	}
	if err := backend.Resize(cols, rows); err != nil {
		_ = backend.Close()
		return Snapshot{}, fmt.Errorf("size task terminal: %w", err)
	}
	environment := terminalEnvironment()
	if sandboxEnabled {
		environment = sandboxTerminalEnvironment()
	}
	environment = mergeTaskEnvironment(environment, request.Environment)
	processContext, cancel := context.WithCancel(context.Background())
	process, err := backend.Start(processContext, CommandSpec{
		Name: command, Args: append([]string(nil), request.Args...), Dir: workingDir, Env: environment,
	})
	if err != nil {
		cancel()
		_ = backend.Close()
		return Snapshot{}, fmt.Errorf("start task: %w", err)
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "Task Output"
	}
	current := &session{
		workspaceID: workspaceID, id: uuid.NewString(), name: name, kind: "test",
		shell: filepath.Base(command), workingDir: workingDir, backend: backend, process: process,
		cancel: cancel, done: make(chan struct{}), status: "running", taskStatus: "running", output: make([]bufferedChunk, 0, 64),
	}
	s.mu.Lock()
	if previous != nil {
		delete(s.sessions, previous.id)
	}
	s.sessions[current.id] = current
	s.tasks[workspaceID] = current.id
	s.mu.Unlock()
	s.emit(Event{Type: "terminal_event", WorkspaceID: workspaceID, SessionID: current.id, Name: current.name, Kind: current.kind, Event: "started", TaskStatus: current.taskStatus})
	if display := strings.TrimSpace(request.DisplayCommand); display != "" {
		s.emitOutput(current, []byte("Running tool: "+display+"\r\n\r\n"))
	}
	go s.run(current)
	return current.snapshot(0), nil
}

func (s *Service) SetNotifier(notify func(Event)) {
	s.mu.Lock()
	s.notify = notify
	s.mu.Unlock()
}

func (s *Service) SetSandbox(manager *sandbox.Manager) {
	s.mu.Lock()
	s.sandbox = manager
	s.mu.Unlock()
}

func (s *Service) SetBackendFactory(factory func() (Backend, error)) func() {
	s.mu.Lock()
	previous := s.newBackend
	s.newBackend = factory
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.newBackend = previous
		s.mu.Unlock()
	}
}

func (s *Service) Start(workspaceID string, cols, rows int) (Snapshot, error) {
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	existing := s.sessions[s.defaults[workspaceID]]
	s.mu.Unlock()
	if existing != nil {
		return existing.snapshot(0), nil
	}
	workingDir, err := availableWorkingDirectory(workspace)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	sandboxManager := s.sandbox
	s.mu.Unlock()
	// The portable workspace policy is authoritative. A missing sandbox
	// runtime must never turn a sandbox-owned debuggee into a host process.
	sandboxEnabled := workspace.Sandbox.Enabled || (sandboxManager != nil && sandboxManager.IsEnabled(workspaceID))
	if sandboxEnabled && sandboxManager == nil {
		return Snapshot{}, errors.New("sandbox terminal runtime is unavailable")
	}
	var shellName, shellLabel string
	var shellArgs []string
	if sandboxEnabled {
		workingDir, err = sandboxManager.HostToGuest(workspaceID, workingDir)
		if err != nil {
			return Snapshot{}, fmt.Errorf("map sandbox working directory: %w", err)
		}
		shellName, shellArgs, shellLabel = "/bin/bash", []string{"-l"}, "Sandbox Bash"
	} else {
		shellName, shellArgs, shellLabel, err = resolveInteractiveShell()
		if err != nil {
			return Snapshot{}, err
		}
	}
	cols, rows = ClampSize(cols, rows)

	s.mu.Lock()
	if current := s.sessions[s.defaults[workspaceID]]; current != nil {
		s.mu.Unlock()
		return current.snapshot(0), nil
	}
	factory := s.newBackend
	var backend Backend
	if sandboxEnabled {
		backend = &sandboxBackend{manager: sandboxManager, workspaceID: workspaceID}
	} else {
		backend, err = factory()
	}
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("create terminal: %w", err)
	}
	if err := backend.Resize(cols, rows); err != nil {
		_ = backend.Close()
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("size terminal: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	environment := terminalEnvironment()
	if sandboxEnabled {
		environment = sandboxTerminalEnvironment()
	}
	process, err := backend.Start(ctx, CommandSpec{
		Name: shellName, Args: shellArgs, Dir: workingDir, Env: environment,
	})
	if err != nil {
		cancel()
		_ = backend.Close()
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("start %s: %w", shellLabel, err)
	}
	current := &session{
		workspaceID: workspaceID,
		id:          uuid.NewString(),
		name:        "Terminal",
		kind:        "default",
		shell:       shellLabel,
		workingDir:  workingDir,
		backend:     backend,
		process:     process,
		cancel:      cancel,
		done:        make(chan struct{}),
		status:      "running",
		output:      make([]bufferedChunk, 0, 64),
	}
	s.sessions[current.id] = current
	s.defaults[workspaceID] = current.id
	s.mu.Unlock()

	s.emit(Event{Type: "terminal_event", WorkspaceID: workspaceID, SessionID: current.id, Name: current.name, Kind: current.kind, Event: "started"})
	go s.run(current)
	return current.snapshot(0), nil
}

// StartDebugDAP implements DAP's runInTerminal reverse request using an
// integrated, debugger-owned PTY. The process is intentionally independent of
// the browser request context; StopOwner ties its lifetime to the DAP session.
func (s *Service) StartDebugDAP(ctx context.Context, workspaceID, ownerSessionID string, arguments map[string]any) (map[string]any, error) {
	if strings.EqualFold(stringFromAny(arguments["kind"]), "external") {
		return nil, ErrExternalUnsupported
	}
	if interpreted, _ := arguments["argsCanBeInterpretedByShell"].(bool); interpreted {
		return nil, errors.New("debug terminal shell-interpreted arguments are not supported; provide direct argv")
	}
	argv, err := dapArgv(arguments["args"])
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return nil, err
	}
	workingDir := strings.TrimSpace(stringFromAny(arguments["cwd"]))
	if workingDir == "" {
		workingDir, err = availableWorkingDirectory(workspace)
		if err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	sandboxManager := s.sandbox
	factory := s.newBackend
	s.mu.Unlock()
	// Reverse requests are process launches, so use the same fail-closed policy
	// as the normal terminal path instead of treating a missing manager as host.
	sandboxEnabled := workspace.Sandbox.Enabled || (sandboxManager != nil && sandboxManager.IsEnabled(workspaceID))
	if sandboxEnabled && sandboxManager == nil {
		return nil, errors.New("sandbox debug terminal runtime is unavailable")
	}
	if sandboxEnabled && !strings.HasPrefix(filepath.ToSlash(workingDir), "/") {
		workingDir, err = sandboxManager.HostToGuest(workspaceID, workingDir)
		if err != nil {
			return nil, fmt.Errorf("map debug terminal working directory: %w", err)
		}
	}
	environment := terminalEnvironment()
	if sandboxEnabled {
		environment = sandboxTerminalEnvironment()
	}
	environment = mergeDAPEnvironment(environment, arguments["env"])

	cols, rows := ClampSize(120, 30)
	var backend Backend
	if sandboxEnabled {
		backend = &sandboxBackend{manager: sandboxManager, workspaceID: workspaceID}
	} else {
		backend, err = factory()
	}
	if err != nil {
		return nil, fmt.Errorf("create debug terminal: %w", err)
	}
	if err := backend.Resize(cols, rows); err != nil {
		_ = backend.Close()
		return nil, fmt.Errorf("size debug terminal: %w", err)
	}
	processContext, cancel := context.WithCancel(context.Background())
	process, err := backend.Start(processContext, CommandSpec{Name: argv[0], Args: argv[1:], Dir: workingDir, Env: environment})
	if err != nil {
		cancel()
		_ = backend.Close()
		return nil, fmt.Errorf("start debug terminal: %w", err)
	}
	title := strings.TrimSpace(stringFromAny(arguments["title"]))
	if title == "" {
		title = "Debug Terminal"
	}
	current := &session{
		workspaceID: workspaceID, id: uuid.NewString(), name: title, kind: "debug", ownerID: ownerSessionID,
		shell: filepath.Base(argv[0]), workingDir: workingDir, backend: backend, process: process,
		cancel: cancel, done: make(chan struct{}), status: "running", output: make([]bufferedChunk, 0, 64),
	}
	s.mu.Lock()
	s.sessions[current.id] = current
	s.mu.Unlock()
	s.emit(Event{Type: "terminal_event", WorkspaceID: workspaceID, SessionID: current.id, Name: current.name, Kind: current.kind, OwnerSessionID: current.ownerID, Event: "started"})
	go s.run(current)
	response := map[string]any{"sessionId": current.id}
	if process, ok := process.(interface{ PID() int }); ok && process.PID() > 0 {
		response["processId"] = process.PID()
	}
	return response, nil
}

// StopOwner terminates every integrated terminal created for one debugger
// session without disturbing the workspace's normal shell.
func (s *Service) StopOwner(workspaceID, ownerSessionID string) {
	s.mu.Lock()
	all := make([]*session, 0)
	for id, current := range s.sessions {
		if current.workspaceID == workspaceID && current.ownerID == ownerSessionID && current.kind == "debug" {
			all = append(all, current)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
	for _, current := range all {
		current.stopAndWait()
	}
}

func (s *Service) Sync(workspaceID, sessionID string, afterSequence uint64) (Snapshot, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return Snapshot{}, err
	}
	current, err := s.current(workspaceID, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return current.snapshot(afterSequence), nil
}

// List returns the server-owned terminal sessions for reconnecting browsers.
// In particular, debug PTYs cannot be reconstructed from a DAP snapshot alone
// because runInTerminal assigns their own independent session IDs.
func (s *Service) List(workspaceID string) ([]Snapshot, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	current := make([]*session, 0, len(s.sessions))
	for _, candidate := range s.sessions {
		if candidate.workspaceID == workspaceID {
			current = append(current, candidate)
		}
	}
	s.mu.Unlock()
	result := make([]Snapshot, 0, len(current))
	for _, candidate := range current {
		result = append(result, candidate.snapshot(0))
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (s *Service) Write(workspaceID, sessionID, data string) error {
	if len(data) > MaxInput {
		return fmt.Errorf("%w: maximum is %d bytes", ErrInputTooLarge, MaxInput)
	}
	if data == "" {
		return nil
	}
	current, err := s.currentRunning(workspaceID, sessionID)
	if err != nil {
		return err
	}
	current.writeMu.Lock()
	defer current.writeMu.Unlock()
	buffer := []byte(data)
	for len(buffer) > 0 {
		written, writeErr := current.backend.Write(buffer)
		if writeErr != nil {
			return fmt.Errorf("write terminal input: %w", writeErr)
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		buffer = buffer[written:]
	}
	return nil
}

func (s *Service) Resize(workspaceID, sessionID string, cols, rows int) error {
	current, err := s.currentRunning(workspaceID, sessionID)
	if err != nil {
		return err
	}
	cols, rows = ClampSize(cols, rows)
	if err := current.backend.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}
	return nil
}

func (s *Service) Stop(workspaceID, sessionID string) error {
	current, err := s.current(workspaceID, sessionID)
	if err != nil {
		return err
	}
	current.stopAndWait()
	return nil
}

// StopWorkspace terminates and forgets a workspace terminal when its root is
// rebound. A subsequent Start resolves the new authoritative workspace path.
func (s *Service) StopWorkspace(workspaceID string) {
	s.mu.Lock()
	all := make([]*session, 0)
	for id, current := range s.sessions {
		if current.workspaceID == workspaceID {
			all = append(all, current)
			delete(s.sessions, id)
		}
	}
	delete(s.defaults, workspaceID)
	delete(s.tasks, workspaceID)
	s.mu.Unlock()
	for _, current := range all {
		current.stopAndWait()
	}
}

func (s *Service) Restart(workspaceID, sessionID string, cols, rows int) (Snapshot, error) {
	current, err := s.current(workspaceID, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if current.kind != "default" {
		return Snapshot{}, errors.New("debug-owned terminals are restarted with their debug session")
	}
	s.mu.Lock()
	if s.sessions[sessionID] == current {
		delete(s.sessions, sessionID)
	}
	if s.defaults[workspaceID] == sessionID {
		delete(s.defaults, workspaceID)
	}
	s.mu.Unlock()
	current.stopAndWait()
	return s.Start(workspaceID, cols, rows)
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		all = append(all, current)
	}
	s.sessions = make(map[string]*session)
	s.defaults = make(map[string]string)
	s.tasks = make(map[string]string)
	s.mu.Unlock()
	for _, current := range all {
		current.stop()
	}
	for _, current := range all {
		select {
		case <-current.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) ListSavedCommands(workspaceID string) ([]SavedCommand, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return nil, err
	}
	file, err := s.data.Load()
	if err != nil {
		return nil, err
	}
	commands := fromStoredCommands(file.SavedCommands[workspaceID])
	sort.SliceStable(commands, func(i, j int) bool { return commands[i].Order < commands[j].Order })
	return commands, nil
}

func (s *Service) CreateSavedCommand(workspaceID, name, command string) (SavedCommand, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return SavedCommand{}, err
	}
	name, command, err := validateSavedCommand(name, command)
	if err != nil {
		return SavedCommand{}, err
	}
	created := SavedCommand{ID: uuid.NewString(), Name: name, Command: command}
	err = s.data.Update(func(file *appdata.File) error {
		if file.SavedCommands == nil {
			file.SavedCommands = make(map[string][]appdata.SavedCommand)
		}
		for _, existing := range file.SavedCommands[workspaceID] {
			if existing.Order >= created.Order {
				created.Order = existing.Order + 1
			}
		}
		file.SavedCommands[workspaceID] = append(file.SavedCommands[workspaceID], toStoredCommand(created))
		return nil
	})
	return created, err
}

func (s *Service) UpdateSavedCommand(workspaceID, id, name, command string) (SavedCommand, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return SavedCommand{}, err
	}
	name, command, err := validateSavedCommand(name, command)
	if err != nil {
		return SavedCommand{}, err
	}
	var updated SavedCommand
	err = s.data.Update(func(file *appdata.File) error {
		for index, existing := range file.SavedCommands[workspaceID] {
			if existing.ID != id {
				continue
			}
			existing.Name = name
			existing.Command = command
			file.SavedCommands[workspaceID][index] = existing
			updated = fromStoredCommand(existing)
			return nil
		}
		return ErrSavedCommandNotFound
	})
	return updated, err
}

func (s *Service) DeleteSavedCommand(workspaceID, id string) error {
	if _, err := s.workspace(workspaceID); err != nil {
		return err
	}
	return s.data.Update(func(file *appdata.File) error {
		existing := file.SavedCommands[workspaceID]
		for index, command := range existing {
			if command.ID != id {
				continue
			}
			file.SavedCommands[workspaceID] = append(existing[:index], existing[index+1:]...)
			return nil
		}
		return ErrSavedCommandNotFound
	})
}

func (s *Service) workspace(id string) (workspaces.Workspace, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return workspaces.Workspace{}, fmt.Errorf("%w: workspace id is required", ErrWorkspaceNotFound)
	}
	workspace, ok, err := s.workspaces.Get(id)
	if err != nil {
		return workspaces.Workspace{}, err
	}
	if !ok {
		return workspaces.Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	return workspace, nil
}

func (s *Service) current(workspaceID, sessionID string) (*session, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	current := s.sessions[sessionID]
	s.mu.Unlock()
	if current == nil || sessionID == "" || current.workspaceID != workspaceID {
		return nil, ErrSessionNotFound
	}
	return current, nil
}

func (s *Service) currentRunning(workspaceID, sessionID string) (*session, error) {
	current, err := s.current(workspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	running := current.status == "running"
	current.mu.Unlock()
	if !running {
		return nil, ErrSessionNotRunning
	}
	return current, nil
}

func (s *Service) run(current *session) {
	defer close(current.done)
	waitResult := make(chan struct {
		exitCode int
		err      error
	}, 1)
	go func() {
		exitCode, err := current.process.Wait()
		waitResult <- struct {
			exitCode int
			err      error
		}{exitCode: exitCode, err: err}
	}()

	readResult := make(chan error, 1)
	go func() {
		buffer := make([]byte, readBytes)
		for {
			count, err := current.backend.Read(buffer)
			if count > 0 {
				s.emitOutput(current, buffer[:count])
			}
			if err != nil {
				readResult <- err
				return
			}
		}
	}()

	result := <-waitResult
	var readErr error
	forcedClose := false
	select {
	case readErr = <-readResult:
	case <-time.After(150 * time.Millisecond):
		// ConPTY can keep Read blocked after a short-lived process exits. Give
		// trailing output a chance to drain, then close the PTY deterministically.
		forcedClose = true
		current.closeBackend()
		readErr = <-readResult
	}
	current.closeBackend()
	current.cancel()

	message := ""
	if readErr != nil && !errors.Is(readErr, io.EOF) && !forcedClose && !current.stopping() {
		message = readErr.Error()
	} else if result.err != nil && result.exitCode < 0 && !current.stopping() {
		message = result.err.Error()
	}
	wasStopping := current.stopping()
	current.mu.Lock()
	exitCode := result.exitCode
	current.exitCode = &exitCode
	current.status = "exited"
	if current.kind == "test" {
		switch {
		case wasStopping:
			current.taskStatus = "stopped"
		case exitCode == 0 && message == "":
			current.taskStatus = "passed"
		default:
			current.taskStatus = "failed"
		}
	}
	current.message = message
	lastSequence := current.sequence
	taskStatus := current.taskStatus
	current.mu.Unlock()
	s.emit(Event{
		Type: "terminal_event", WorkspaceID: current.workspaceID, SessionID: current.id,
		Name: current.name, Kind: current.kind, OwnerSessionID: current.ownerID,
		Event: "exited", Sequence: lastSequence, ExitCode: &exitCode, Message: message, TaskStatus: taskStatus,
	})
}

func (s *Service) emitOutput(current *session, data []byte) {
	value := append([]byte(nil), data...)
	current.mu.Lock()
	current.sequence++
	sequence := current.sequence
	current.output = append(current.output, bufferedChunk{sequence: sequence, data: value})
	current.outputBytes += len(value)
	for current.outputBytes > ReplayBytes && len(current.output) > 1 {
		current.outputBytes -= len(current.output[0].data)
		current.output = current.output[1:]
	}
	taskStatus := current.taskStatus
	current.mu.Unlock()
	s.emit(Event{
		Type: "terminal_event", WorkspaceID: current.workspaceID, SessionID: current.id,
		Name: current.name, Kind: current.kind, OwnerSessionID: current.ownerID,
		Event: "data", Sequence: sequence, Data: base64.StdEncoding.EncodeToString(value), TaskStatus: taskStatus,
	})
}

func (s *Service) emit(event Event) {
	s.mu.Lock()
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(event)
	}
}

func (current *session) snapshot(afterSequence uint64) Snapshot {
	current.mu.Lock()
	defer current.mu.Unlock()
	reset := false
	if afterSequence > 0 && len(current.output) > 0 && afterSequence+1 < current.output[0].sequence {
		reset = true
	}
	output := make([]OutputChunk, 0, len(current.output))
	for _, chunk := range current.output {
		if !reset && chunk.sequence <= afterSequence {
			continue
		}
		output = append(output, OutputChunk{Sequence: chunk.sequence, Data: base64.StdEncoding.EncodeToString(chunk.data)})
	}
	var exitCode *int
	if current.exitCode != nil {
		value := *current.exitCode
		exitCode = &value
	}
	return Snapshot{
		WorkspaceID: current.workspaceID, ID: current.id, Name: current.name, Kind: current.kind,
		OwnerSessionID: current.ownerID, Shell: current.shell,
		WorkingDirectory: current.workingDir, Status: current.status, ExitCode: exitCode,
		Message: current.message, TaskStatus: current.taskStatus, LastSequence: current.sequence, Reset: reset, Output: output,
	}
}

func (current *session) stop() {
	current.stopOnce.Do(func() {
		current.mu.Lock()
		if current.status == "running" {
			current.status = "stopping"
			if current.kind == "test" {
				current.taskStatus = "stopped"
			}
		}
		current.mu.Unlock()
		current.cancel()
		_ = current.process.Kill()
		current.closeBackend()
	})
}

func mergeTaskEnvironment(base []string, values map[string]string) []string {
	if len(values) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(values))
	for _, item := range base {
		if index := strings.IndexByte(item, '='); index > 0 {
			merged[item[:index]] = item[index+1:]
		}
	}
	for key, value := range values {
		if runtime.GOOS == "windows" {
			for existing := range merged {
				if strings.EqualFold(existing, key) {
					delete(merged, existing)
				}
			}
		}
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+merged[key])
	}
	return result
}

func (current *session) closeBackend() {
	current.closeOnce.Do(func() { _ = current.backend.Close() })
}
func (current *session) stopAndWait() { current.stop(); <-current.done }
func (current *session) stopping() bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.status == "stopping"
}

func ClampSize(cols, rows int) (int, int) {
	if cols < MinCols {
		cols = MinCols
	}
	if cols > MaxCols {
		cols = MaxCols
	}
	if rows < MinRows {
		rows = MinRows
	}
	if rows > MaxRows {
		rows = MaxRows
	}
	return cols, rows
}

func availableWorkingDirectory(workspace workspaces.Workspace) (string, error) {
	candidates := append([]string{workspace.MainPath}, workspace.Folders...)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if candidate == "." || candidate == "" {
			continue
		}
		key := candidate
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
	}
	return "", ErrWorkspaceUnavailable
}

func resolveInteractiveShell() (string, []string, string, error) {
	if runtime.GOOS == "windows" {
		if shell, err := exec.LookPath("pwsh.exe"); err == nil {
			return shell, []string{"-NoLogo"}, "PowerShell", nil
		}
		if shell, err := exec.LookPath("powershell.exe"); err == nil {
			return shell, []string{"-NoLogo"}, "Windows PowerShell", nil
		}
		return "", nil, "", errors.New("PowerShell was not found")
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" && filepath.IsAbs(shell) {
		if _, err := os.Stat(shell); err == nil {
			return shell, nil, filepath.Base(shell), nil
		}
	}
	return "/bin/sh", nil, "sh", nil
}

func terminalEnvironment() []string {
	values := os.Environ()
	values = setEnvironment(values, "TERM", "xterm-256color")
	values = setEnvironment(values, "COLORTERM", "truecolor")
	values = setEnvironment(values, "TERM_PROGRAM", "Echo")
	return values
}

func sandboxTerminalEnvironment() []string {
	return []string{
		"TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=Echo",
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "ECHO_SANDBOX=1",
	}
}

func setEnvironment(values []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	for index, existing := range values {
		if strings.HasPrefix(strings.ToUpper(existing), prefix) {
			values[index] = key + "=" + value
			return values
		}
	}
	return append(values, key+"="+value)
}

func removeEnvironment(values []string, key string) []string {
	prefix := strings.ToUpper(key) + "="
	result := values[:0]
	for _, existing := range values {
		if !strings.HasPrefix(strings.ToUpper(existing), prefix) {
			result = append(result, existing)
		}
	}
	return result
}

func mergeDAPEnvironment(values []string, raw any) []string {
	environment, ok := raw.(map[string]any)
	if !ok {
		return values
	}
	for key, value := range environment {
		if value == nil {
			values = removeEnvironment(values, key)
			continue
		}
		if text, ok := value.(string); ok {
			values = setEnvironment(values, key, text)
		}
	}
	return values
}

func dapArgv(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("debug terminal requires a non-empty args array")
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok || strings.ContainsRune(text, 0) || (index == 0 && strings.TrimSpace(text) == "") {
			return nil, fmt.Errorf("debug terminal argument %d is invalid", index)
		}
		result[index] = text
	}
	return result, nil
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func validateSavedCommand(name, command string) (string, string, error) {
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" || command == "" {
		return "", "", errors.New("name and command are required")
	}
	return name, command, nil
}

func fromStoredCommands(values []appdata.SavedCommand) []SavedCommand {
	result := make([]SavedCommand, len(values))
	for index, value := range values {
		result[index] = fromStoredCommand(value)
	}
	return result
}
func fromStoredCommand(value appdata.SavedCommand) SavedCommand {
	return SavedCommand{ID: value.ID, Name: value.Name, Command: value.Command, Order: value.Order}
}
func toStoredCommand(value SavedCommand) appdata.SavedCommand {
	return appdata.SavedCommand{ID: value.ID, Name: value.Name, Command: value.Command, Order: value.Order}
}
