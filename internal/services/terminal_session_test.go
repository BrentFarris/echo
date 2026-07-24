package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeTerminalWaitResult struct {
	exitCode int
	err      error
}

type fakeTerminalBackend struct {
	mu        sync.Mutex
	readCh    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	writes    bytes.Buffer
	cols      int
	rows      int
	spec      terminalCommandSpec
	process   *fakeTerminalProcess
	pending   []byte
}

type fakeTerminalProcess struct {
	backend  *fakeTerminalBackend
	waitCh   chan fakeTerminalWaitResult
	waitOnce sync.Once
}

func newFakeTerminalBackend() *fakeTerminalBackend {
	backend := &fakeTerminalBackend{
		readCh: make(chan []byte, 128),
		closed: make(chan struct{}),
	}
	backend.process = &fakeTerminalProcess{
		backend: backend,
		waitCh:  make(chan fakeTerminalWaitResult, 1),
	}
	return backend
}

func (b *fakeTerminalBackend) Read(buffer []byte) (int, error) {
	b.mu.Lock()
	if len(b.pending) > 0 {
		count := copy(buffer, b.pending)
		b.pending = b.pending[count:]
		b.mu.Unlock()
		return count, nil
	}
	b.mu.Unlock()
	select {
	case value := <-b.readCh:
		b.mu.Lock()
		count := copy(buffer, value)
		b.pending = append(b.pending[:0], value[count:]...)
		b.mu.Unlock()
		return count, nil
	case <-b.closed:
		return 0, io.EOF
	}
}

func (b *fakeTerminalBackend) Write(buffer []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.Write(buffer)
}

func (b *fakeTerminalBackend) Close() error {
	b.closeOnce.Do(func() {
		close(b.closed)
	})
	return nil
}

func (b *fakeTerminalBackend) Resize(cols, rows int) error {
	b.mu.Lock()
	b.cols = cols
	b.rows = rows
	b.mu.Unlock()
	return nil
}

func (b *fakeTerminalBackend) Start(_ context.Context, spec terminalCommandSpec) (terminalProcess, error) {
	b.mu.Lock()
	b.spec = spec
	b.mu.Unlock()
	return b.process, nil
}

func (b *fakeTerminalBackend) send(data []byte) {
	b.readCh <- append([]byte(nil), data...)
}

func (b *fakeTerminalBackend) written() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.String()
}

func (p *fakeTerminalProcess) Wait() (int, error) {
	result := <-p.waitCh
	return result.exitCode, result.err
}

func (p *fakeTerminalProcess) Kill() error {
	p.complete(-1, errors.New("killed"))
	return nil
}

func (p *fakeTerminalProcess) complete(exitCode int, err error) {
	p.waitOnce.Do(func() {
		p.waitCh <- fakeTerminalWaitResult{exitCode: exitCode, err: err}
		_ = p.backend.Close()
	})
}

