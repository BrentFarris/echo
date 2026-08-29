package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

type workspaceResolver interface {
	Get(string) (workspaces.Workspace, bool, error)
	List() ([]workspaces.Workspace, error)
}

type runtimeState struct {
	status     SandboxStatus
	lastActive time.Time
	active     int
	viewers    map[string]DesktopSession
	connected  map[string]bool
	lease      DesktopLease
	guiCancel  context.CancelFunc
	guiContext context.Context
}

type Manager struct {
	workspaces   workspaceResolver
	store        *StateStore
	engine       Engine
	installation string
	images       ImageSet

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	runtime map[string]*runtimeState
	secrets map[string]RuntimeSecrets
	policy  map[string]bool
	notify  func(Event)
	closed  chan struct{}
	done    chan struct{}

	reconcileOnce sync.Once
	reconcileDone chan struct{}
	reconcileErr  error
}

func NewManager(workspaceManager workspaceResolver, stateRoot, installation string, engine Engine) *Manager {
	if engine == nil {
		engine = NewUnavailableEngine(fmt.Errorf("sandbox engine is not configured"))
	}
	manager := &Manager{
		workspaces: workspaceManager, store: NewStateStore(stateRoot), engine: engine,
		installation: installation, images: BuildImages(), locks: make(map[string]*sync.Mutex),
		runtime: make(map[string]*runtimeState), secrets: make(map[string]RuntimeSecrets), policy: make(map[string]bool),
		closed: make(chan struct{}), done: make(chan struct{}), reconcileDone: make(chan struct{}),
	}
	go manager.maintenance()
	return manager
}

func StateRootForSettings(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "sandboxes")
}

func (m *Manager) SetNotifier(notify func(Event)) {
	m.mu.Lock()
	m.notify = notify
	m.mu.Unlock()
}

func (m *Manager) lock(workspaceID string) func() {
	m.mu.Lock()
	lock := m.locks[workspaceID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[workspaceID] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (m *Manager) runtimeFor(workspaceID string) *runtimeState {
	runtime := m.runtime[workspaceID]
	if runtime == nil {
		runtime = &runtimeState{
			lastActive: time.Now(), viewers: make(map[string]DesktopSession), connected: make(map[string]bool),
			lease:  DesktopLease{Owner: LeaseNone},
			status: SandboxStatus{State: StateStopped, ProtocolVersion: ProtocolVersion, ImageVersion: m.images, ControlOwner: LeaseNone},
		}
		m.runtime[workspaceID] = runtime
	}
	return runtime
}

func (m *Manager) workspace(workspaceID string) (workspaces.Workspace, error) {
	workspace, ok, err := m.workspaces.Get(strings.TrimSpace(workspaceID))
	if err != nil {
		return workspaces.Workspace{}, err
	}
	if !ok {
		return workspaces.Workspace{}, &Error{Code: "workspace_not_found", Message: "workspace was not found"}
	}
	return workspace, nil
}

func (m *Manager) IsEnabled(workspaceID string) bool {
	m.mu.Lock()
	_, transitioning := m.policy[workspaceID]
	m.mu.Unlock()
	if transitioning {
		// Force every process-capable caller down the sandbox route while Start
		// rejects new work. This is the fail-closed handoff between host and guest.
		return true
	}
	workspace, err := m.workspace(workspaceID)
	if err == nil {
		m.mu.Lock()
		m.runtimeFor(workspaceID).status.Enabled = workspace.Sandbox.Enabled
		m.mu.Unlock()
		return workspace.Sandbox.Enabled
	}
	// Once a workspace has entered sandbox execution, a transient or malformed
	// portable config must fail closed. An explicit successful disable still
	// wins through the branch above.
	m.mu.Lock()
	runtime := m.runtime[workspaceID]
	enabled := runtime != nil && runtime.status.Enabled
	m.mu.Unlock()
	return enabled
}

// BeginPolicyTransition blocks new process starts while the server cancels
// the old target and atomically persists the new portable workspace policy.
func (m *Manager) BeginPolicyTransition(workspaceID string, targetEnabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.policy[workspaceID]; exists {
		return ErrPolicyTransition
	}
	m.policy[workspaceID] = targetEnabled
	return nil
}

// EndPolicyTransition publishes either the committed target or the restored
// prior policy after a failed configuration update.
func (m *Manager) EndPolicyTransition(workspaceID string, enabled bool) {
	m.mu.Lock()
	delete(m.policy, workspaceID)
	runtime := m.runtimeFor(workspaceID)
	runtime.status.Enabled = enabled
	if !enabled {
		runtime.status.State = StateDisabled
	} else if runtime.status.State == StateDisabled {
		runtime.status.State = StateStopped
	}
	m.mu.Unlock()
}

func (m *Manager) policyTransitioning(workspaceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.policy[workspaceID]
	return exists
}

func (m *Manager) HostToGuest(workspaceID, hostPath string) (string, error) {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return "", err
	}
	roots, err := WorkspaceMounts(workspace)
	if err != nil {
		return "", err
	}
	return NewPathMapper(roots).HostToGuest(hostPath)
}

func (m *Manager) GuestToHost(workspaceID, guestPath string) (string, error) {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return "", err
	}
	roots, err := WorkspaceMounts(workspace)
	if err != nil {
		return "", err
	}
	return NewPathMapper(roots).GuestToHost(guestPath)
}

func (m *Manager) PathMapper(workspaceID string) (PathMapper, error) {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return PathMapper{}, err
	}
	roots, err := WorkspaceMounts(workspace)
	if err != nil {
		return PathMapper{}, err
	}
	return NewPathMapper(roots), nil
}

func (m *Manager) HTTPClient(ctx context.Context, workspaceID string, timeout time.Duration) (*http.Client, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return nil, err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return nil, err
	}
	endpoint, err := m.engine.ProxyEndpoint(ctx, state)
	if err != nil {
		return nil, err
	}
	secrets, err := m.credentials(workspaceID)
	if err != nil {
		return nil, err
	}
	proxyURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	proxyURL.User = url.UserPassword("echo", secrets.ProxyToken)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	tracked := &activityRoundTripper{
		base:  transport,
		begin: func() { m.touch(workspaceID, 1) },
		end:   func() { m.touch(workspaceID, -1) },
	}
	return &http.Client{Transport: tracked, Timeout: timeout}, nil
}

