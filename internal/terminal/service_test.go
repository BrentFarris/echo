package terminal

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/workspaces"
)

type fakeWaitResult struct {
	exitCode int
	err      error
}

type fakeBackend struct {
	mu         sync.Mutex
	readCh     chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
	closeCalls int
	writes     bytes.Buffer
	cols       int
	rows       int
	spec       CommandSpec
	process    *fakeProcess
	pending    []byte
}

type fakeProcess struct {
	backend  *fakeBackend
	waitCh   chan fakeWaitResult
	waitOnce sync.Once
}

type startErrorBackend struct{ *fakeBackend }

func (b *startErrorBackend) Start(context.Context, CommandSpec) (Process, error) {
	return nil, errors.New("process startup failed")
}

func newFakeBackend() *fakeBackend {
	backend := &fakeBackend{readCh: make(chan []byte, 128), closed: make(chan struct{})}
	backend.process = &fakeProcess{backend: backend, waitCh: make(chan fakeWaitResult, 1)}
	return backend
}

func (b *fakeBackend) Read(buffer []byte) (int, error) {
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

func (b *fakeBackend) Write(buffer []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.Write(buffer)
}

func (b *fakeBackend) Close() error {
	b.mu.Lock()
	b.closeCalls++
	b.mu.Unlock()
	b.signalClosed()
	return nil
}

func (b *fakeBackend) Resize(cols, rows int) error {
	b.mu.Lock()
	b.cols, b.rows = cols, rows
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) Start(_ context.Context, spec CommandSpec) (Process, error) {
	b.mu.Lock()
	b.spec = spec
	b.mu.Unlock()
	return b.process, nil
}

func (b *fakeBackend) signalClosed()    { b.closeOnce.Do(func() { close(b.closed) }) }
func (b *fakeBackend) send(data []byte) { b.readCh <- append([]byte(nil), data...) }
func (b *fakeBackend) written() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.String()
}
func (b *fakeBackend) size() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cols, b.rows
}
func (b *fakeBackend) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalls
}

func (p *fakeProcess) Wait() (int, error) {
	result := <-p.waitCh
	return result.exitCode, result.err
}
func (p *fakeProcess) Kill() error {
	p.complete(-1, errors.New("killed"))
	return nil
}
func (p *fakeProcess) complete(exitCode int, err error) {
	p.waitOnce.Do(func() {
		p.waitCh <- fakeWaitResult{exitCode: exitCode, err: err}
		p.backend.signalClosed()
	})
}

func TestSessionLifecycleReplayAndExit(t *testing.T) {
	service, workspace, _ := newTestService(t)
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })
	t.Cleanup(func() { shutdownTestService(t, service) })

	events := make(chan Event, 16)
	service.SetNotifier(func(event Event) { events <- event })
	started, err := service.Start(workspace.ID, 120, 40)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.ID == "" || started.Status != "running" {
		t.Fatalf("unexpected start snapshot: %#v", started)
	}
	if cols, rows := backend.size(); cols != 120 || rows != 40 {
		t.Fatalf("initial size = %dx%d, want 120x40", cols, rows)
	}
	if backend.spec.Dir != workspace.MainPath || len(backend.spec.Env) == 0 {
		t.Fatalf("unexpected command spec: %#v", backend.spec)
	}

	again, err := service.Start(workspace.ID, 80, 24)
	if err != nil || again.ID != started.ID {
		t.Fatalf("idempotent start = %#v, %v", again, err)
	}
	if err := service.Write(workspace.ID, started.ID, "echo hello\r"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := backend.written(); got != "echo hello\r" {
		t.Fatalf("input = %q", got)
	}
	if err := service.Resize(workspace.ID, started.ID, 999, 1); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if cols, rows := backend.size(); cols != MaxCols || rows != MinRows {
		t.Fatalf("clamped size = %dx%d", cols, rows)
	}

	ansi := []byte("\x1b[32mhello\x1b[0m\r\n")
	backend.send(ansi)
	dataEvent := waitEvent(t, events, "data")
	decoded, err := base64.StdEncoding.DecodeString(dataEvent.Data)
	if err != nil || !bytes.Equal(decoded, ansi) || dataEvent.Sequence != 1 {
		t.Fatalf("raw output event = %#v, decoded %q, err %v", dataEvent, decoded, err)
	}
	replay, err := service.Sync(workspace.ID, started.ID, 0)
	if err != nil || replay.LastSequence != 1 || len(replay.Output) != 1 {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	caughtUp, err := service.Sync(workspace.ID, started.ID, 1)
	if err != nil || len(caughtUp.Output) != 0 {
		t.Fatalf("caught-up replay = %#v, %v", caughtUp, err)
	}

	backend.process.complete(7, errors.New("exit status 7"))
	exit := waitEvent(t, events, "exited")
	if exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Fatalf("exit event = %#v", exit)
	}
	exited := waitStatus(t, service, workspace.ID, started.ID, "exited")
	if exited.ExitCode == nil || *exited.ExitCode != 7 {
		t.Fatalf("exit snapshot = %#v", exited)
	}
	if err := service.Write(workspace.ID, started.ID, "x"); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("write after exit = %v", err)
	}
}

