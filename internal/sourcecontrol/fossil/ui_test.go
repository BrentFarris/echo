package fossil

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
	"github.com/brent/echo/internal/workspaces"
)

type fakeFossilUIProcess struct {
	done       chan error
	killErr    error
	killCloses bool
	kills      atomic.Int32
	once       sync.Once
}

func newFakeFossilUIProcess() *fakeFossilUIProcess {
	return &fakeFossilUIProcess{done: make(chan error, 1), killCloses: true}
}

func (p *fakeFossilUIProcess) Wait() error { return <-p.done }

func (p *fakeFossilUIProcess) Kill() error {
	p.kills.Add(1)
	if p.killErr != nil {
		return p.killErr
	}
	if p.killCloses {
		p.once.Do(func() { p.done <- errors.New("process killed") })
	}
	return nil
}

type fakeFossilUIStarter struct {
	mu              sync.Mutex
	calls           []fossilUIStartSpec
	processes       []*fakeFossilUIProcess
	startErr        error
	stderr          string
	exitImmediately bool
	exitErr         error
}

func (s *fakeFossilUIStarter) start(spec fossilUIStartSpec) (fossilUIProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec.Args = append([]string(nil), spec.Args...)
	spec.Environment = append([]string(nil), spec.Environment...)
	s.calls = append(s.calls, spec)
	if s.stderr != "" {
		_, _ = io.WriteString(spec.Stderr, s.stderr)
	}
	if s.startErr != nil {
		return nil, s.startErr
	}
	process := newFakeFossilUIProcess()
	s.processes = append(s.processes, process)
	if s.exitImmediately {
		process.once.Do(func() { process.done <- s.exitErr })
	}
	return process, nil
}

func (s *fakeFossilUIStarter) snapshot() ([]fossilUIStartSpec, []*fakeFossilUIProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fossilUIStartSpec(nil), s.calls...), append([]*fakeFossilUIProcess(nil), s.processes...)
}

func newFossilUITestProvider(t *testing.T) (*Provider, *repositoryState, *fakeFossilUIStarter) {
	t.Helper()
	provider, state, _ := newCheckpointFileTest(t)
	state.available = true
	state.label = "checkout"
	state.rootMu = &sync.RWMutex{}
	state.revision.Store(7)
	provider.mu.Lock()
	provider.repos[state.workspaceID] = map[string]*repositoryState{state.repositoryID(): state}
	provider.mu.Unlock()
	starter := &fakeFossilUIStarter{}
	provider.uiStarter = starter.start
	provider.uiStartupGrace = time.Millisecond
	provider.uiStopTimeout = 20 * time.Millisecond
	t.Cleanup(provider.Close)
	return provider, state, starter
}

func openFossilUITestAction(t *testing.T, provider *Provider, state *repositoryState, requestID string) (sourcecontrol.ActionResult, error) {
	t.Helper()
	return provider.Action(context.Background(), state.workspaceID, state.repositoryID(), sourcecontrol.ActionRequest{
		RequestID: requestID, Action: "open_ui", ExpectedRevision: state.revision.Load(),
	})
}