type activityRoundTripper struct {
	base       http.RoundTripper
	begin, end func()
}

func (t *activityRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.begin()
	response, err := t.base.RoundTrip(request)
	if err != nil {
		t.end()
		return nil, err
	}
	if response.Body == nil {
		t.end()
		return response, nil
	}
	response.Body = &activityResponseBody{ReadCloser: response.Body, done: t.end}
	return response, nil
}

type activityResponseBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (body *activityResponseBody) finish() { body.once.Do(body.done) }
func (body *activityResponseBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if err != nil {
		body.finish()
	}
	return count, err
}
func (body *activityResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.finish()
	return err
}

func (m *Manager) Host(ctx context.Context) HostStatus {
	status := m.engine.Host(ctx, m.images)
	status.ProtocolVersion = ProtocolVersion
	status.ImagesImmutable = m.images.Immutable()
	if status.Images == nil {
		status.Images = map[string]ImageStatus{}
	}
	return status
}

// Preflight validates the Docker host, immutable images, and bind-mount access
// before enabled=true is persisted.
func (m *Manager) Preflight(ctx context.Context, workspaceID string, config workspaces.SandboxConfig) (HostStatus, error) {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return HostStatus{}, ctx.Err()
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return HostStatus{}, err
	}
	normalized, err := workspaces.NormalizeSandboxConfig(config)
	if err != nil {
		return HostStatus{}, &Error{Code: "invalid_sandbox_config", Message: err.Error()}
	}
	host := m.Host(ctx)
	if !host.Available || !host.Supported {
		return host, &Error{Code: host.ErrorCode, Message: host.Message}
	}
	for _, image := range host.Images {
		if !image.Present {
			return host, &Error{Code: "sandbox_images_missing", Message: "pull all sandbox images before enabling the sandbox"}
		}
	}
	workspace.Sandbox = normalized
	spec, err := m.spec(workspace)
	if err != nil {
		return host, err
	}
	if err := m.engine.ProbeWorkspace(ctx, spec); err != nil {
		return host, err
	}
	return host, nil
}

func (m *Manager) Status(ctx context.Context, workspaceID string) (SandboxStatus, error) {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return SandboxStatus{}, err
	}
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	runtime.status.Enabled = workspace.Sandbox.Enabled
	m.mu.Unlock()
	if !workspace.Sandbox.Enabled {
		return SandboxStatus{State: StateDisabled, Enabled: false, ProtocolVersion: ProtocolVersion, ImageVersion: m.images, ControlOwner: LeaseNone, UpdatedAt: time.Now().UTC()}, nil
	}
	m.mu.Lock()
	runtime = m.runtimeFor(workspaceID)
	status := runtime.status
	status.Enabled = true
	status.ActiveViewers = connectedViewerCount(runtime)
	status.DesktopLease = runtime.lease
	status.ControlOwner = runtime.lease.Owner
	m.mu.Unlock()
	if status.State == "" || status.State == StateStopped {
		if state, exists, loadErr := m.store.Load(workspaceID); loadErr == nil && exists {
			status.Setup = state.LastSetup
			status.State = StateStopped
		}
	}
	if status.State == StateReady {
		if state, exists, loadErr := m.store.Load(workspaceID); loadErr == nil && exists {
			if usage, usageErr := m.engine.Usage(ctx, state); usageErr == nil {
				status.Resources = usage
			}
		}
	}
	status.UpdatedAt = time.Now().UTC()
	return status, nil
}

func (m *Manager) spec(workspace workspaces.Workspace) (WorkspaceSpec, error) {
	mounts, err := WorkspaceMounts(workspace)
	if err != nil {
		return WorkspaceSpec{}, err
	}
	return WorkspaceSpec{
		ID: workspace.ID, Config: workspace.Sandbox, Roots: mounts,
		SetupPath:    filepath.Join(workspace.MainPath, filepath.FromSlash(SetupRecipePath)),
		Installation: m.installation,
	}, nil
}

func (m *Manager) machineState(workspaceID string) (MachineState, error) {
	state, exists, err := m.store.Load(workspaceID)
	if err != nil {
		return MachineState{}, err
	}
	if !exists {
		state = DefaultMachineState(m.installation, workspaceID, m.images)
	}
	return state, nil
}

func (m *Manager) credentials(workspaceID string) (RuntimeSecrets, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if secret := m.secrets[workspaceID]; secret.WorkbenchAgentToken != "" {
		return secret, nil
	}
	randomHex := func() (string, error) {
		buffer := make([]byte, 32)
		if _, err := rand.Read(buffer); err != nil {
			return "", err
		}
		return hex.EncodeToString(buffer), nil
	}
	secret := RuntimeSecrets{}
	values := []*string{&secret.WorkbenchAgentToken, &secret.DesktopAgentToken, &secret.ProxyToken, &secret.BrowserToken}
	for _, target := range values {
		value, err := randomHex()
		if err != nil {
			return RuntimeSecrets{}, fmt.Errorf("generate sandbox credential: %w", err)
		}
		*target = value
	}
	vncBytes := make([]byte, 18)
	if _, err := rand.Read(vncBytes); err != nil {
		return RuntimeSecrets{}, fmt.Errorf("generate sandbox credential: %w", err)
	}
	secret.VNCToken = base64.RawURLEncoding.EncodeToString(vncBytes)
	m.secrets[workspaceID] = secret
	return secret, nil
}

func (m *Manager) transition(workspaceID string, state State, code, message string) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	runtime.status.State = state
	if state == StateDisabled {
		runtime.status.Enabled = false
	}
	runtime.status.ErrorCode = code
	runtime.status.Message = message
	runtime.status.UpdatedAt = time.Now().UTC()
	status := runtime.status
	status.ActiveViewers = connectedViewerCount(runtime)
	status.DesktopLease = runtime.lease
	status.ControlOwner = runtime.lease.Owner
	notify := m.notify
	m.mu.Unlock()
	if notify != nil {
		notify(Event{Type: "sandbox_event", WorkspaceID: workspaceID, Event: "status", Status: &status, At: time.Now().UTC()})
	}
}

