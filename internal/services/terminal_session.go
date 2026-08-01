package services

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	ptylib "github.com/aymanbagabas/go-pty"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	terminalReplayBytes = 2 * 1024 * 1024
	terminalMaxInput    = 64 * 1024
	terminalReadBytes   = 32 * 1024
	terminalMinCols     = 2
	terminalMaxCols     = 500
	terminalMinRows     = 2
	terminalMaxRows     = 200
)

type TerminalOutputChunk struct {
	Sequence uint64 `json:"sequence"`
	Data     string `json:"data"`
}

type TerminalSessionSnapshot struct {
	WorkspaceID  string                `json:"workspaceId"`
	ID           string                `json:"id"`
	Shell        string                `json:"shell"`
	WorkingDir   string                `json:"workingDirectory"`
	Status       string                `json:"status"`
	ExitCode     *int                  `json:"exitCode,omitempty"`
	Message      string                `json:"message,omitempty"`
	LastSequence uint64                `json:"lastSequence"`
	Reset        bool                  `json:"reset,omitempty"`
	Output       []TerminalOutputChunk `json:"output"`
}

type TerminalEvent struct {
	WorkspaceID string `json:"workspaceId"`
	ID          string `json:"id"`
	Type        string `json:"type"`
	Sequence    uint64 `json:"sequence,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Message     string `json:"message,omitempty"`
}

type terminalCommandSpec struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type terminalProcess interface {
	Wait() (int, error)
	Kill() error
}

type terminalBackend interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Start(context.Context, terminalCommandSpec) (terminalProcess, error)
}

type realTerminalBackend struct {
	pty ptylib.Pty
}

type realTerminalProcess struct {
	cmd *ptylib.Cmd
}

var newTerminalBackend = func() (terminalBackend, error) {
	value, err := ptylib.New()
	if err != nil {
		return nil, err
	}
	return &realTerminalBackend{pty: value}, nil
}

func (b *realTerminalBackend) Read(buffer []byte) (int, error) {
	return b.pty.Read(buffer)
}

func (b *realTerminalBackend) Write(buffer []byte) (int, error) {
	return b.pty.Write(buffer)
}

func (b *realTerminalBackend) Close() error {
	return b.pty.Close()
}

func (b *realTerminalBackend) Resize(cols, rows int) error {
	return b.pty.Resize(cols, rows)
}

func (b *realTerminalBackend) Start(ctx context.Context, spec terminalCommandSpec) (terminalProcess, error) {
	command := b.pty.CommandContext(ctx, spec.Name, spec.Args...)
	command.Dir = spec.Dir
	command.Env = spec.Env
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &realTerminalProcess{cmd: command}, nil
}

func (p *realTerminalProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), err
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (p *realTerminalProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

type terminalBufferedChunk struct {
	sequence uint64
	data     []byte
}

type terminalSession struct {
	workspaceID string
	id          string
	shell       string
	workingDir  string
	backend     terminalBackend
	process     terminalProcess
	cancel      context.CancelFunc

	mu          sync.Mutex
	writeMu     sync.Mutex
	stopOnce    sync.Once
	closeOnce   sync.Once
	done        chan struct{}
	status      string
	exitCode    *int
	message     string
	sequence    uint64
	output      []terminalBufferedChunk
	outputBytes int
}

func (s *SystemService) StartTerminalSession(workspaceID string, cols, rows int) (TerminalSessionSnapshot, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TerminalSessionSnapshot{}, err
	}
	workingDir, ok := firstAvailableWorkspaceFolderPath(workspace)
	if !ok {
		return TerminalSessionSnapshot{}, fmt.Errorf("workspace has no available folders")
	}
	shellName, shellArgs, shellLabel, err := resolveInteractiveShell()
	if err != nil {
		return TerminalSessionSnapshot{}, err
	}
	cols, rows = clampTerminalSize(cols, rows)

	s.terminalMu.Lock()
	if current := s.terminalSessions[workspaceID]; current != nil {
		s.terminalMu.Unlock()
		return current.snapshot(0), nil
	}
	s.terminalSeq++
	sessionID := fmt.Sprintf("%s:%d", workspaceID, s.terminalSeq)

	backend, err := newTerminalBackend()
	if err != nil {
		s.terminalMu.Unlock()
		return TerminalSessionSnapshot{}, fmt.Errorf("create terminal: %w", err)
	}
	if err := backend.Resize(cols, rows); err != nil {
		_ = backend.Close()
		s.terminalMu.Unlock()
		return TerminalSessionSnapshot{}, fmt.Errorf("size terminal: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	spec := terminalCommandSpec{
		Name: shellName,
		Args: shellArgs,
		Dir:  workingDir,
		Env:  terminalEnvironment(),
	}
	process, err := backend.Start(ctx, spec)
	if err != nil {
		cancel()
		_ = backend.Close()
		s.terminalMu.Unlock()
		return TerminalSessionSnapshot{}, fmt.Errorf("start %s: %w", shellLabel, err)
	}
	session := &terminalSession{
		workspaceID: workspaceID,
		id:          sessionID,
		shell:       shellLabel,
		workingDir:  workingDir,
		backend:     backend,
		process:     process,
		cancel:      cancel,
		done:        make(chan struct{}),
		status:      "running",
		output:      make([]terminalBufferedChunk, 0, 64),
	}
	s.terminalSessions[workspaceID] = session
	s.terminalMu.Unlock()

	s.emitTerminalEvent(TerminalEvent{
		WorkspaceID: workspaceID,
		ID:          sessionID,
		Type:        "started",
	})
	go s.runTerminalSession(session)
	return session.snapshot(0), nil
}

func (s *SystemService) SyncTerminalSession(workspaceID, sessionID string, afterSequence uint64) (TerminalSessionSnapshot, error) {
	if _, _, err := s.workspaceAndSettings(workspaceID); err != nil {
		return TerminalSessionSnapshot{}, err
	}
	session, err := s.currentTerminalSession(workspaceID, sessionID)
	if err != nil {
		return TerminalSessionSnapshot{}, err
	}
	return session.snapshot(afterSequence), nil
}

func (s *SystemService) WriteTerminalSession(workspaceID, sessionID, data string) error {
	if len(data) > terminalMaxInput {
		return fmt.Errorf("terminal input exceeds %d bytes", terminalMaxInput)
	}
	if data == "" {
		return nil
	}
	session, err := s.currentRunningTerminalSession(workspaceID, sessionID)
	if err != nil {
		return err
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	buffer := []byte(data)
	for len(buffer) > 0 {
		written, writeErr := session.backend.Write(buffer)
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

func (s *SystemService) ResizeTerminalSession(workspaceID, sessionID string, cols, rows int) error {
	session, err := s.currentRunningTerminalSession(workspaceID, sessionID)
	if err != nil {
		return err
	}
	cols, rows = clampTerminalSize(cols, rows)
	if err := session.backend.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}
	return nil
}

func (s *SystemService) StopTerminalSession(workspaceID, sessionID string) error {
	session, err := s.currentTerminalSession(workspaceID, sessionID)
	if err != nil {
		return err
	}
	session.stopAndWait()
	return nil
}

func (s *SystemService) RestartTerminalSession(workspaceID, sessionID string, cols, rows int) (TerminalSessionSnapshot, error) {
	session, err := s.currentTerminalSession(workspaceID, sessionID)
	if err != nil {
		return TerminalSessionSnapshot{}, err
	}
	s.terminalMu.Lock()
	if s.terminalSessions[workspaceID] == session {
		delete(s.terminalSessions, workspaceID)
	}
	s.terminalMu.Unlock()
	session.stopAndWait()
	return s.StartTerminalSession(workspaceID, cols, rows)
}

func (s *SystemService) currentTerminalSession(workspaceID, sessionID string) (*terminalSession, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	sessionID = strings.TrimSpace(sessionID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace id is required")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("terminal session id is required")
	}
	s.terminalMu.Lock()
	session := s.terminalSessions[workspaceID]
	s.terminalMu.Unlock()
	if session == nil || session.id != sessionID {
		return nil, fmt.Errorf("terminal session was not found")
	}
	return session, nil
}

func (s *SystemService) currentRunningTerminalSession(workspaceID, sessionID string) (*terminalSession, error) {
	session, err := s.currentTerminalSession(workspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	running := session.status == "running"
	session.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("terminal session is not running")
	}
	return session, nil
}

func (s *SystemService) runTerminalSession(session *terminalSession) {
	defer close(session.done)
	waitResult := make(chan struct {
		exitCode int
		err      error
	}, 1)
	go func() {
		exitCode, err := session.process.Wait()
		waitResult <- struct {
			exitCode int
			err      error
		}{exitCode: exitCode, err: err}
	}()

	buffer := make([]byte, terminalReadBytes)
	var readErr error
	for {
		count, err := session.backend.Read(buffer)
		if count > 0 {
			s.emitTerminalOutput(session, buffer[:count])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !isTerminalStopping(session) {
				readErr = err
			}
			break
		}
	}
	result := <-waitResult
	session.closeBackend()
	session.cancel()

	message := ""
	if readErr != nil {
		message = readErr.Error()
	} else if result.err != nil && result.exitCode < 0 && !isTerminalStopping(session) {
		message = result.err.Error()
	}
	session.mu.Lock()
	exitCode := result.exitCode
	session.exitCode = &exitCode
	session.status = "exited"
	session.message = message
	lastSequence := session.sequence
	session.mu.Unlock()

	s.emitTerminalEvent(TerminalEvent{
		WorkspaceID: session.workspaceID,
		ID:          session.id,
		Type:        "exited",
		Sequence:    lastSequence,
		ExitCode:    &exitCode,
		Message:     message,
	})
}

func (s *SystemService) emitTerminalOutput(session *terminalSession, data []byte) {
	value := append([]byte(nil), data...)
	session.mu.Lock()
	session.sequence++
	sequence := session.sequence
	session.output = append(session.output, terminalBufferedChunk{
		sequence: sequence,
		data:     value,
	})
	session.outputBytes += len(value)
	for session.outputBytes > terminalReplayBytes && len(session.output) > 1 {
		session.outputBytes -= len(session.output[0].data)
		session.output = session.output[1:]
	}
	session.mu.Unlock()

	s.emitTerminalEvent(TerminalEvent{
		WorkspaceID: session.workspaceID,
		ID:          session.id,
		Type:        "data",
		Sequence:    sequence,
		Data:        base64.StdEncoding.EncodeToString(value),
	})
}

func (s *SystemService) emitTerminalEvent(event TerminalEvent) {
	s.emitRuntimeEvent(TerminalRuntimeEventName, event)
	if s.ctx != nil {
		wailsruntime.EventsEmit(s.ctx, TerminalRuntimeEventName, event)
	}
}

func (session *terminalSession) snapshot(afterSequence uint64) TerminalSessionSnapshot {
	session.mu.Lock()
	defer session.mu.Unlock()
	reset := false
	if afterSequence > 0 && len(session.output) > 0 && afterSequence+1 < session.output[0].sequence {
		reset = true
	}
	output := make([]TerminalOutputChunk, 0, len(session.output))
	for _, chunk := range session.output {
		if !reset && chunk.sequence <= afterSequence {
			continue
		}
		output = append(output, TerminalOutputChunk{
			Sequence: chunk.sequence,
			Data:     base64.StdEncoding.EncodeToString(chunk.data),
		})
	}
	var exitCode *int
	if session.exitCode != nil {
		value := *session.exitCode
		exitCode = &value
	}
	return TerminalSessionSnapshot{
		WorkspaceID:  session.workspaceID,
		ID:           session.id,
		Shell:        session.shell,
		WorkingDir:   session.workingDir,
		Status:       session.status,
		ExitCode:     exitCode,
		Message:      session.message,
		LastSequence: session.sequence,
		Reset:        reset,
		Output:       output,
	}
}

func (session *terminalSession) stop() {
	session.stopOnce.Do(func() {
		session.mu.Lock()
		if session.status == "running" {
			session.status = "stopping"
		}
		session.mu.Unlock()
		session.cancel()
		_ = session.process.Kill()
		session.closeBackend()
	})
}

func (session *terminalSession) closeBackend() {
	session.closeOnce.Do(func() {
		_ = session.backend.Close()
	})
}

func (session *terminalSession) stopAndWait() {
	session.stop()
	<-session.done
}

func isTerminalStopping(session *terminalSession) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.status == "stopping"
}

func (s *SystemService) closeWorkspaceTerminalSession(workspaceID string) {
	s.terminalMu.Lock()
	session := s.terminalSessions[workspaceID]
	delete(s.terminalSessions, workspaceID)
	s.terminalMu.Unlock()
	if session != nil {
		session.stopAndWait()
	}
}

func (s *SystemService) closeAllTerminalSessions() {
	s.terminalMu.Lock()
	sessions := make([]*terminalSession, 0, len(s.terminalSessions))
	for _, session := range s.terminalSessions {
		sessions = append(sessions, session)
	}
	s.terminalSessions = make(map[string]*terminalSession)
	s.terminalMu.Unlock()
	for _, session := range sessions {
		session.stop()
	}
	for _, session := range sessions {
		<-session.done
	}
}

func clampTerminalSize(cols, rows int) (int, int) {
	if cols < terminalMinCols {
		cols = terminalMinCols
	}
	if cols > terminalMaxCols {
		cols = terminalMaxCols
	}
	if rows < terminalMinRows {
		rows = terminalMinRows
	}
	if rows > terminalMaxRows {
		rows = terminalMaxRows
	}
	return cols, rows
}

func resolveInteractiveShell() (string, []string, string, error) {
	if goruntime.GOOS == "windows" {
		if shell, err := exec.LookPath("pwsh.exe"); err == nil {
			return shell, []string{"-NoLogo"}, "PowerShell", nil
		}
		if shell, err := exec.LookPath("powershell.exe"); err == nil {
			return shell, []string{"-NoLogo"}, "Windows PowerShell", nil
		}
		return "", nil, "", fmt.Errorf("PowerShell was not found")
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
	values = setTerminalEnvironment(values, "TERM", "xterm-256color")
	values = setTerminalEnvironment(values, "COLORTERM", "truecolor")
	values = setTerminalEnvironment(values, "TERM_PROGRAM", "Echo")
	return values
}

func setTerminalEnvironment(values []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	for i, existing := range values {
		if strings.HasPrefix(strings.ToUpper(existing), prefix) {
			values[i] = key + "=" + value
			return values
		}
	}
	return append(values, key+"="+value)
}
