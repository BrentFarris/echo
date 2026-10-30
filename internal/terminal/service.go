// Package terminal owns Echo's shared bot-scoped interactive terminals.
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
	"unicode"

	"database/sql"
	ptylib "github.com/aymanbagabas/go-pty"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
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
	BotID            string        `json:"botId"`
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
	BotID          string `json:"botId"`
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
	botID      string
	id         string
	name       string
	kind       string
	ownerID    string
	shell      string
	workingDir string
	backend    Backend
	process    Process
	cancel     context.CancelFunc

	mu          sync.Mutex
	writeMu     sync.Mutex
	stopOnce    sync.Once
	done        chan struct{}
	status      string
	exitCode    *int
	message     string
	taskStatus  string
	sequence    uint64
	output      []bufferedChunk
	outputBytes int
	closed      map[Backend]bool
}

type Service struct {
	resolve    func(string) (domain.Bot, error)
	data       *store.Store
	mu         sync.Mutex
	commandMu  sync.Mutex
	sessions   map[string]*session
	defaults   map[string]string
	newBackend func() (Backend, error)
	notify     func(Event)
	closed     bool
}

func New(resolve func(string) (domain.Bot, error), data *store.Store) *Service {
	return &Service{resolve: resolve, data: data, sessions: map[string]*session{}, defaults: map[string]string{}, newBackend: newRealBackend}
}
func (s *Service) workspace(id string) (domain.Bot, error) { return s.resolve(id) }
func (s *Service) SetNotifier(notify func(Event)) {
	s.mu.Lock()
	s.notify = notify
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
func (s *Service) Start(botID string, cols, rows int) (Snapshot, error) {
	workspace, err := s.workspace(botID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	existing := s.sessions[s.defaults[botID]]
	s.mu.Unlock()
	if existing != nil {
		return existing.snapshot(0), nil
	}
	workingDir, err := availableWorkingDirectory(workspace)
	if err != nil {
		return Snapshot{}, err
	}
	shellName, shellArgs, shellLabel, err := resolveInteractiveShell()
	if err != nil {
		return Snapshot{}, err
	}
	cols, rows = ClampSize(cols, rows)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Snapshot{}, errors.New("terminal service is closed")
	}
	if current := s.sessions[s.defaults[botID]]; current != nil {
		s.mu.Unlock()
		return current.snapshot(0), nil
	}
	factory := s.newBackend
	backend, err := factory()
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
		botID:      botID,
		id:         uuid.NewString(),
		name:       "Terminal",
		kind:       "default",
		shell:      shellLabel,
		workingDir: workingDir,
		backend:    backend,
		process:    process,
		cancel:     cancel,
		done:       make(chan struct{}),
		status:     "running",
		output:     make([]bufferedChunk, 0, 64),
	}
	s.sessions[current.id] = current
	s.defaults[botID] = current.id
	s.mu.Unlock()

	s.emit(Event{Type: "terminal_event", BotID: botID, SessionID: current.id, Name: current.name, Kind: current.kind, Event: "started"})
	go s.run(current)
	return current.snapshot(0), nil
}