func (m *Manager) emit(event Event) {
	event.Type = "sandbox_event"
	event.At = time.Now().UTC()
	m.mu.Lock()
	notify := m.notify
	m.mu.Unlock()
	if notify != nil {
		notify(event)
	}
}

func (m *Manager) Pull(ctx context.Context, workspaceID string) error {
	if _, err := m.workspace(workspaceID); err != nil {
		return err
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	m.transition(workspaceID, StatePulling, "", "Pulling sandbox images")
	err := m.engine.Pull(ctx, m.images, func(role, message string, progress int) {
		m.emit(Event{WorkspaceID: workspaceID, Event: "image_pull", Role: role, Message: message, Progress: progress})
	})
	if err != nil {
		m.transition(workspaceID, StateError, ErrorCode(err), "Sandbox image pull failed")
		return err
	}
	m.transition(workspaceID, StateStopped, "", "Sandbox images are ready")
	return nil
}

func (m *Manager) Start(ctx context.Context, workspaceID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if m.policyTransitioning(workspaceID) {
		return ErrPolicyTransition
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	if !workspace.Sandbox.Enabled {
		return ErrDisabled
	}
	m.mu.Lock()
	m.runtimeFor(workspaceID).status.Enabled = true
	m.mu.Unlock()
	unlock := m.lock(workspaceID)
	defer unlock()
	m.mu.Lock()
	current := m.runtimeFor(workspaceID).status.State
	m.mu.Unlock()
	if current == StateReady {
		m.touch(workspaceID, 0)
		return nil
	}
	host := m.Host(ctx)
	if !host.Available || !host.Supported {
		message := host.Message
		if message == "" {
			message = ErrUnavailable.Message
		}
		m.transition(workspaceID, StateUnavailable, host.ErrorCode, message)
		return &Error{Code: host.ErrorCode, Message: message}
	}
	spec, err := m.spec(workspace)
	if err != nil {
		return err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	if state.ProtocolVersion != "" && state.ProtocolVersion != ProtocolVersion {
		m.transition(workspaceID, StateError, ErrProtocolMismatch.Code, ErrProtocolMismatch.Message)
		return ErrProtocolMismatch
	}
	secrets, err := m.credentials(workspaceID)
	if err != nil {
		return err
	}
	m.transition(workspaceID, StateCreating, "", "Creating sandbox")
	state, err = m.engine.Ensure(ctx, spec, state, secrets)
	if err != nil {
		m.transition(workspaceID, StateError, ErrorCode(err), "Could not create sandbox")
		return err
	}
	state.ProtocolVersion = ProtocolVersion
	state.Images = m.images
	if err := m.store.Save(state); err != nil {
		return err
	}
	m.transition(workspaceID, StateStarting, "", "Starting sandbox")
	if err := m.engine.Start(ctx, state); err != nil {
		m.transition(workspaceID, StateError, ErrorCode(err), "Could not start sandbox")
		return err
	}
	m.touch(workspaceID, 0)
	m.transition(workspaceID, StateReady, "", "Sandbox is ready")
	return nil
}

func (m *Manager) Stop(ctx context.Context, workspaceID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	if !workspace.Sandbox.Enabled {
		return nil
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	m.transition(workspaceID, StateStopping, "", "Stopping sandbox")
	if err := m.engine.Stop(ctx, state); err != nil {
		m.transition(workspaceID, StateError, ErrorCode(err), "Could not stop sandbox")
		return err
	}
	m.mu.Lock()
	delete(m.secrets, workspaceID)
	m.mu.Unlock()
	m.transition(workspaceID, StateStopped, "", "Sandbox stopped")
	return nil
}

// StopRetainingData stops sandbox containers for a workspace ID without
// resolving the workspace registration and without deleting its persistent
// volumes or machine state. This allows unavailable workspaces to be safely
// unregistered.
func (m *Manager) StopRetainingData(ctx context.Context, workspaceID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, exists, err := m.store.Load(workspaceID)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	m.transition(workspaceID, StateStopping, "", "Stopping sandbox")
	if err := m.engine.Stop(ctx, state); err != nil {
		m.transition(workspaceID, StateError, ErrorCode(err), "Could not stop sandbox")
		return err
	}
	m.mu.Lock()
	delete(m.secrets, workspaceID)
	m.mu.Unlock()
	m.transition(workspaceID, StateStopped, "", "Sandbox stopped")
	return nil
}

func (m *Manager) UpdateResources(ctx context.Context, workspaceID string, next workspaces.SandboxConfig) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	next, err = workspaces.NormalizeSandboxConfig(next)
	if err != nil {
		return &Error{Code: "invalid_sandbox_config", Message: err.Error()}
	}
	if workspace.Sandbox.CPULimit == next.CPULimit && workspace.Sandbox.MemoryMiB == next.MemoryMiB {
		return nil
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	m.mu.Lock()
	ready := m.runtimeFor(workspaceID).status.State == StateReady
	m.mu.Unlock()
	if !ready {
		return nil
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	if err := m.engine.UpdateResources(ctx, state, workspace.Sandbox, next); err != nil {
		return err
	}
	m.emit(Event{WorkspaceID: workspaceID, Event: "resources", Message: "Sandbox resource limits updated"})
	return nil
}

func (m *Manager) Recreate(ctx context.Context, workspaceID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	if err := m.engine.Delete(ctx, state, DeleteScope{Containers: true, Network: true}); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.secrets, workspaceID)
	m.runtimeFor(workspaceID).status.State = StateStopped
	m.mu.Unlock()
	if err := m.startLocked(ctx, workspaceID); err != nil {
		return err
	}
	return m.rerunApprovedSetupLocked(ctx, workspaceID)
}

func (m *Manager) startLocked(ctx context.Context, workspaceID string) error {
	// The caller owns the workspace lock, so perform Start's body after briefly
	// releasing it would race. Recreate is uncommon; mark stopped and duplicate
	// the safe public transition through a goroutine-free unlock/relock boundary.
	workspace, err := m.workspace(workspaceID)
	if err != nil || !workspace.Sandbox.Enabled {
		if err != nil {
			return err
		}
		return ErrDisabled
	}
	m.mu.Lock()
	m.runtimeFor(workspaceID).status.Enabled = true
	m.mu.Unlock()
	spec, err := m.spec(workspace)
	if err != nil {
		return err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	secrets, err := m.credentials(workspaceID)
	if err != nil {
		return err
	}
	m.transition(workspaceID, StateCreating, "", "Recreating sandbox")
	state, err = m.engine.Ensure(ctx, spec, state, secrets)
	if err != nil {
		return err
	}
	if err := m.store.Save(state); err != nil {
		return err
	}
	m.transition(workspaceID, StateStarting, "", "Starting sandbox")
	if err := m.engine.Start(ctx, state); err != nil {
		return err
	}
	m.transition(workspaceID, StateReady, "", "Sandbox is ready")
	return nil
}

func (m *Manager) Reset(ctx context.Context, workspaceID, scope string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	deleteScope := DeleteScope{Containers: true}
	switch scope {
	case "workbench":
		deleteScope.Workbench = true
	case "browser":
		deleteScope.Browser = true
	default:
		return &Error{Code: "invalid_reset_scope", Message: "reset scope must be workbench or browser"}
	}
	if err := m.engine.Delete(ctx, state, deleteScope); err != nil {
		return err
	}
	if err := m.store.Save(state); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.secrets, workspaceID)
	m.runtimeFor(workspaceID).status.State = StateStopped
	m.mu.Unlock()
	if err := m.startLocked(ctx, workspaceID); err != nil {
		return err
	}
	return m.rerunApprovedSetupLocked(ctx, workspaceID)
}

func (m *Manager) Delete(ctx context.Context, workspaceID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	_ = workspace // Workspace resolution proves the target is registered.
	unlock := m.lock(workspaceID)
	defer unlock()
	state, exists, err := m.store.Load(workspaceID)
	if err != nil {
		return err
	}
	if !exists {
		state = DefaultMachineState(m.installation, workspaceID, m.images)
	}
	if err := m.engine.Delete(ctx, state, DeleteScope{Containers: true, Network: true, Workbench: true, Desktop: true, Browser: true, Exchange: true}); err != nil {
		return err
	}
	if err := m.store.Delete(workspaceID); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.secrets, workspaceID)
	delete(m.runtime, workspaceID)
	m.mu.Unlock()
	m.emit(Event{WorkspaceID: workspaceID, Event: "deleted", Message: "Sandbox data was deleted; workspace files were retained"})
	return nil
}

func (m *Manager) Execute(ctx context.Context, workspaceID string, request ExecRequest) (ExecResult, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return ExecResult{}, err
	}
	m.touch(workspaceID, 1)
	defer m.touch(workspaceID, -1)
	state, err := m.machineState(workspaceID)
	if err != nil {
		return ExecResult{}, err
	}
	return m.engine.Exec(ctx, state, request)
}

func (m *Manager) OpenPTY(ctx context.Context, workspaceID string, request ExecRequest) (PTY, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return nil, err
	}
	m.touch(workspaceID, 1)
	state, err := m.machineState(workspaceID)
	if err != nil {
		m.touch(workspaceID, -1)
		return nil, err
	}
	pty, err := m.engine.OpenPTY(ctx, state, request)
	if err != nil {
		m.touch(workspaceID, -1)
		return nil, err
	}
	return &activityPTY{PTY: pty, done: func() { m.touch(workspaceID, -1) }}, nil
}

func (m *Manager) OpenProcess(ctx context.Context, workspaceID string, request ExecRequest) (Process, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return nil, err
	}
	m.touch(workspaceID, 1)
	state, err := m.machineState(workspaceID)
	if err != nil {
		m.touch(workspaceID, -1)
		return nil, err
	}
	process, err := m.engine.OpenProcess(ctx, state, request)
	if err != nil {
		m.touch(workspaceID, -1)
		return nil, err
	}
	return &activityProcess{Process: process, done: func() { m.touch(workspaceID, -1) }}, nil
}