func TestTerminalSessionLifecycleAndReplay(t *testing.T) {
	service, workspaceID := newTerminalTestService(t)
	backend := newFakeTerminalBackend()
	restore := installFakeTerminalBackend(t, backend)
	defer restore()
	defer service.closeAllTerminalSessions()

	snapshot, err := service.StartTerminalSession(workspaceID, 120, 40)
	if err != nil {
		t.Fatalf("start terminal: %v", err)
	}
	if snapshot.Status != "running" || snapshot.ID == "" {
		t.Fatalf("unexpected start snapshot: %#v", snapshot)
	}
	if backend.cols != 120 || backend.rows != 40 {
		t.Fatalf("expected initial terminal size 120x40, got %dx%d", backend.cols, backend.rows)
	}
	if backend.spec.Dir == "" || len(backend.spec.Env) == 0 {
		t.Fatalf("expected terminal working directory and environment")
	}

	again, err := service.StartTerminalSession(workspaceID, 80, 24)
	if err != nil {
		t.Fatalf("start existing terminal: %v", err)
	}
	if again.ID != snapshot.ID {
		t.Fatalf("expected idempotent start to return %q, got %q", snapshot.ID, again.ID)
	}

	if err := service.WriteTerminalSession(workspaceID, snapshot.ID, "echo hello\r"); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	if got := backend.written(); got != "echo hello\r" {
		t.Fatalf("unexpected terminal input %q", got)
	}
	if err := service.ResizeTerminalSession(workspaceID, snapshot.ID, 999, 1); err != nil {
		t.Fatalf("resize terminal: %v", err)
	}
	if backend.cols != terminalMaxCols || backend.rows != terminalMinRows {
		t.Fatalf("expected clamped size, got %dx%d", backend.cols, backend.rows)
	}

	events, unsubscribe := SubscribeEvents(service, 16)
	defer unsubscribe()
	backend.send([]byte("\x1b[32mhello\x1b[0m\r\n"))
	event := waitForTerminalEvent(t, events, "data")
	if event.Sequence != 1 || event.Data == "" {
		t.Fatalf("unexpected output event: %#v", event)
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Data)
	if err != nil {
		t.Fatalf("decode terminal event: %v", err)
	}
	if expected := "\x1b[32mhello\x1b[0m\r\n"; string(decoded) != expected {
		t.Fatalf("expected raw ANSI output %q, got %q", expected, decoded)
	}

	replay, err := service.SyncTerminalSession(workspaceID, snapshot.ID, 0)
	if err != nil {
		t.Fatalf("sync terminal: %v", err)
	}
	if replay.LastSequence != 1 || len(replay.Output) != 1 {
		t.Fatalf("unexpected replay snapshot: %#v", replay)
	}
	caughtUp, err := service.SyncTerminalSession(workspaceID, snapshot.ID, 1)
	if err != nil {
		t.Fatalf("sync caught-up terminal: %v", err)
	}
	if len(caughtUp.Output) != 0 {
		t.Fatalf("expected no output after sequence 1, got %#v", caughtUp.Output)
	}

	backend.process.complete(7, errors.New("exit status 7"))
	exit := waitForTerminalEvent(t, events, "exited")
	if exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %#v", exit)
	}
	exited := waitForTerminalStatus(t, service, workspaceID, snapshot.ID, "exited")
	if exited.ExitCode == nil || *exited.ExitCode != 7 {
		t.Fatalf("unexpected exited snapshot: %#v", exited)
	}
}

func TestTerminalSessionReplayResetAfterTruncation(t *testing.T) {
	service, workspaceID := newTerminalTestService(t)
	backend := newFakeTerminalBackend()
	restore := installFakeTerminalBackend(t, backend)
	defer restore()
	defer service.closeAllTerminalSessions()

	snapshot, err := service.StartTerminalSession(workspaceID, 80, 24)
	if err != nil {
		t.Fatalf("start terminal: %v", err)
	}
	chunk := bytes.Repeat([]byte("x"), terminalReadBytes)
	chunkCount := terminalReplayBytes/terminalReadBytes + 8
	for i := 0; i < chunkCount; i++ {
		backend.send(chunk)
	}
	waitForTerminalSequence(t, service, workspaceID, snapshot.ID, uint64(chunkCount))

	replay, err := service.SyncTerminalSession(workspaceID, snapshot.ID, 1)
	if err != nil {
		t.Fatalf("sync terminal: %v", err)
	}
	if !replay.Reset {
		t.Fatalf("expected replay reset after history truncation")
	}
	if len(replay.Output) == 0 || len(replay.Output) >= chunkCount {
		t.Fatalf("expected only retained output, got %d chunks", len(replay.Output))
	}
}

func TestTerminalSessionRejectsInvalidOrStaleRequests(t *testing.T) {
	service, workspaceID := newTerminalTestService(t)
	backend := newFakeTerminalBackend()
	restore := installFakeTerminalBackend(t, backend)
	defer restore()
	defer service.closeAllTerminalSessions()

	snapshot, err := service.StartTerminalSession(workspaceID, 0, 0)
	if err != nil {
		t.Fatalf("start terminal: %v", err)
	}
	if backend.cols != terminalMinCols || backend.rows != terminalMinRows {
		t.Fatalf("expected minimum initial dimensions")
	}
	if err := service.WriteTerminalSession(workspaceID, "stale", "x"); err == nil {
		t.Fatal("expected stale session write to fail")
	}
	if err := service.WriteTerminalSession(workspaceID, snapshot.ID, string(make([]byte, terminalMaxInput+1))); err == nil {
		t.Fatal("expected oversized terminal input to fail")
	}

	if err := service.StopTerminalSession(workspaceID, snapshot.ID); err != nil {
		t.Fatalf("stop terminal: %v", err)
	}
	waitForTerminalStatus(t, service, workspaceID, snapshot.ID, "exited")
	if err := service.WriteTerminalSession(workspaceID, snapshot.ID, "x"); err == nil {
		t.Fatal("expected write to stopped terminal to fail")
	}
}