func TestTaskLifecycleStatusEnvironmentAndReplacement(t *testing.T) {
	service, workspace, _ := newTestService(t)
	first, second := newFakeBackend(), newFakeBackend()
	firstCompletion := make(chan TaskResult, 2)
	backends := []Backend{first, second}
	service.SetBackendFactory(func() (Backend, error) {
		next := backends[0]
		backends = backends[1:]
		return next, nil
	})
	t.Cleanup(func() { shutdownTestService(t, service) })

	started, err := service.StartTask(workspace.ID, TaskRequest{
		Name: "Test Output", Command: "go", Args: []string{"test", "."}, WorkingDirectory: workspace.MainPath,
		Environment: map[string]string{"ECHO_TEST": "yes"}, DisplayCommand: "go test .",
		OnExit: func(result TaskResult) { firstCompletion <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Kind != "test" || started.TaskStatus != "running" || len(started.Output) != 1 {
		t.Fatalf("task snapshot = %#v", started)
	}
	foundEnvironment := false
	for _, item := range first.spec.Env {
		foundEnvironment = foundEnvironment || item == "ECHO_TEST=yes"
	}
	if !foundEnvironment {
		t.Fatalf("task environment = %#v", first.spec.Env)
	}

	replaced, err := service.StartTask(workspace.ID, TaskRequest{
		Name: "Test Output", Command: "go", Args: []string{"test", "-run", "TestOne", "."}, WorkingDirectory: workspace.MainPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ID == started.ID {
		t.Fatal("replacement reused task session id")
	}
	completion := <-firstCompletion
	if completion.SessionID != started.ID || completion.Status != "stopped" {
		t.Fatalf("replacement completion = %#v", completion)
	}
	select {
	case duplicate := <-firstCompletion:
		t.Fatalf("duplicate replacement completion = %#v", duplicate)
	default:
	}
	if _, err := service.Sync(workspace.ID, started.ID, 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("replaced task is still retained: %v", err)
	}
	second.process.complete(0, nil)
	exited := waitStatus(t, service, workspace.ID, replaced.ID, "exited")
	if exited.TaskStatus != "passed" || exited.ExitCode == nil || *exited.ExitCode != 0 {
		t.Fatalf("completed task = %#v", exited)
	}
}

func TestTaskCompletionRunsOnceAfterFinalOutputAndStatus(t *testing.T) {
	service, workspace, _ := newTestService(t)
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })
	t.Cleanup(func() { shutdownTestService(t, service) })

	completed := make(chan TaskResult, 2)
	started, err := service.StartTask(workspace.ID, TaskRequest{
		Name: "Test Output", Command: "go", Args: []string{"test", "."}, WorkingDirectory: workspace.MainPath,
		OnExit: func(result TaskResult) { completed <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.readCh <- []byte("final output")
	backend.process.complete(0, nil)
	result := <-completed
	if result.SessionID != started.ID || result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("completion = %#v", result)
	}
	snapshot, err := service.Sync(workspace.ID, started.ID, 0)
	if err != nil || snapshot.Status != "exited" || len(snapshot.Output) != 1 {
		t.Fatalf("snapshot before callback = %#v, %v", snapshot, err)
	}
	select {
	case duplicate := <-completed:
		t.Fatalf("duplicate completion = %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestTaskCompletionReportsFailureAndStop(t *testing.T) {
	for _, scenario := range []struct {
		name string
		want string
		end  func(*Service, workspaces.Workspace, *fakeBackend, string) error
	}{
		{
			name: "failure", want: "failed",
			end: func(_ *Service, _ workspaces.Workspace, backend *fakeBackend, _ string) error {
				backend.process.complete(2, errors.New("exit status 2"))
				return nil
			},
		},
		{
			name: "stop", want: "stopped",
			end: func(service *Service, workspace workspaces.Workspace, _ *fakeBackend, sessionID string) error {
				return service.Stop(workspace.ID, sessionID)
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			service, workspace, _ := newTestService(t)
			backend := newFakeBackend()
			service.SetBackendFactory(func() (Backend, error) { return backend, nil })
			t.Cleanup(func() { shutdownTestService(t, service) })
			completed := make(chan TaskResult, 2)
			started, err := service.StartTask(workspace.ID, TaskRequest{
				Name: "Test Output", Command: "go", Args: []string{"test", "."}, WorkingDirectory: workspace.MainPath,
				OnExit: func(result TaskResult) { completed <- result },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := scenario.end(service, workspace, backend, started.ID); err != nil {
				t.Fatal(err)
			}
			result := <-completed
			if result.SessionID != started.ID || result.Status != scenario.want {
				t.Fatalf("completion = %#v, want status %q", result, scenario.want)
			}
			select {
			case duplicate := <-completed:
				t.Fatalf("duplicate completion = %#v", duplicate)
			case <-time.After(25 * time.Millisecond):
			}
		})
	}
}

func TestTaskStartupFailureKeepsStoppedOutputForRecovery(t *testing.T) {
	service, workspace, _ := newTestService(t)
	first := newFakeBackend()
	failed := &startErrorBackend{fakeBackend: newFakeBackend()}
	backends := []Backend{first, failed}
	service.SetBackendFactory(func() (Backend, error) {
		next := backends[0]
		backends = backends[1:]
		return next, nil
	})
	t.Cleanup(func() { shutdownTestService(t, service) })

	started, err := service.StartTask(workspace.ID, TaskRequest{
		Name: "Test Output", Command: "go", Args: []string{"test", "."}, WorkingDirectory: workspace.MainPath,
		DisplayCommand: "go test .",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartTask(workspace.ID, TaskRequest{
		Name: "Test Output", Command: "go", Args: []string{"test", "./broken"}, WorkingDirectory: workspace.MainPath,
	}); err == nil {
		t.Fatal("task startup failure was not returned")
	}
	recovered, err := service.Sync(workspace.ID, started.ID, 0)
	if err != nil {
		t.Fatalf("stopped task output was discarded: %v", err)
	}
	if recovered.TaskStatus != "stopped" || len(recovered.Output) == 0 {
		t.Fatalf("recovered task = %#v", recovered)
	}
}

func TestDebugTerminalIsNamedOwnedAndIndependentOfDefaultShell(t *testing.T) {
	service, workspace, _ := newTestService(t)
	defaultBackend := newFakeBackend()
	debugBackend := newFakeBackend()
	backends := []Backend{defaultBackend, debugBackend}
	var backendMu sync.Mutex
	service.SetBackendFactory(func() (Backend, error) {
		backendMu.Lock()
		defer backendMu.Unlock()
		if len(backends) == 0 {
			return nil, errors.New("no fake backend")
		}
		next := backends[0]
		backends = backends[1:]
		return next, nil
	})
	t.Cleanup(func() { shutdownTestService(t, service) })

	defaultSession, err := service.Start(workspace.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.StartDebugDAP(context.Background(), workspace.ID, "debug-owner", map[string]any{
		"kind": "integrated", "title": "API Process", "cwd": workspace.MainPath,
		"args": []any{"example-debuggee", "--port", "4000"},
		"env":  map[string]any{"ECHO_MODE": "debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	debugID, _ := response["sessionId"].(string)
	if debugID == "" || debugID == defaultSession.ID {
		t.Fatalf("debug response = %#v", response)
	}
	debugSnapshot, err := service.Sync(workspace.ID, debugID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if debugSnapshot.Kind != "debug" || debugSnapshot.Name != "API Process" || debugSnapshot.OwnerSessionID != "debug-owner" {
		t.Fatalf("debug snapshot = %#v", debugSnapshot)
	}
	if debugBackend.spec.Name != "example-debuggee" || len(debugBackend.spec.Args) != 2 {
		t.Fatalf("debug argv = %#v", debugBackend.spec)
	}
	foundEnvironment := false
	for _, value := range debugBackend.spec.Env {
		if value == "ECHO_MODE=debug" {
			foundEnvironment = true
		}
	}
	if !foundEnvironment {
		t.Fatalf("debug environment = %#v", debugBackend.spec.Env)
	}
	reconnect, err := service.List(workspace.ID)
	if err != nil || len(reconnect) != 2 {
		t.Fatalf("terminal reconnect list = %#v, %v", reconnect, err)
	}
	foundDebug := false
	for _, snapshot := range reconnect {
		if snapshot.ID == debugID && snapshot.Kind == "debug" && snapshot.OwnerSessionID == "debug-owner" {
			foundDebug = true
		}
	}
	if !foundDebug {
		t.Fatalf("debug terminal missing from reconnect list: %#v", reconnect)
	}

	service.StopOwner(workspace.ID, "debug-owner")
	if _, err := service.Sync(workspace.ID, debugID, 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("owned terminal still exists: %v", err)
	}
	if snapshot, err := service.Sync(workspace.ID, defaultSession.ID, 0); err != nil || snapshot.Status != "running" {
		t.Fatalf("default terminal was disturbed: %#v, %v", snapshot, err)
	}
}

func TestDebugTerminalFailsClosedWithoutSandboxRuntime(t *testing.T) {
	service, workspace, _ := newTestService(t)
	manager := service.workspaces.(*workspaces.Manager)
	config := workspaces.DefaultSandboxConfig()
	config.Enabled = true
	if _, err := manager.SetSandboxConfig(workspace.ID, config); err != nil {
		t.Fatal(err)
	}
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })

	_, err := service.StartDebugDAP(context.Background(), workspace.ID, "debug-owner", map[string]any{
		"kind": "integrated", "args": []any{"example-debuggee"},
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox debug terminal runtime is unavailable") {
		t.Fatalf("sandbox debug terminal error = %v", err)
	}
	if backend.spec.Name != "" {
		t.Fatalf("host backend was started for sandbox workspace: %#v", backend.spec)
	}
}

func TestReplayResetAfterTruncation(t *testing.T) {
	service, workspace, _ := newTestService(t)
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })
	t.Cleanup(func() { shutdownTestService(t, service) })
	started, err := service.Start(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	chunk := bytes.Repeat([]byte("x"), readBytes)
	count := ReplayBytes/readBytes + 8
	for index := 0; index < count; index++ {
		backend.send(chunk)
	}
	waitSequence(t, service, workspace.ID, started.ID, uint64(count))
	replay, err := service.Sync(workspace.ID, started.ID, 1)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !replay.Reset || len(replay.Output) == 0 || len(replay.Output) >= count {
		t.Fatalf("expected retained reset snapshot, got reset=%v chunks=%d", replay.Reset, len(replay.Output))
	}
}

func TestStaleOversizedAndStoppedRequests(t *testing.T) {
	service, workspace, _ := newTestService(t)
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })
	started, err := service.Start(workspace.ID, 0, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if cols, rows := backend.size(); cols != MinCols || rows != MinRows {
		t.Fatalf("minimum size = %dx%d", cols, rows)
	}
	if err := service.Write(workspace.ID, "stale", "x"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("stale write = %v", err)
	}
	if err := service.Resize(workspace.ID, "stale", 80, 24); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("stale resize = %v", err)
	}
	if err := service.Write(workspace.ID, started.ID, string(make([]byte, MaxInput+1))); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("oversized write = %v", err)
	}
	if err := service.Stop(workspace.ID, started.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if backend.closeCount() != 1 {
		t.Fatalf("PTY closed %d times", backend.closeCount())
	}
	if err := service.Resize(workspace.ID, started.ID, 80, 24); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("resize after stop = %v", err)
	}
}

func TestRestartAndConcurrentShutdownCloseOnce(t *testing.T) {
	service, workspace, _ := newTestService(t)
	first, second := newFakeBackend(), newFakeBackend()
	backends := []Backend{first, second}
	var factoryMu sync.Mutex
	service.SetBackendFactory(func() (Backend, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		backend := backends[0]
		backends = backends[1:]
		return backend, nil
	})
	started, err := service.Start(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	restarted, err := service.Restart(workspace.ID, started.ID, 100, 30)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.ID == started.ID {
		t.Fatal("restart reused session id")
	}
	if err := service.Write(workspace.ID, started.ID, "stale"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old id after restart = %v", err)
	}
	if first.closeCount() != 1 {
		t.Fatalf("first PTY closed %d times", first.closeCount())
	}

	finished := make(chan struct{})
	go func() {
		var wait sync.WaitGroup
		wait.Add(2)
		go func() { defer wait.Done(); _ = service.Stop(workspace.ID, restarted.ID) }()
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = service.Shutdown(ctx)
		}()
		wait.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent stop and shutdown deadlocked")
	}
	if second.closeCount() != 1 {
		t.Fatalf("second PTY closed %d times", second.closeCount())
	}
}

func TestWorkingDirectoryRejectsUnavailableMainFolder(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "gone")
	extraPath := filepath.Join(root, "available")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extraPath, 0o755); err != nil {
		t.Fatal(err)
	}
	data := appdata.NewStore(filepath.Join(root, "echo.json"))
	manager := workspaces.NewManagerWithData(data)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "fallback", MainPath: mainPath, Folders: []string{extraPath}})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.RemoveAll(mainPath); err != nil {
		t.Fatalf("remove main folder: %v", err)
	}
	service := New(manager, data)
	backend := newFakeBackend()
	service.SetBackendFactory(func() (Backend, error) { return backend, nil })
	t.Cleanup(func() { shutdownTestService(t, service) })
	_, err = service.Start(workspace.ID, 80, 24)
	var configErr *workspaces.ConfigError
	if !errors.As(err, &configErr) || configErr.Code != workspaces.ConfigMainUnavailable {
		t.Fatalf("expected unavailable-main error, got %T %v", err, err)
	}
}

func TestSavedCommandCRUDOrderingAndPersistence(t *testing.T) {
	service, workspace, path := newTestService(t)
	if _, err := service.CreateSavedCommand(workspace.ID, " ", "echo nope"); err == nil {
		t.Fatal("blank name was accepted")
	}
	first, err := service.CreateSavedCommand(workspace.ID, " Status ", " git status ")
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := service.CreateSavedCommand(workspace.ID, "Tests", "go test ./...")
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if first.Order != 0 || second.Order != 1 || first.Name != "Status" || first.Command != "git status" {
		t.Fatalf("unexpected commands: %#v %#v", first, second)
	}
	updated, err := service.UpdateSavedCommand(workspace.ID, first.ID, "Git status", "git status --short")
	if err != nil || updated.Order != first.Order {
		t.Fatalf("update: %#v, %v", updated, err)
	}
	if err := service.DeleteSavedCommand(workspace.ID, "missing"); !errors.Is(err, ErrSavedCommandNotFound) {
		t.Fatalf("delete missing = %v", err)
	}
	if err := service.DeleteSavedCommand(workspace.ID, second.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	reloadedData := appdata.NewStore(path)
	reloaded := New(workspaces.NewManagerWithData(reloadedData), reloadedData)
	list, err := reloaded.ListSavedCommands(workspace.ID)
	if err != nil {
		t.Fatalf("reload commands: %v", err)
	}
	if len(list) != 1 || list[0].ID != first.ID || list[0].Command != "git status --short" {
		t.Fatalf("persisted commands = %#v", list)
	}
}

func newTestService(t *testing.T) (*Service, workspaces.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "echo.json")
	data := appdata.NewStore(path)
	manager := workspaces.NewManagerWithData(data)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "terminal", MainPath: filepath.Join(root, "workspace")})
	if err != nil {
		if mkdirErr := os.MkdirAll(filepath.Join(root, "workspace"), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		workspace, err = manager.Create(workspaces.CreateRequest{Name: "terminal", MainPath: filepath.Join(root, "workspace")})
	}
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return New(manager, data), workspace, path
}

func shutdownTestService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func waitEvent(t *testing.T, events <-chan Event, eventType string) Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Event == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s event", eventType)
		}
	}
}

func waitStatus(t *testing.T, service *Service, workspaceID, sessionID, status string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.Sync(workspaceID, sessionID, 0)
		if err == nil && snapshot.Status == status {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q", status)
	return Snapshot{}
}

func waitSequence(t *testing.T, service *Service, workspaceID, sessionID string, sequence uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.Sync(workspaceID, sessionID, 0)
		if err == nil && snapshot.LastSequence >= sequence {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sequence %d", sequence)
}