type activityProcess struct {
	Process
	once sync.Once
	done func()
}

func (p *activityProcess) finish() { p.once.Do(p.done) }
func (p *activityProcess) Wait() (int, error) {
	code, err := p.Process.Wait()
	p.finish()
	return code, err
}
func (p *activityProcess) Kill() error { err := p.Process.Kill(); p.finish(); return err }

type activityPTY struct {
	PTY
	once sync.Once
	done func()
}

func (p *activityPTY) finish()            { p.once.Do(p.done) }
func (p *activityPTY) Close() error       { err := p.PTY.Close(); p.finish(); return err }
func (p *activityPTY) Wait() (int, error) { code, err := p.PTY.Wait(); p.finish(); return code, err }
func (p *activityPTY) Kill() error        { err := p.PTY.Kill(); p.finish(); return err }

func (m *Manager) touch(workspaceID string, delta int) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	runtime.active += delta
	if runtime.active < 0 {
		runtime.active = 0
	}
	runtime.lastActive = time.Now()
	m.mu.Unlock()
}

func readSetupRecipe(setupPath string) (string, []byte, bool, error) {
	info, err := os.Lstat(setupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, false, fmt.Errorf("sandbox setup recipe must be a regular file, not a symlink or directory")
	}
	if info.Size() > 1<<20 {
		return "", nil, false, fmt.Errorf("sandbox setup recipe exceeds the 1 MiB limit")
	}
	data, err := os.ReadFile(setupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}
	if len(data) > 1<<20 {
		return "", nil, false, fmt.Errorf("sandbox setup recipe exceeds the 1 MiB limit")
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), data, true, nil
}

func setupDigest(setupPath string) (string, bool, error) {
	digest, _, exists, err := readSetupRecipe(setupPath)
	return digest, exists, err
}