func TestFossilUIActionStartsRestartsAndKeepsRevision(t *testing.T) {
	provider, state, starter := newFossilUITestProvider(t)

	result, err := openFossilUITestAction(t, provider, state, "open-ui-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 7 || state.revision.Load() != 7 {
		t.Fatalf("open_ui changed the source-control revision: result=%d state=%d", result.Revision, state.revision.Load())
	}
	calls, processes := starter.snapshot()
	if len(calls) != 1 || len(processes) != 1 {
		t.Fatalf("launcher calls=%d processes=%d", len(calls), len(processes))
	}
	if calls[0].Executable != "fossil" || len(calls[0].Args) != 2 || calls[0].Args[0] != "ui" || calls[0].Args[1] != "--localhost" {
		t.Fatalf("launcher command = %q %#v", calls[0].Executable, calls[0].Args)
	}
	if calls[0].Directory != state.root {
		t.Fatalf("launcher directory = %q, want %q", calls[0].Directory, state.root)
	}

	if _, err := openFossilUITestAction(t, provider, state, "open-ui-2"); err != nil {
		t.Fatal(err)
	}
	calls, processes = starter.snapshot()
	if len(calls) != 2 || len(processes) != 2 || processes[0].kills.Load() != 1 {
		t.Fatalf("restart calls=%d processes=%d first kills=%d", len(calls), len(processes), processes[0].kills.Load())
	}
	provider.uiMu.Lock()
	running := len(provider.uiProcesses)
	provider.uiMu.Unlock()
	if running != 1 {
		t.Fatalf("running Fossil UI processes = %d, want 1", running)
	}

	provider.StopWorkspaceProcesses(state.workspaceID)
	if processes[1].kills.Load() != 1 {
		t.Fatalf("workspace stop kills = %d, want 1", processes[1].kills.Load())
	}
	provider.uiMu.Lock()
	running = len(provider.uiProcesses)
	provider.uiMu.Unlock()
	if running != 0 {
		t.Fatalf("running Fossil UI processes after stop = %d", running)
	}

	if _, err := openFossilUITestAction(t, provider, state, "open-ui-3"); err != nil {
		t.Fatal(err)
	}
	_, processes = starter.snapshot()
	provider.Close()
	if processes[2].kills.Load() != 1 {
		t.Fatalf("provider close kills = %d, want 1", processes[2].kills.Load())
	}
}

func TestFossilUIActionSurvivesRequestCancellation(t *testing.T) {
	provider, state, starter := newFossilUITestProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	result, err := provider.Action(ctx, state.workspaceID, state.repositoryID(), sourcecontrol.ActionRequest{
		RequestID: "open-ui", Action: "open_ui", ExpectedRevision: 7,
	})
	if err != nil || result.Revision != 7 {
		t.Fatalf("open_ui result=%#v err=%v", result, err)
	}
	cancel()
	time.Sleep(5 * time.Millisecond)
	_, processes := starter.snapshot()
	if len(processes) != 1 || processes[0].kills.Load() != 0 {
		t.Fatalf("request cancellation stopped provider-owned process: %#v", processes)
	}
}

func TestFossilUIActionReportsStartupAndRestartFailures(t *testing.T) {
	provider, state, starter := newFossilUITestProvider(t)
	starter.startErr = errors.New("cannot start")
	starter.stderr = "launch failed in " + state.root
	_, err := openFossilUITestAction(t, provider, state, "start-failure")
	var sourceError *sourcecontrol.Error
	if !errors.As(err, &sourceError) || sourceError.Code != "fossil_ui_start_failed" || sourceError.Message != "launch failed in <checkout>" {
		t.Fatalf("startup error = %#v", err)
	}

	starter.startErr = nil
	starter.stderr = "exited early"
	starter.exitImmediately = true
	starter.exitErr = errors.New("early exit")
	_, err = openFossilUITestAction(t, provider, state, "early-exit")
	if !errors.As(err, &sourceError) || sourceError.Code != "fossil_ui_start_failed" || sourceError.Message != "exited early" {
		t.Fatalf("early exit error = %#v", err)
	}

	starter.stderr = ""
	starter.exitImmediately = false
	if _, err := openFossilUITestAction(t, provider, state, "open-ui"); err != nil {
		t.Fatal(err)
	}
	_, processes := starter.snapshot()
	processes[len(processes)-1].killErr = errors.New("cannot stop")
	processes[len(processes)-1].killCloses = false
	_, err = openFossilUITestAction(t, provider, state, "restart-failure")
	if !errors.As(err, &sourceError) || sourceError.Code != "fossil_ui_restart_failed" {
		t.Fatalf("restart error = %#v", err)
	}
	provider.uiMu.Lock()
	retained := len(provider.uiProcesses)
	provider.uiMu.Unlock()
	if retained != 1 {
		t.Fatalf("failed restart retained %d owned processes, want 1", retained)
	}
	processes[len(processes)-1].killErr = nil
	provider.Close()
}

