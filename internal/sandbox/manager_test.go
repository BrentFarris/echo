package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspaces"
)

type fakeWorkspaceResolver struct {
	items map[string]workspaces.Workspace
	err   error
}

func (r fakeWorkspaceResolver) Get(id string) (workspaces.Workspace, bool, error) {
	if r.err != nil {
		return workspaces.Workspace{}, false, r.err
	}
	workspace, ok := r.items[id]
	return workspace, ok, nil
}
func (r fakeWorkspaceResolver) List() ([]workspaces.Workspace, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := make([]workspaces.Workspace, 0, len(r.items))
	for _, workspace := range r.items {
		result = append(result, workspace)
	}
	return result, nil
}

func TestEnabledExecutionTargetFailsClosedAfterConfigurationReadError(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	if err := manager.Start(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	manager.workspaces = fakeWorkspaceResolver{err: errors.New("workspace config became unreadable")}
	if !manager.IsEnabled(workspace.ID) {
		t.Fatal("active sandbox unexpectedly fell back to host execution")
	}
}

type fakeEngine struct {
	ensureCount    atomic.Int32
	startCount     atomic.Int32
	stopCount      atomic.Int32
	updateCount    atomic.Int32
	mu             sync.Mutex
	deleteScopes   []DeleteScope
	execRequests   []ExecRequest
	browserEntered chan struct{}
	browserOnce    sync.Once
	reconcileIDs   []string
	reconcileEnter chan struct{}
	reconcileGo    chan struct{}
	reconcileOnce  sync.Once
}

func (e *fakeEngine) Host(context.Context, ImageSet) HostStatus {
	return HostStatus{Available: true, Supported: true, LinuxEngine: true, Architecture: "amd64"}
}
func (e *fakeEngine) ProbeWorkspace(context.Context, WorkspaceSpec) error             { return nil }
func (e *fakeEngine) Pull(context.Context, ImageSet, func(string, string, int)) error { return nil }
func (e *fakeEngine) Ensure(_ context.Context, _ WorkspaceSpec, state MachineState, _ RuntimeSecrets) (MachineState, error) {
	e.ensureCount.Add(1)
	time.Sleep(time.Millisecond)
	return state, nil
}
func (e *fakeEngine) Start(context.Context, MachineState) error { e.startCount.Add(1); return nil }
func (e *fakeEngine) UpdateResources(context.Context, MachineState, workspaces.SandboxConfig, workspaces.SandboxConfig) error {
	e.updateCount.Add(1)
	return nil
}
func (e *fakeEngine) Stop(context.Context, MachineState) error { e.stopCount.Add(1); return nil }
func (e *fakeEngine) Delete(_ context.Context, _ MachineState, scope DeleteScope) error {
	e.mu.Lock()
	e.deleteScopes = append(e.deleteScopes, scope)
	e.mu.Unlock()
	return nil
}
func (e *fakeEngine) Exec(_ context.Context, _ MachineState, request ExecRequest) (ExecResult, error) {
	e.mu.Lock()
	e.execRequests = append(e.execRequests, request)
	e.mu.Unlock()
	return ExecResult{}, nil
}
func (e *fakeEngine) OpenPTY(context.Context, MachineState, ExecRequest) (PTY, error) {
	return nil, errors.New("not implemented")
}
func (e *fakeEngine) OpenProcess(context.Context, MachineState, ExecRequest) (Process, error) {
	return nil, errors.New("not implemented")
}
func (e *fakeEngine) Usage(context.Context, MachineState) (ResourceUsage, error) {
	return ResourceUsage{MemoryBytes: 64}, nil
}
func (e *fakeEngine) ApplyNetworkGrants(context.Context, MachineState, []NetworkGrant) error {
	return nil
}
func (e *fakeEngine) Heartbeat(context.Context, MachineState) error { return nil }
func (e *fakeEngine) OpenDesktop(context.Context, MachineState) (io.ReadWriteCloser, error) {
	return &fakeStream{}, nil
}
func (e *fakeEngine) BrowserCall(ctx context.Context, _ MachineState, _ string, _ json.RawMessage) (json.RawMessage, error) {
	if e.browserEntered == nil {
		return json.RawMessage(`{}`), nil
	}
	e.browserOnce.Do(func() { close(e.browserEntered) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func (e *fakeEngine) DesktopAction(context.Context, MachineState, DesktopActionRequest) error {
	return nil
}
func (e *fakeEngine) DesktopScreenshot(context.Context, MachineState) ([]byte, string, error) {
	return []byte("image"), "image/png", nil
}
func (e *fakeEngine) ProxyEndpoint(context.Context, MachineState) (string, error) {
	return "http://127.0.0.1:3129", nil
}
func (e *fakeEngine) Reconcile(_ context.Context, _, _ string, ids []string) error {
	if e.reconcileEnter != nil {
		e.reconcileOnce.Do(func() { close(e.reconcileEnter) })
	}
	if e.reconcileGo != nil {
		<-e.reconcileGo
	}
	e.mu.Lock()
	e.reconcileIDs = append([]string(nil), ids...)
	e.mu.Unlock()
	return nil
}

func TestStartupReconciliationCompletesBeforeFirstContainerStart(t *testing.T) {
	engine := &fakeEngine{reconcileEnter: make(chan struct{}), reconcileGo: make(chan struct{})}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	started := make(chan error, 1)
	go func() { started <- manager.Start(context.Background(), workspace.ID) }()
	select {
	case <-engine.reconcileEnter:
	case <-time.After(3 * time.Second):
		t.Fatal("startup reconciliation did not begin")
	}
	if engine.ensureCount.Load() != 0 || engine.startCount.Load() != 0 {
		t.Fatal("sandbox started before startup reconciliation completed")
	}
	close(engine.reconcileGo)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if engine.ensureCount.Load() != 1 || engine.startCount.Load() != 1 {
		t.Fatal("sandbox did not start after startup reconciliation")
	}
}

func TestDisabledImagePullDoesNotBecomeFailClosedSandboxPolicy(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	workspace.Sandbox.Enabled = false
	manager.workspaces = fakeWorkspaceResolver{items: map[string]workspaces.Workspace{workspace.ID: workspace}}
	if err := manager.Pull(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	manager.workspaces = fakeWorkspaceResolver{err: errors.New("workspace config became unreadable")}
	if manager.IsEnabled(workspace.ID) {
		t.Fatal("pulling images for a disabled workspace changed its execution policy")
	}
}

func TestExecutionTargetTransitionFailsClosed(t *testing.T) {
	manager, workspace, _ := newSandboxManagerForTest(t, &fakeEngine{})
	if err := manager.BeginPolicyTransition(workspace.ID, false); err != nil {
		t.Fatal(err)
	}
	if !manager.IsEnabled(workspace.ID) {
		t.Fatal("transition did not force process callers onto the fail-closed sandbox route")
	}
	if err := manager.Start(context.Background(), workspace.ID); !errors.Is(err, ErrPolicyTransition) {
		t.Fatalf("start during transition returned %v", err)
	}
	if err := manager.BeginPolicyTransition(workspace.ID, false); !errors.Is(err, ErrPolicyTransition) {
		t.Fatalf("competing transition returned %v", err)
	}
	workspace.Sandbox.Enabled = false
	manager.workspaces = fakeWorkspaceResolver{items: map[string]workspaces.Workspace{workspace.ID: workspace}}
	manager.EndPolicyTransition(workspace.ID, false)
	if manager.IsEnabled(workspace.ID) {
		t.Fatal("committed disabled policy was not published after the transition")
	}
}
func (e *fakeEngine) Close() error { return nil }

type fakeStream struct{ bytes.Buffer }

func (*fakeStream) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHTTPActivityTracksResponseBodyLifetime(t *testing.T) {
	var active atomic.Int32
	transport := &activityRoundTripper{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString("response"))}, nil
		}),
		begin: func() { active.Add(1) }, end: func() { active.Add(-1) },
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	response, err := transport.RoundTrip(request)
	if err != nil || active.Load() != 1 {
		t.Fatalf("request was not tracked while its body was open: active=%d err=%v", active.Load(), err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 {
		t.Fatalf("request activity survived response EOF: %d", active.Load())
	}
	_ = response.Body.Close()
	if active.Load() != 0 {
		t.Fatalf("response close decremented activity twice: %d", active.Load())
	}
}

func newSandboxManagerForTest(t *testing.T, engine *fakeEngine) (*Manager, workspaces.Workspace, string) {
	t.Helper()
	root := t.TempDir()
	main := filepath.Join(root, "workspace with spaces Ω")
	if err := os.MkdirAll(filepath.Join(main, ".echo", "sandbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := workspaces.Workspace{ID: "workspace-one", Name: "Workspace", MainPath: main, Folders: []string{main}, Sandbox: workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 30}}
	manager := NewManager(fakeWorkspaceResolver{items: map[string]workspaces.Workspace{workspace.ID: workspace}}, filepath.Join(root, "state"), "installation", engine)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return manager, workspace, main
}

func TestManagerStartIsConcurrentAndIdempotent(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 24)
	for index := 0; index < 24; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); errorsChannel <- manager.Start(context.Background(), workspace.ID) }()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := engine.ensureCount.Load(); got != 1 {
		t.Fatalf("Ensure called %d times", got)
	}
	if got := engine.startCount.Load(); got != 1 {
		t.Fatalf("Start called %d times", got)
	}
	status, err := manager.Status(context.Background(), workspace.ID)
	if err != nil || status.State != StateReady {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestReadyManagerAppliesResourceChanges(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	if err := manager.Start(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	next := workspace.Sandbox
	next.CPULimit = 8
	next.MemoryMiB = 8192
	if err := manager.UpdateResources(context.Background(), workspace.ID, next); err != nil {
		t.Fatal(err)
	}
	if engine.updateCount.Load() != 1 {
		t.Fatalf("resource update calls = %d", engine.updateCount.Load())
	}
}

func TestManagerResetAndDeleteBoundariesRetainWorkspaceFiles(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, main := newSandboxManagerForTest(t, engine)
	sentinel := filepath.Join(main, "must-survive.txt")
	if err := os.WriteFile(sentinel, []byte("host file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(context.Background(), workspace.ID, "workbench"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(context.Background(), workspace.ID, "browser"); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	scopes := append([]DeleteScope(nil), engine.deleteScopes...)
	engine.mu.Unlock()
	if len(scopes) != 2 || !scopes[0].Containers || !scopes[0].Workbench || scopes[0].Browser || !scopes[1].Browser || scopes[1].Workbench {
		t.Fatalf("unexpected reset scopes: %+v", scopes)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("reset touched host workspace file: %v", err)
	}
	if err := manager.Delete(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("delete touched host workspace file: %v", err)
	}
	if _, exists, err := manager.store.Load(workspace.ID); err != nil || exists {
		t.Fatalf("machine state retained after delete: %v %v", exists, err)
	}
	engine.mu.Lock()
	full := engine.deleteScopes[len(engine.deleteScopes)-1]
	engine.mu.Unlock()
	if !full.Containers || !full.Network || !full.Workbench || !full.Desktop || !full.Browser || !full.Exchange {
		t.Fatalf("delete scope was incomplete: %+v", full)
	}
}

func TestSetupRecipeRequiresDigestApprovalAndRunsOnlyApprovedRecipeAsRoot(t *testing.T) {
	engine := &fakeEngine{}
	manager, workspace, main := newSandboxManagerForTest(t, engine)
	recipe := filepath.Join(main, filepath.FromSlash(SetupRecipePath))
	if err := os.WriteFile(recipe, []byte("#!/bin/bash\necho setup\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	approval, err := manager.RunSetup(context.Background(), workspace.ID, "")
	if !errors.Is(err, ErrSetupApproval) || approval.State != "approval_required" || approval.RecipeDigest == "" {
		t.Fatalf("unexpected approval result: %+v %v", approval, err)
	}
	completed, err := manager.RunSetup(context.Background(), workspace.ID, approval.RecipeDigest)
	if err != nil || completed.State != "succeeded" {
		t.Fatalf("setup result: %+v %v", completed, err)
	}
	engine.mu.Lock()
	requests := append([]ExecRequest(nil), engine.execRequests...)
	engine.mu.Unlock()
	if len(requests) != 2 || requests[0].Role != "workbench" || requests[1].Role != "desktop" {
		t.Fatalf("setup roles: %+v", requests)
	}
	for _, request := range requests {
		if !request.Root {
			t.Fatal("approved setup did not request root execution")
		}
		if string(request.Input) != "#!/bin/bash\necho setup\n" || len(request.Command) < 2 || request.Command[1] != "-s" {
			t.Fatalf("setup did not execute the exact approved bytes through Bash: %+v", request)
		}
	}

	if err := manager.Recreate(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	requests = append([]ExecRequest(nil), engine.execRequests...)
	engine.mu.Unlock()
	if len(requests) != 4 || requests[2].Role != "workbench" || requests[3].Role != "desktop" {
		t.Fatalf("approved setup was not reapplied after recreation: %+v", requests)
	}

	if err := os.WriteFile(recipe, []byte("#!/bin/bash\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(context.Background(), workspace.ID, "browser"); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	requests = append([]ExecRequest(nil), engine.execRequests...)
	engine.mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("changed setup ran without renewed approval: %+v", requests)
	}
	status, err := manager.Status(context.Background(), workspace.ID)
	if err != nil || status.Setup.State != "approval_required" || status.Setup.RecipeDigest == completed.RecipeDigest {
		t.Fatalf("changed setup was not exposed for approval: %+v, %v", status.Setup, err)
	}
}

func TestUserTakeoverCancelsAIActionAndDesktopSessionIsOneUse(t *testing.T) {
	engine := &fakeEngine{browserEntered: make(chan struct{})}
	manager, workspace, _ := newSandboxManagerForTest(t, engine)
	result := make(chan error, 1)
	go func() {
		_, err := manager.BrowserCall(context.Background(), workspace.ID, "turn-one", "snapshot", json.RawMessage(`{}`))
		result <- err
	}()
	select {
	case <-engine.browserEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("browser action did not start")
	}
	if _, err := manager.TakeUserControl(workspace.ID, "browser-session", false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrUserControlActive) {
			t.Fatalf("action error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("takeover did not cancel action")
	}
	if _, err := manager.ReleaseUserControl(workspace.ID, "browser-session"); err != nil {
		t.Fatal(err)
	}

	session, err := manager.CreateDesktopSession(context.Background(), workspace.ID, "browser-session")
	if err != nil || session.Credential == "" {
		t.Fatalf("desktop session = %+v, %v", session, err)
	}
	if status, statusErr := manager.Status(context.Background(), workspace.ID); statusErr != nil || status.ActiveViewers != 0 {
		t.Fatalf("unused connection token counted as a live viewer: %+v, %v", status, statusErr)
	}
	stream, err := manager.OpenDesktop(context.Background(), workspace.ID, session.ID, "browser-session")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.TakeUserControl(workspace.ID, "browser-session", false)
	if err != nil || !lease.ExpiresAt.IsZero() {
		t.Fatalf("connected user's lease should not expire: %+v, %v", lease, err)
	}
	if _, err := manager.OpenDesktop(context.Background(), workspace.ID, session.ID, "browser-session"); err == nil {
		t.Fatal("desktop connection token was reusable")
	}
	_ = stream.Close()
	manager.CloseDesktopConnection(workspace.ID, session.ID, "browser-session")
	status, _ := manager.Status(context.Background(), workspace.ID)
	if status.ActiveViewers != 0 {
		t.Fatalf("viewer was not cleaned up: %+v", status)
	}
	if status.DesktopLease.Owner != LeaseUser || status.DesktopLease.ExpiresAt.IsZero() {
		t.Fatalf("disconnected controller did not receive grace period: %+v", status.DesktopLease)
	}
}

func TestNetworkGrantValidationRejectsWildcardsReservedAliasesAndMissingLabels(t *testing.T) {
	valid := NetworkGrant{Host: "printer.office.example", Port: 443, Label: "Office printer", SandboxAlias: "printer"}
	if err := ValidateNetworkGrant(valid); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	for _, grant := range []NetworkGrant{
		{Host: "*.example.com", Port: 443, Label: "wildcard"},
		{Host: "10.0.0.8", Port: 443},
		{Host: "10.0.0.8", Port: 443, Label: "reserved", SandboxAlias: "gateway"},
		{Host: "10.0.0.8", Port: 0, Label: "bad port"},
		{Host: "10.0.0.0/24", Port: 443, Label: "subnet"},
	} {
		if err := ValidateNetworkGrant(grant); err == nil {
			t.Fatalf("invalid grant accepted: %+v", grant)
		}
	}
}

func TestGeneratedRuntimeCredentialsAreIndependent(t *testing.T) {
	manager, workspace, _ := newSandboxManagerForTest(t, &fakeEngine{})
	secret, err := manager.credentials(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{secret.WorkbenchAgentToken, secret.DesktopAgentToken, secret.ProxyToken, secret.BrowserToken, secret.VNCToken}
	seen := map[string]bool{}
	for _, value := range values {
		if len(value) < 20 || seen[value] {
			t.Fatalf("runtime credential is short or reused: lengths=%v", []int{len(values[0]), len(values[1]), len(values[2]), len(values[3]), len(values[4])})
		}
		seen[value] = true
	}
}

func TestReconcileTreatsDisabledWorkspaceContainersAsOrphaned(t *testing.T) {
	engine := &fakeEngine{}
	manager, enabled, _ := newSandboxManagerForTest(t, engine)
	disabled := enabled
	disabled.ID = "workspace-disabled"
	disabled.Sandbox.Enabled = false
	manager.workspaces = fakeWorkspaceResolver{items: map[string]workspaces.Workspace{enabled.ID: enabled, disabled.ID: disabled}}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	ids := append([]string(nil), engine.reconcileIDs...)
	engine.mu.Unlock()
	if len(ids) != 1 || ids[0] != enabled.ID {
		t.Fatalf("reconcile retained disabled workspace containers: %v", ids)
	}
}