func (m *Manager) RunSetup(ctx context.Context, workspaceID, approvedDigest string) (SetupStatus, error) {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return SetupStatus{}, err
	}
	if !workspace.Sandbox.Enabled {
		return SetupStatus{}, ErrDisabled
	}
	spec, err := m.spec(workspace)
	if err != nil {
		return SetupStatus{}, err
	}
	digest, exists, err := setupDigest(spec.SetupPath)
	if err != nil {
		return SetupStatus{}, err
	}
	if !exists {
		return SetupStatus{}, &Error{Code: "setup_recipe_missing", Message: "sandbox setup recipe was not found"}
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return SetupStatus{}, err
	}
	if state.ApprovedSetupDigest != digest {
		if approvedDigest != digest {
			return SetupStatus{RecipeDigest: digest, ApprovedDigest: state.ApprovedSetupDigest, State: "approval_required"}, ErrSetupApproval
		}
		state.ApprovedSetupDigest = digest
		if err := m.store.Save(state); err != nil {
			return SetupStatus{}, err
		}
	}
	if err := m.startLocked(ctx, workspaceID); err != nil {
		return SetupStatus{}, err
	}
	state, err = m.machineState(workspaceID)
	if err != nil {
		return SetupStatus{}, err
	}
	return m.runSetupLocked(ctx, workspaceID, spec, state, digest)
}

// rerunApprovedSetupLocked restores reproducible root-layer customization
// after containers have been replaced. A recipe that changed since approval is
// reported as pending and is never executed implicitly.
func (m *Manager) rerunApprovedSetupLocked(ctx context.Context, workspaceID string) error {
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	spec, err := m.spec(workspace)
	if err != nil {
		return err
	}
	digest, exists, err := setupDigest(spec.SetupPath)
	if err != nil || !exists {
		return err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	if state.ApprovedSetupDigest != digest {
		state.LastSetup = SetupStatus{
			RecipeDigest: digest, ApprovedDigest: state.ApprovedSetupDigest,
			State: "approval_required", Message: "Setup recipe changed and requires owner approval",
		}
		if err := m.store.Save(state); err != nil {
			return err
		}
		m.mu.Lock()
		m.runtimeFor(workspaceID).status.Setup = state.LastSetup
		m.mu.Unlock()
		m.emit(Event{WorkspaceID: workspaceID, Event: "setup", Message: "Setup recipe requires owner approval"})
		return nil
	}
	returnError := error(nil)
	_, returnError = m.runSetupLocked(ctx, workspaceID, spec, state, digest)
	return returnError
}

func (m *Manager) runSetupLocked(ctx context.Context, workspaceID string, spec WorkspaceSpec, state MachineState, digest string) (SetupStatus, error) {
	var setup SetupStatus
	currentDigest, recipe, exists, err := readSetupRecipe(spec.SetupPath)
	if err != nil {
		return setup, err
	}
	if !exists {
		return setup, &Error{Code: "setup_recipe_missing", Message: "sandbox setup recipe was not found"}
	}
	if currentDigest != digest {
		setup = SetupStatus{
			RecipeDigest: currentDigest, ApprovedDigest: state.ApprovedSetupDigest, State: "approval_required",
			Message: "Setup recipe changed and requires owner approval",
		}
		state.LastSetup = setup
		_ = m.store.Save(state)
		m.mu.Lock()
		m.runtimeFor(workspaceID).status.Setup = setup
		m.mu.Unlock()
		return setup, ErrSetupApproval
	}
	setup.RecipeDigest, setup.ApprovedDigest, setup.State = digest, digest, "running"
	for _, role := range []string{"workbench", "desktop"} {
		setup.LastRole = role
		m.emit(Event{WorkspaceID: workspaceID, Event: "setup", Role: role, Message: "Running approved setup recipe"})
		result, runErr := m.engine.Exec(ctx, state, ExecRequest{
			Role: role, Command: []string{"/bin/bash", "-s", "--"}, Input: recipe,
			WorkingDirectory: mainGuestPath(spec.Roots), Environment: []string{"ECHO_SANDBOX_ROLE=" + role, "ECHO_SANDBOX_SETUP_RECIPE=" + guestSetupPath(spec.Roots)}, OutputLimit: 4 << 20,
			Root: true,
		})
		m.emit(Event{WorkspaceID: workspaceID, Event: "setup_log", Role: role, Stream: "stdout", Data: string(result.Stdout)})
		m.emit(Event{WorkspaceID: workspaceID, Event: "setup_log", Role: role, Stream: "stderr", Data: string(result.Stderr)})
		setup.ExitCode = result.ExitCode
		if runErr != nil || result.ExitCode != 0 {
			setup.State, setup.Message, setup.LastRunAt = "failed", "Setup recipe failed", time.Now().UTC()
			state.LastSetup = setup
			_ = m.store.Save(state)
			if runErr != nil {
				return setup, runErr
			}
			return setup, &Error{Code: "setup_failed", Message: "sandbox setup recipe failed"}
		}
	}
	setup.State, setup.Message, setup.LastRunAt = "succeeded", "Setup completed", time.Now().UTC()
	state.LastSetup = setup
	if err := m.store.Save(state); err != nil {
		return setup, err
	}
	m.mu.Lock()
	m.runtimeFor(workspaceID).status.Setup = setup
	m.mu.Unlock()
	return setup, nil
}

func mainGuestPath(roots []RootMount) string {
	for _, root := range roots {
		if root.Main {
			return root.GuestPath
		}
	}
	if len(roots) > 0 {
		return roots[0].GuestPath
	}
	return "/workspace"
}

func guestSetupPath(roots []RootMount) string { return mainGuestPath(roots) + "/" + SetupRecipePath }

func (m *Manager) NetworkGrants(workspaceID string) ([]NetworkGrant, error) {
	if _, err := m.workspace(workspaceID); err != nil {
		return nil, err
	}
	state, _, err := m.store.Load(workspaceID)
	if err != nil {
		return nil, err
	}
	grants := append([]NetworkGrant(nil), state.NetworkGrants...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].CreatedAt.Before(grants[j].CreatedAt) })
	return grants, nil
}