func TestTerminalSessionRestartAndWorkspaceCleanup(t *testing.T) {
	service, workspaceID := newTerminalTestService(t)
	first := newFakeTerminalBackend()
	second := newFakeTerminalBackend()
	backends := []terminalBackend{first, second}
	index := 0
	previous := newTerminalBackend
	newTerminalBackend = func() (terminalBackend, error) {
		value := backends[index]
		index++
		return value, nil
	}
	defer func() { newTerminalBackend = previous }()
	defer service.closeAllTerminalSessions()

	started, err := service.StartTerminalSession(workspaceID, 80, 24)
	if err != nil {
		t.Fatalf("start terminal: %v", err)
	}
	restarted, err := service.RestartTerminalSession(workspaceID, started.ID, 100, 30)
	if err != nil {
		t.Fatalf("restart terminal: %v", err)
	}
	if restarted.ID == started.ID {
		t.Fatalf("expected restart to create a new session")
	}
	if err := service.WriteTerminalSession(workspaceID, started.ID, "stale"); err == nil {
		t.Fatal("expected the restarted session to reject the stale id")
	}
	select {
	case <-first.closed:
	case <-time.After(time.Second):
		t.Fatal("expected restart to close the previous PTY")
	}

	if _, err := service.DeleteWorkspace(workspaceID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	select {
	case <-second.closed:
	case <-time.After(time.Second):
		t.Fatal("expected workspace deletion to close its PTY")
	}
}

func TestTerminalSessionConcurrentStopAndShutdown(t *testing.T) {
	service, workspaceID := newTerminalTestService(t)
	backend := newFakeTerminalBackend()
	restore := installFakeTerminalBackend(t, backend)
	defer restore()

	started, err := service.StartTerminalSession(workspaceID, 80, 24)
	if err != nil {
		t.Fatalf("start terminal: %v", err)
	}

	finished := make(chan struct{})
	go func() {
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			_ = service.StopTerminalSession(workspaceID, started.ID)
		}()
		go func() {
			defer wait.Done()
			service.Shutdown()
		}()
		wait.Wait()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent terminal cleanup did not complete")
	}
	select {
	case <-backend.closed:
	default:
		t.Fatal("expected shutdown to close the PTY")
	}
	service.terminalMu.Lock()
	remaining := len(service.terminalSessions)
	service.terminalMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected shutdown to clear terminal sessions, got %d", remaining)
	}
}

func newTerminalTestService(t *testing.T) (*SystemService, string) {
	t.Helper()
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return service, state.ActiveWorkspaceID
}

func installFakeTerminalBackend(t *testing.T, backend terminalBackend) func() {
	t.Helper()
	previous := newTerminalBackend
	newTerminalBackend = func() (terminalBackend, error) {
		return backend, nil
	}
	return func() {
		newTerminalBackend = previous
	}
}

func waitForTerminalEvent(t *testing.T, events <-chan RuntimeEvent, eventType string) TerminalEvent {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-events:
			terminal, ok := event.Data.(TerminalEvent)
			if event.Name == TerminalRuntimeEventName && ok && terminal.Type == eventType {
				return terminal
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for terminal %s event", eventType)
		}
	}
}

func waitForTerminalStatus(t *testing.T, service *SystemService, workspaceID, sessionID, status string) TerminalSessionSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.SyncTerminalSession(workspaceID, sessionID, 0)
		if err == nil && snapshot.Status == status {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal status %q", status)
	return TerminalSessionSnapshot{}
}

func waitForTerminalSequence(t *testing.T, service *SystemService, workspaceID, sessionID string, sequence uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.SyncTerminalSession(workspaceID, sessionID, sequence)
		if err == nil && snapshot.LastSequence >= sequence {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal sequence %d", sequence)
}