func (s *Service) Sync(botID, sessionID string, afterSequence uint64) (Snapshot, error) {
	if _, err := s.workspace(botID); err != nil {
		return Snapshot{}, err
	}
	current, err := s.current(botID, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	return current.snapshot(afterSequence), nil
}

func (s *Service) List(botID string) ([]Snapshot, error) {
	if _, err := s.workspace(botID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	current := make([]*session, 0, len(s.sessions))
	for _, candidate := range s.sessions {
		if candidate.botID == botID {
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
func (s *Service) Write(botID, sessionID, data string) error {
	if len(data) > MaxInput {
		return fmt.Errorf("%w: maximum is %d bytes", ErrInputTooLarge, MaxInput)
	}
	if data == "" {
		return nil
	}
	current, err := s.currentRunning(botID, sessionID)
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
func (s *Service) Resize(botID, sessionID string, cols, rows int) error {
	current, err := s.currentRunning(botID, sessionID)
	if err != nil {
		return err
	}
	cols, rows = ClampSize(cols, rows)
	if err := current.backend.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}
	return nil
}
func (s *Service) Stop(botID, sessionID string) error {
	current, err := s.current(botID, sessionID)
	if err != nil {
		return err
	}
	current.stopAndWait()
	return nil
}

func (s *Service) StopBot(botID string) {
	s.mu.Lock()
	all := make([]*session, 0)
	for id, current := range s.sessions {
		if current.botID == botID {
			all = append(all, current)
			delete(s.sessions, id)
		}
	}
	delete(s.defaults, botID)
	s.mu.Unlock()
	for _, current := range all {
		current.stopAndWait()
	}
}
func (s *Service) Restart(botID, sessionID string, cols, rows int) (Snapshot, error) {
	current, err := s.current(botID, sessionID)
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
	if s.defaults[botID] == sessionID {
		delete(s.defaults, botID)
	}
	s.mu.Unlock()
	current.stopAndWait()
	return s.Start(botID, cols, rows)
}
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		all = append(all, current)
	}
	s.sessions = make(map[string]*session)
	s.defaults = make(map[string]string)
	s.closed = true
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
func (s *Service) current(botID, sessionID string) (*session, error) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	current := s.sessions[sessionID]
	s.mu.Unlock()
	if current == nil || sessionID == "" || current.botID != botID {
		return nil, ErrSessionNotFound
	}
	return current, nil
}
func (s *Service) currentRunning(botID, sessionID string) (*session, error) {
	current, err := s.current(botID, sessionID)
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
	code, message := s.runProcess(current)
	current.mu.Lock()
	current.exitCode = &code
	current.status = "exited"
	current.message = message
	seq := current.sequence
	current.mu.Unlock()
	s.emit(Event{Type: "terminal_event", BotID: current.botID, SessionID: current.id, Event: "exited", Sequence: seq, ExitCode: &code, Message: message})
}
func (s *Service) runProcess(current *session) (int, string) {
	current.mu.Lock()
	process, backend := current.process, current.backend
	current.mu.Unlock()
	waitResult := make(chan struct {
		exitCode int
		err      error
	}, 1)
	go func() {
		exitCode, err := process.Wait()
		waitResult <- struct {
			exitCode int
			err      error
		}{exitCode: exitCode, err: err}
	}()

	readResult := make(chan error, 1)
	go func() {
		buffer := make([]byte, readBytes)
		for {
			count, err := backend.Read(buffer)
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
		current.closeBackend(backend)
		readErr = <-readResult
	}
	current.closeBackend(backend)
	current.mu.Lock()
	cancel := current.cancel
	current.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	message := ""
	if readErr != nil && !errors.Is(readErr, io.EOF) && !forcedClose && !current.stopping() {
		message = readErr.Error()
	} else if result.err != nil && result.exitCode < 0 && !current.stopping() {
		message = result.err.Error()
	}
	return result.exitCode, message
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
		Type: "terminal_event", BotID: current.botID, SessionID: current.id,
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
		BotID: current.botID, ID: current.id, Name: current.name, Kind: current.kind,
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
		process := current.process
		cancel := current.cancel
		backend := current.backend
		current.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if process != nil {
			_ = process.Kill()
		}
		current.closeBackend(backend)
	})
}
func (current *session) closeBackend(backend Backend) {
	if backend == nil {
		return
	}
	current.mu.Lock()
	if current.closed == nil {
		current.closed = make(map[Backend]bool)
	}
	if current.closed[backend] {
		current.mu.Unlock()
		return
	}
	current.closed[backend] = true
	current.mu.Unlock()
	_ = backend.Close()
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
func availableWorkingDirectory(bot domain.Bot) (string, error) {
	if len(bot.Roots) == 0 {
		return os.UserHomeDir()
	}
	for _, root := range bot.Roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return filepath.Abs(root)
		}
	}
	return "", ErrWorkspaceUnavailable
}
func (s *Service) ListSavedCommands(id string) ([]SavedCommand, error) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return s.savedCommands(id)
}
func (s *Service) savedCommands(id string) ([]SavedCommand, error) {
	if _, err := s.resolve(id); err != nil {
		return nil, err
	}
	list := []SavedCommand{}
	err := s.data.Get("terminal-commands", id, &list)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Order < list[j].Order })
	return list, err
}
func (s *Service) SaveCommand(id, commandID, name, command string) (SavedCommand, error) {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	name = strings.TrimSpace(name)
	command = strings.TrimSpace(command)
	if name == "" || command == "" || len(name) > 1024 || len(command) > MaxInput {
		return SavedCommand{}, errors.New("a name and command are required (maximum 1024 and 65536 bytes)")
	}
	list, err := s.savedCommands(id)
	if err != nil {
		return SavedCommand{}, err
	}
	item := SavedCommand{ID: commandID, Name: name, Command: command}
	if commandID == "" {
		item.ID = uuid.NewString()
		for _, v := range list {
			if item.Order <= v.Order {
				item.Order = v.Order + 1
			}
		}
		list = append(list, item)
	} else {
		found := false
		for i, v := range list {
			if v.ID == commandID {
				item.Order = v.Order
				list[i] = item
				found = true
				break
			}
		}
		if !found {
			return SavedCommand{}, ErrSavedCommandNotFound
		}
	}
	return item, s.data.Put("terminal-commands", id, list)
}
func (s *Service) DeleteSavedCommand(id, commandID string) error {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	list, err := s.savedCommands(id)
	if err != nil {
		return err
	}
	for i, v := range list {
		if v.ID == commandID {
			return s.data.Put("terminal-commands", id, append(list[:i], list[i+1:]...))
		}
	}
	return ErrSavedCommandNotFound
}

// Reconcile also cleans up sessions whose browser has gone away.
func (s *Service) Reconcile() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.defaults))
	for id := range s.defaults {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if _, err := s.resolve(id); err != nil {
			s.StopBot(id)
		}
	}
}
func (s *Service) ChangeFolder(botID, sessionID, folder string) error {
	bot, err := s.resolve(botID)
	if err != nil {
		return err
	}
	found := false
	for _, root := range bot.Roots {
		if root == folder {
			found = true
			break
		}
	}
	if !found {
		return errors.New("folder is not configured for this bot")
	}
	info, err := os.Stat(folder)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("folder is unavailable")
	}
	current, err := s.currentRunning(botID, sessionID)
	if err != nil {
		return err
	}
	command, err := DirectoryCommand(current.shell, folder)
	if err != nil {
		return err
	}
	return s.Write(botID, sessionID, command+"\r")
}
func DirectoryCommand(shell, folder string) (string, error) {
	if strings.IndexFunc(folder, unicode.IsControl) >= 0 {
		return "", errors.New("folder contains terminal control characters")
	}
	if strings.Contains(strings.ToLower(shell), "powershell") {
		return "Set-Location -LiteralPath '" + strings.ReplaceAll(folder, "'", "''") + "'", nil
	}
	return "cd -- '" + strings.ReplaceAll(folder, "'", "'\\''") + "'", nil
}