func (m *Manager) AddNetworkGrant(ctx context.Context, workspaceID string, grant NetworkGrant) (NetworkGrant, error) {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return NetworkGrant{}, ctx.Err()
	}
	if err := ValidateNetworkGrant(grant); err != nil {
		return NetworkGrant{}, &Error{Code: "invalid_network_grant", Message: err.Error()}
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return NetworkGrant{}, err
	}
	grant.Host = strings.TrimSuffix(strings.Trim(strings.TrimSpace(grant.Host), "[]"), ".")
	grant.Label = strings.TrimSpace(grant.Label)
	grant.SandboxAlias = strings.TrimSuffix(strings.TrimSpace(grant.SandboxAlias), ".")
	grant.ID, grant.CreatedAt = uuid.NewString(), time.Now().UTC()
	for _, existing := range state.NetworkGrants {
		if strings.EqualFold(existing.Host, grant.Host) && existing.Port == grant.Port {
			return NetworkGrant{}, &Error{Code: "network_grant_exists", Message: "an exact grant already exists"}
		}
		if grant.SandboxAlias != "" && strings.EqualFold(existing.SandboxAlias, grant.SandboxAlias) && !strings.EqualFold(existing.Host, grant.Host) {
			return NetworkGrant{}, &Error{Code: "network_alias_conflict", Message: "sandbox alias already maps to another host"}
		}
	}
	previous := append([]NetworkGrant(nil), state.NetworkGrants...)
	state.NetworkGrants = append(state.NetworkGrants, grant)
	if err := m.store.Save(state); err != nil {
		return NetworkGrant{}, err
	}
	if err := m.engine.ApplyNetworkGrants(ctx, state, state.NetworkGrants); err != nil {
		state.NetworkGrants = previous
		_ = m.store.Save(state)
		return NetworkGrant{}, err
	}
	m.emit(Event{WorkspaceID: workspaceID, Event: "network_grants", Message: "Network grant added"})
	return grant, nil
}

func (m *Manager) DeleteNetworkGrant(ctx context.Context, workspaceID, grantID string) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	unlock := m.lock(workspaceID)
	defer unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	filtered := make([]NetworkGrant, 0, len(state.NetworkGrants))
	found := false
	for _, grant := range state.NetworkGrants {
		if grant.ID == grantID {
			found = true
			continue
		}
		filtered = append(filtered, grant)
	}
	if !found {
		return &Error{Code: "network_grant_not_found", Message: "network grant was not found"}
	}
	previous := append([]NetworkGrant(nil), state.NetworkGrants...)
	state.NetworkGrants = filtered
	if err := m.store.Save(state); err != nil {
		return err
	}
	if err := m.engine.ApplyNetworkGrants(ctx, state, filtered); err != nil {
		state.NetworkGrants = previous
		_ = m.store.Save(state)
		return err
	}
	m.emit(Event{WorkspaceID: workspaceID, Event: "network_grants", Message: "Network grant revoked"})
	return nil
}

func (m *Manager) CreateDesktopSession(ctx context.Context, workspaceID, browserSessionID string) (DesktopSession, error) {
	if strings.TrimSpace(browserSessionID) == "" {
		return DesktopSession{}, &Error{Code: "authentication_required", Message: "authenticated browser session is required"}
	}
	if err := m.Start(ctx, workspaceID); err != nil {
		return DesktopSession{}, err
	}
	session := DesktopSession{ID: uuid.NewString(), BrowserSession: browserSessionID, ViewOnly: true, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC()}
	secrets, err := m.credentials(workspaceID)
	if err != nil {
		return DesktopSession{}, err
	}
	session.Credential = secrets.VNCToken
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	runtime.viewers[session.ID] = session
	runtime.connected[session.ID] = false
	runtime.lastActive = time.Now()
	m.mu.Unlock()
	m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_viewers", Message: "Desktop viewer session created"})
	return session, nil
}

func (m *Manager) DeleteDesktopSession(workspaceID, sessionID, browserSessionID string) error {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	session, ok := runtime.viewers[sessionID]
	if !ok || session.BrowserSession != browserSessionID {
		m.mu.Unlock()
		return &Error{Code: "desktop_session_not_found", Message: "desktop session was not found"}
	}
	delete(runtime.viewers, sessionID)
	delete(runtime.connected, sessionID)
	if runtime.lease.Owner == LeaseUser && runtime.lease.BrowserSessionID == browserSessionID {
		runtime.lease.ExpiresAt = time.Now().Add(2 * time.Minute).UTC()
		runtime.lease.Revision++
	}
	m.mu.Unlock()
	m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_viewers", Message: "Desktop viewer disconnected"})
	return nil
}

func (m *Manager) TakeUserControl(workspaceID, browserSessionID string, confirmPreempt bool) (DesktopLease, error) {
	if strings.TrimSpace(browserSessionID) == "" {
		return DesktopLease{}, &Error{Code: "authentication_required", Message: "authenticated browser session is required"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	lease := runtime.lease
	if lease.Owner == LeaseUser && lease.BrowserSessionID != browserSessionID && !confirmPreempt {
		return lease, ErrControlConflict
	}
	if runtime.guiCancel != nil {
		runtime.guiCancel()
	}
	runtime.guiCancel, runtime.guiContext = nil, nil
	lease.Owner, lease.BrowserSessionID, lease.ChatTurnID = LeaseUser, browserSessionID, ""
	lease.ExpiresAt, lease.Revision = time.Now().Add(2*time.Minute).UTC(), lease.Revision+1
	if hasConnectedBrowserSession(runtime, browserSessionID) {
		lease.ExpiresAt = time.Time{}
	}
	runtime.lease = lease
	go m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "User took desktop control"})
	return lease, nil
}

func (m *Manager) ReleaseUserControl(workspaceID, browserSessionID string) (DesktopLease, error) {
	if strings.TrimSpace(browserSessionID) == "" {
		return DesktopLease{}, &Error{Code: "authentication_required", Message: "authenticated browser session is required"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	lease := runtime.lease
	if lease.Owner == LeaseNone {
		return lease, nil
	}
	if lease.Owner != LeaseUser {
		return lease, ErrControlConflict
	}
	if lease.Owner == LeaseUser && lease.BrowserSessionID != browserSessionID {
		return lease, ErrControlConflict
	}
	lease = DesktopLease{Owner: LeaseNone, Revision: lease.Revision + 1}
	runtime.lease = lease
	go m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "Desktop control returned"})
	return lease, nil
}