func TestFossilUIActionAllowsCheckpointButBlocksRecovery(t *testing.T) {
	provider, state, starter := newFossilUITestProvider(t)
	entry := checkpoint.FileState{Path: "protected.txt", StatusCode: "EDITED", Kind: "modified", Exists: false}
	manifest := checkpoint.Manifest{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: "deliberately-stale", Generation: 1, Entries: []checkpoint.FileState{entry},
	}
	if err := provider.checkpoints.ReplaceManifest(manifest, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := openFossilUITestAction(t, provider, state, "protected"); err != nil {
		t.Fatalf("active or stale protection blocked Fossil UI: %v", err)
	}
	provider.StopWorkspaceProcesses(state.workspaceID)

	journal := checkpoint.Journal{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: manifest.Baseline, Phase: "ambiguous", Current: []checkpoint.FileState{entry},
	}
	if err := provider.checkpoints.WriteJournal(journal, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := starter.snapshot()
	_, err := openFossilUITestAction(t, provider, state, "recovery")
	var sourceError *sourcecontrol.Error
	if !errors.As(err, &sourceError) || sourceError.Code != "protected_changes_recovery_required" {
		t.Fatalf("ambiguous recovery error = %#v", err)
	}
	after, _ := starter.snapshot()
	if len(after) != len(before) {
		t.Fatal("Fossil UI launched while protected recovery was ambiguous")
	}
}

func TestFossilUIAvailabilityAndSandboxRecheck(t *testing.T) {
	provider, state, starter := newFossilUITestProvider(t)
	public := state.public(true)
	availability := public.ActionAvailability["open_ui"]
	if availability.Enabled || availability.Diagnostic != "Disable the workspace sandbox first" {
		t.Fatalf("sandbox action availability = %#v", availability)
	}

	config := workspaces.DefaultSandboxConfig()
	config.Enabled = true
	if _, err := provider.workspaces.SetSandboxConfig(state.workspaceID, config); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "sandbox-state")
	sandboxManager := sandbox.NewManager(provider.workspaces, stateRoot, "test", nil)
	provider.sandbox = sandboxManager
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sandboxManager.Shutdown(ctx)
	})

	_, err := openFossilUITestAction(t, provider, state, "sandboxed")
	var sourceError *sourcecontrol.Error
	if !errors.As(err, &sourceError) || sourceError.Code != "fossil_ui_unavailable_in_sandbox" {
		t.Fatalf("sandboxed open_ui error = %#v", err)
	}
	calls, _ := starter.snapshot()
	if len(calls) != 0 {
		t.Fatalf("sandboxed open_ui used host launcher %d time(s)", len(calls))
	}
}

func TestFossilUILifecycleStopsWorkspaceProcesses(t *testing.T) {
	t.Run("reset", func(t *testing.T) {
		provider, state, starter := newFossilUITestProvider(t)
		if _, err := openFossilUITestAction(t, provider, state, "reset"); err != nil {
			t.Fatal(err)
		}
		_, processes := starter.snapshot()
		if err := provider.ResetWorkspace(context.Background(), state.workspaceID); err != nil {
			t.Fatal(err)
		}
		if processes[0].kills.Load() != 1 {
			t.Fatalf("workspace reset kills = %d, want 1", processes[0].kills.Load())
		}
	})

	t.Run("remove", func(t *testing.T) {
		provider, state, starter := newFossilUITestProvider(t)
		if _, err := openFossilUITestAction(t, provider, state, "remove"); err != nil {
			t.Fatal(err)
		}
		_, processes := starter.snapshot()
		provider.RemoveWorkspace(state.workspaceID)
		if processes[0].kills.Load() != 1 {
			t.Fatalf("workspace removal kills = %d, want 1", processes[0].kills.Load())
		}
	})
}