// DesktopInputAllowed is enforced by the server-side RFB bridge. Frontend
// view-only flags are presentation hints, not an authorization boundary.
func (m *Manager) DesktopInputAllowed(workspaceID, browserSessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease := m.runtimeFor(workspaceID).lease
	return lease.Owner == LeaseUser && lease.BrowserSessionID == browserSessionID
}

func (m *Manager) AcquireAIControl(workspaceID, turnID string, timeout time.Duration) (context.Context, func(), error) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	lease := runtime.lease
	if !lease.ExpiresAt.IsZero() && time.Now().After(lease.ExpiresAt) {
		if runtime.guiCancel != nil {
			runtime.guiCancel()
		}
		runtime.guiCancel, runtime.guiContext = nil, nil
		lease = DesktopLease{Owner: LeaseNone, Revision: lease.Revision + 1}
		runtime.lease = lease
	}
	if lease.Owner == LeaseUser {
		m.mu.Unlock()
		return nil, nil, ErrUserControlActive
	}
	if lease.Owner == LeaseAI && lease.ChatTurnID != turnID {
		m.mu.Unlock()
		return nil, nil, ErrControlConflict
	}
	if lease.Owner == LeaseAI && lease.ChatTurnID == turnID && runtime.guiContext != nil && runtime.guiContext.Err() == nil {
		ctx := runtime.guiContext
		m.mu.Unlock()
		return ctx, func() {}, nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	lease.Owner, lease.ChatTurnID, lease.BrowserSessionID = LeaseAI, turnID, ""
	lease.ExpiresAt, lease.Revision = time.Now().Add(timeout).UTC(), lease.Revision+1
	runtime.lease, runtime.guiCancel, runtime.guiContext = lease, cancel, ctx
	m.mu.Unlock()
	m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "AI acquired desktop control"})
	release := func() {
		m.mu.Lock()
		current := m.runtimeFor(workspaceID)
		if current.lease.Owner == LeaseAI && current.lease.ChatTurnID == turnID {
			current.lease = DesktopLease{Owner: LeaseNone, Revision: current.lease.Revision + 1}
			current.guiCancel = nil
			current.guiContext = nil
		}
		m.mu.Unlock()
		cancel()
		m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "AI released desktop control"})
	}
	return ctx, release, nil
}

func (m *Manager) ReleaseAIControl(workspaceID, turnID string) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	if runtime.lease.Owner != LeaseAI || runtime.lease.ChatTurnID != turnID {
		m.mu.Unlock()
		return
	}
	if runtime.guiCancel != nil {
		runtime.guiCancel()
	}
	runtime.guiCancel, runtime.guiContext = nil, nil
	runtime.lease = DesktopLease{Owner: LeaseNone, Revision: runtime.lease.Revision + 1}
	m.mu.Unlock()
	m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "AI released desktop control"})
}

func (m *Manager) BrowserCall(ctx context.Context, workspaceID, turnID, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return nil, err
	}
	leaseCtx, _, err := m.AcquireAIControl(workspaceID, turnID, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := joinContexts(ctx, leaseCtx)
	defer cancel()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return nil, err
	}
	m.touch(workspaceID, 1)
	defer m.touch(workspaceID, -1)
	result, callErr := m.engine.BrowserCall(callCtx, state, method, params)
	if callErr != nil && m.userControlActive(workspaceID) {
		return nil, ErrUserControlActive
	}
	return result, callErr
}

func (m *Manager) DesktopAction(ctx context.Context, workspaceID, turnID string, action DesktopActionRequest) error {
	if err := m.Start(ctx, workspaceID); err != nil {
		return err
	}
	leaseCtx, _, err := m.AcquireAIControl(workspaceID, turnID, 2*time.Minute)
	if err != nil {
		return err
	}
	callCtx, cancel := joinContexts(ctx, leaseCtx)
	defer cancel()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	m.touch(workspaceID, 1)
	defer m.touch(workspaceID, -1)
	callErr := m.engine.DesktopAction(callCtx, state, action)
	if callErr != nil && m.userControlActive(workspaceID) {
		return ErrUserControlActive
	}
	return callErr
}

func (m *Manager) DesktopScreenshot(ctx context.Context, workspaceID, turnID string) ([]byte, string, error) {
	if err := m.Start(ctx, workspaceID); err != nil {
		return nil, "", err
	}
	leaseCtx, _, err := m.AcquireAIControl(workspaceID, turnID, 2*time.Minute)
	if err != nil {
		return nil, "", err
	}
	callCtx, cancel := joinContexts(ctx, leaseCtx)
	defer cancel()
	state, err := m.machineState(workspaceID)
	if err != nil {
		return nil, "", err
	}
	m.touch(workspaceID, 1)
	defer m.touch(workspaceID, -1)
	image, mediaType, captureErr := m.engine.DesktopScreenshot(callCtx, state)
	if captureErr != nil && m.userControlActive(workspaceID) {
		return nil, "", ErrUserControlActive
	}
	return image, mediaType, captureErr
}

func (m *Manager) userControlActive(workspaceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimeFor(workspaceID).lease.Owner == LeaseUser
}

func joinContexts(primary, lease context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(lease, cancel)
	return ctx, func() { stop(); cancel() }
}

func (m *Manager) OpenDesktop(ctx context.Context, workspaceID, sessionID, browserSessionID string) (io.ReadWriteCloser, error) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	session, ok := runtime.viewers[sessionID]
	if !ok || session.BrowserSession != browserSessionID || time.Now().After(session.ExpiresAt) || runtime.connected[sessionID] {
		m.mu.Unlock()
		return nil, &Error{Code: "desktop_session_not_found", Message: "desktop session is invalid or expired"}
	}
	runtime.connected[sessionID] = true
	if runtime.lease.Owner == LeaseUser && runtime.lease.BrowserSessionID == browserSessionID {
		runtime.lease.ExpiresAt = time.Time{}
	}
	runtime.lastActive = time.Now()
	m.mu.Unlock()
	state, err := m.machineState(workspaceID)
	if err != nil {
		m.closeDesktopConnection(workspaceID, sessionID, browserSessionID)
		return nil, err
	}
	stream, err := m.engine.OpenDesktop(ctx, state)
	if err != nil {
		m.closeDesktopConnection(workspaceID, sessionID, browserSessionID)
		return nil, err
	}
	m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_viewers", Message: "Desktop viewer connected"})
	return stream, nil
}

func (m *Manager) CloseDesktopConnection(workspaceID, sessionID, browserSessionID string) {
	m.closeDesktopConnection(workspaceID, sessionID, browserSessionID)
}

func (m *Manager) closeDesktopConnection(workspaceID, sessionID, browserSessionID string) {
	m.mu.Lock()
	runtime := m.runtimeFor(workspaceID)
	session, ok := runtime.viewers[sessionID]
	removed := false
	if ok && session.BrowserSession == browserSessionID {
		delete(runtime.viewers, sessionID)
		delete(runtime.connected, sessionID)
		if runtime.lease.Owner == LeaseUser && runtime.lease.BrowserSessionID == browserSessionID {
			runtime.lease.ExpiresAt = time.Now().Add(2 * time.Minute).UTC()
			runtime.lease.Revision++
		}
		removed = true
	}
	m.mu.Unlock()
	if removed {
		m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_viewers", Message: "Desktop viewer disconnected"})
	}
}

func (m *Manager) maintenance() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer close(m.done)
	for {
		select {
		case <-m.closed:
			return
		case now := <-ticker.C:
			m.maintain(now)
		}
	}
}

func (m *Manager) maintain(now time.Time) {
	type stopCandidate struct {
		id      string
		timeout time.Duration
	}
	var stops []stopCandidate
	var heartbeats []string
	var releasedLeases []string
	m.mu.Lock()
	for workspaceID, runtime := range m.runtime {
		for id, session := range runtime.viewers {
			if !runtime.connected[id] && now.After(session.ExpiresAt) {
				delete(runtime.viewers, id)
				delete(runtime.connected, id)
			}
		}
		leaseConnected := runtime.lease.Owner == LeaseUser && hasConnectedBrowserSession(runtime, runtime.lease.BrowserSessionID)
		if !leaseConnected && !runtime.lease.ExpiresAt.IsZero() && now.After(runtime.lease.ExpiresAt) {
			if runtime.guiCancel != nil {
				runtime.guiCancel()
			}
			runtime.guiCancel, runtime.guiContext = nil, nil
			runtime.lease = DesktopLease{Owner: LeaseNone, Revision: runtime.lease.Revision + 1}
			releasedLeases = append(releasedLeases, workspaceID)
		}
		if runtime.status.State == StateReady {
			heartbeats = append(heartbeats, workspaceID)
		}
		if runtime.status.State != StateReady || runtime.active > 0 || connectedViewerCount(runtime) > 0 || runtime.lease.Owner != LeaseNone {
			continue
		}
		workspace, err := m.workspace(workspaceID)
		if err != nil || workspace.Sandbox.IdleTimeoutMinutes == 0 {
			continue
		}
		timeout := time.Duration(workspace.Sandbox.IdleTimeoutMinutes) * time.Minute
		if now.Sub(runtime.lastActive) >= timeout {
			stops = append(stops, stopCandidate{id: workspaceID, timeout: timeout})
		}
	}
	m.mu.Unlock()
	for _, workspaceID := range releasedLeases {
		m.emit(Event{WorkspaceID: workspaceID, Event: "desktop_lease", Message: "Desktop control lease expired"})
	}
	for _, workspaceID := range heartbeats {
		state, err := m.machineState(workspaceID)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.engine.Heartbeat(ctx, state)
		cancel()
	}
	for _, candidate := range stops {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = m.Stop(ctx, candidate.id)
		cancel()
	}
}

func hasConnectedBrowserSession(runtime *runtimeState, browserSessionID string) bool {
	for id, session := range runtime.viewers {
		if session.BrowserSession == browserSessionID && runtime.connected[id] {
			return true
		}
	}
	return false
}

func connectedViewerCount(runtime *runtimeState) int {
	count := 0
	for id := range runtime.viewers {
		if runtime.connected[id] {
			count++
		}
	}
	return count
}

func (m *Manager) Reconcile(ctx context.Context) error {
	return m.awaitStartupReconcile(ctx)
}

func (m *Manager) awaitStartupReconcile(ctx context.Context) error {
	m.reconcileOnce.Do(func() {
		go func() {
			reconcileContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := m.reconcileNow(reconcileContext)
			m.mu.Lock()
			m.reconcileErr = err
			m.mu.Unlock()
			close(m.reconcileDone)
		}()
	})
	select {
	case <-m.reconcileDone:
		m.mu.Lock()
		err := m.reconcileErr
		m.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) reconcileNow(ctx context.Context) error {
	registered, err := m.workspaces.List()
	if err != nil {
		return err
	}
	workspaceIDs := make([]string, 0, len(registered))
	for _, workspace := range registered {
		if workspace.Sandbox.Enabled {
			workspaceIDs = append(workspaceIDs, workspace.ID)
		}
	}
	return m.engine.Reconcile(ctx, m.installation, ProtocolVersion, workspaceIDs)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if err := m.awaitStartupReconcile(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-m.closed:
	default:
		close(m.closed)
	}
	select {
	case <-m.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	workspaceSet := map[string]bool{}
	m.mu.Lock()
	for id, runtime := range m.runtime {
		if runtime.status.State == StateReady {
			workspaceSet[id] = true
		}
	}
	m.mu.Unlock()
	if registered, err := m.workspaces.List(); err == nil {
		for _, workspace := range registered {
			if !workspace.Sandbox.Enabled {
				continue
			}
			if _, exists, loadErr := m.store.Load(workspace.ID); loadErr == nil && exists {
				workspaceSet[workspace.ID] = true
			}
		}
	}
	workspacesList := make([]string, 0, len(workspaceSet))
	for workspaceID := range workspaceSet {
		workspacesList = append(workspacesList, workspaceID)
	}
	sort.Strings(workspacesList)
	var joined error
	for _, workspaceID := range workspacesList {
		if err := m.Stop(ctx, workspaceID); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	if err := m.engine.Close(); err != nil {
		joined = errors.Join(joined, err)
	}
	return joined
}
