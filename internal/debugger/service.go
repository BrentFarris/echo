package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

type workspaceResolver interface {
	Get(string) (workspaces.Workspace, bool, error)
	List() ([]workspaces.Workspace, error)
	SetDebugConfig(string, debugconfig.WorkspaceConfig) (workspaces.Workspace, error)
}

type workspaceRuntime struct {
	sequence uint64
	sessions map[string]*session
	groups   map[string]*sessionGroup
}

type sessionGroup struct {
	id, name   string
	compoundID string
	sessionIDs []string
	stopAll    bool
}

type session struct {
	id                   string
	workspaceID          string
	groupID              string
	parentID             string
	configuration        debugconfig.Configuration
	profile              debugconfig.AdapterProfile
	startRequest         StartRequest
	status               string
	revision             uint64
	stopGeneration       uint64
	stoppedReason        string
	stoppedText          string
	threadID             int
	allThreadsStopped    bool
	capabilities         map[string]any
	location             *SourceLocation
	err                  string
	startedAt            time.Time
	endedAt              *time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	initialized          chan struct{}
	initializedOnce      sync.Once
	conn                 *dapConnection
	handle               *adapterHandle
	output               []OutputEntry
	outputBytes          int
	outputSequence       uint64
	breakpointSources    map[string]bool
	breakpointStatuses   map[string]BreakpointStatus
	adapterBreakpointIDs map[int]string
	postStarted          bool
	postCancel           context.CancelFunc
	postDone             chan struct{}
	traceDAP             bool
	controlMu            sync.Mutex
}

// Service owns every active debug adapter. Browser clients are observers of
// these server-side sessions, so disconnecting a tab never kills a debuggee.
type Service struct {
	profiles      *debugconfig.ProfileStore
	state         *debugconfig.StateStore
	workspaces    workspaceResolver
	fs            *workspacefs.Service
	mu            sync.Mutex
	runtimes      map[string]*workspaceRuntime
	sandbox       *sandbox.Manager
	notify        func(Event)
	runInTerminal func(context.Context, string, string, map[string]any) (map[string]any, error)
	stopTerminals func(string, string)
}

func New(profiles *debugconfig.ProfileStore, state *debugconfig.StateStore, workspaceManager workspaceResolver, fs *workspacefs.Service) *Service {
	return &Service{profiles: profiles, state: state, workspaces: workspaceManager, fs: fs, runtimes: map[string]*workspaceRuntime{}}
}

func (s *Service) SetSandbox(manager *sandbox.Manager) {
	s.mu.Lock()
	s.sandbox = manager
	s.mu.Unlock()
}
func (s *Service) sandboxManager() *sandbox.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandbox
}
func (s *Service) SetNotifier(notify func(Event)) { s.mu.Lock(); s.notify = notify; s.mu.Unlock() }
func (s *Service) SetRunInTerminal(run func(context.Context, string, string, map[string]any) (map[string]any, error)) {
	s.mu.Lock()
	s.runInTerminal = run
	s.mu.Unlock()
}
func (s *Service) SetStopDebugTerminals(stop func(string, string)) {
	s.mu.Lock()
	s.stopTerminals = stop
	s.mu.Unlock()
}

func (s *Service) Profiles() ([]debugconfig.AdapterProfile, error) { return s.profiles.Profiles() }
func (s *Service) Templates() []debugconfig.Template               { return debugconfig.Templates() }
func (s *Service) AddProfile(profile debugconfig.AdapterProfile) (debugconfig.AdapterProfile, error) {
	return s.profiles.Add(profile)
}
func (s *Service) AddTemplate(id string) (debugconfig.AdapterProfile, error) {
	return s.profiles.AddTemplate(id)
}
func (s *Service) UpdateProfile(id string, profile debugconfig.AdapterProfile) (debugconfig.AdapterProfile, error) {
	return s.profiles.Update(id, profile)
}
func (s *Service) DeleteProfile(id string) error { return s.profiles.Delete(id, s.profileInUse) }

func (s *Service) profileInUse(id string) bool {
	list, err := s.workspaces.List()
	if err != nil {
		return true
	}
	for _, workspace := range list {
		for _, enabled := range workspace.Debug.EnabledAdapterProfileIDs {
			if enabled == id {
				return true
			}
		}
	}
	return false
}

func (s *Service) WorkspaceConfig(workspaceID string) (debugconfig.WorkspaceConfig, []debugconfig.AdapterProfile, error) {
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return debugconfig.WorkspaceConfig{}, nil, err
	}
	profiles, err := s.profiles.Profiles()
	if err != nil {
		return debugconfig.WorkspaceConfig{}, nil, err
	}
	return workspace.Debug.Normalized(), profiles, nil
}

func (s *Service) SetWorkspaceConfig(workspaceID string, config debugconfig.WorkspaceConfig) (debugconfig.WorkspaceConfig, []debugconfig.AdapterProfile, error) {
	profiles, err := s.profiles.Profiles()
	if err != nil {
		return debugconfig.WorkspaceConfig{}, nil, err
	}
	config = config.Normalized()
	if err := config.Validate(profiles); err != nil {
		return debugconfig.WorkspaceConfig{}, profiles, err
	}
	workspace, err := s.workspaces.SetDebugConfig(workspaceID, config)
	if err != nil {
		return debugconfig.WorkspaceConfig{}, profiles, err
	}
	return workspace.Debug, profiles, nil
}

func (s *Service) State(workspaceID string) (debugconfig.State, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return debugconfig.State{}, err
	}
	return s.state.Load(workspaceID)
}

func (s *Service) SetState(workspaceID string, expected uint64, state debugconfig.State) (debugconfig.State, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return debugconfig.State{}, err
	}
	saved, err := s.state.Save(workspaceID, expected, state)
	if err != nil {
		return debugconfig.State{}, err
	}
	s.publishState(workspaceID, saved)
	go s.applyWorkspaceBreakpoints(workspaceID)
	return saved, nil
}

func (s *Service) SetTrace(workspaceID, sessionID string, expectedRevision uint64, enabled bool) (SessionSnapshot, error) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return SessionSnapshot{}, err
	}
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return SessionSnapshot{}, &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	changed := current.traceDAP != enabled
	if current.traceDAP != enabled {
		current.traceDAP = enabled
		current.revision++
	}
	snapshot := s.sessionSnapshotLocked(current)
	s.mu.Unlock()
	if changed {
		s.publishSession(workspaceID, sessionID, "trace_changed", nil, "")
	}
	return snapshot, nil
}

func (s *Service) Diagnostic(ctx context.Context, workspaceID, profileID string) (Diagnostic, error) {
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return Diagnostic{}, err
	}
	profile, err := s.effectiveProfile(workspace, profileID)
	if err != nil {
		return Diagnostic{}, err
	}
	result := Diagnostic{ProfileID: profile.ID, Execution: "host", Command: profile.Command}
	if workspace.Sandbox.Enabled {
		result.Execution = "sandbox"
	}
	options, err := s.executionOptions(workspace, "", nil, "")
	if err != nil {
		return result, err
	}
	if profile.Command != "" {
		if command, expandErr := debugconfig.ExpandString(profile.Command, options); expandErr == nil {
			result.Command = command
		} else {
			return result, expandErr
		}
	}
	timeout := time.Duration(profile.Transport.StartupTimeoutMS)*time.Millisecond + requestTimeout
	testContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	handle, startErr := s.startAdapter(testContext, workspace, profile, options, "", func(string, string) {})
	if startErr != nil {
		result.Message = fmt.Sprintf("Adapter could not start in the workspace %s: %v", result.Execution, startErr)
		return result, nil
	}
	defer handle.stop()
	connection := newDAPConnection(handle.transport, nil, nil, nil)
	defer connection.Close()
	initializeContext, initializeCancel := context.WithTimeout(testContext, requestTimeout)
	_, initializeErr := connection.request(initializeContext, "initialize", map[string]any{
		"clientID": "echo-diagnostic", "clientName": "Echo Adapter Test", "adapterID": profile.AdapterID,
		"locale": "en-us", "pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true,
	})
	initializeCancel()
	if initializeErr != nil {
		result.Message = fmt.Sprintf("Adapter started but did not complete a DAP initialize handshake: %v", initializeErr)
		return result, nil
	}
	disconnectContext, disconnectCancel := context.WithTimeout(testContext, 2*time.Second)
	_, _ = connection.request(disconnectContext, "disconnect", map[string]any{"terminateDebuggee": false})
	disconnectCancel()
	result.Available = true
	result.Message = fmt.Sprintf("DAP initialize handshake succeeded in the workspace %s", result.Execution)
	return result, nil
}

func (s *Service) Snapshot(workspaceID string) (Snapshot, error) {
	if _, err := s.workspace(workspaceID); err != nil {
		return Snapshot{}, err
	}
	state, err := s.state.Load(workspaceID)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimeLocked(workspaceID)
	return s.snapshotLocked(workspaceID, runtime, state), nil
}

func (s *Service) Start(ctx context.Context, workspaceID string, request StartRequest) (Snapshot, error) {
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return Snapshot{}, err
	}
	profiles, err := s.profiles.Profiles()
	if err != nil {
		return Snapshot{}, err
	}
	config := workspace.Debug.Normalized()
	if err := config.Validate(profiles); err != nil {
		return Snapshot{}, err
	}
	if (request.ConfigurationID == "") == (request.CompoundID == "") {
		return Snapshot{}, fmt.Errorf("exactly one configurationId or compoundId is required")
	}
	if request.ConfigurationID != "" {
		entry, ok := config.Configuration(request.ConfigurationID)
		if !ok {
			return Snapshot{}, fmt.Errorf("debug configuration %q was not found", request.ConfigurationID)
		}
		_, err = s.createSession(workspace, entry, "", "", request)
		if err != nil {
			return Snapshot{}, err
		}
	} else {
		compound, ok := config.Compound(request.CompoundID)
		if !ok {
			return Snapshot{}, fmt.Errorf("debug compound %q was not found", request.CompoundID)
		}
		groupID := uuid.NewString()
		entries := make([]debugconfig.Configuration, 0, len(compound.ConfigurationIDs))
		for _, id := range compound.ConfigurationIDs {
			entry, _ := config.Configuration(id)
			if _, err := s.effectiveProfile(workspace, entry.AdapterProfileID); err != nil {
				return Snapshot{}, err
			}
			entries = append(entries, entry)
		}
		s.mu.Lock()
		runtime := s.runtimeLocked(workspaceID)
		runtime.groups[groupID] = &sessionGroup{id: groupID, name: compound.Name, compoundID: compound.ID, stopAll: compound.StopAll}
		s.mu.Unlock()
		for _, entry := range entries {
			if _, err := s.createSession(workspace, entry, groupID, "", request); err != nil {
				if compound.StopAll {
					s.StopGroup(workspaceID, groupID)
				}
				return Snapshot{}, err
			}
		}
	}
	return s.Snapshot(workspaceID)
}

func (s *Service) createSession(workspace workspaces.Workspace, configuration debugconfig.Configuration, groupID, parentID string, request StartRequest) (string, error) {
	profile, err := s.effectiveProfile(workspace, configuration.AdapterProfileID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	current := &session{id: uuid.NewString(), workspaceID: workspace.ID, groupID: groupID, parentID: parentID, configuration: configuration, profile: profile, startRequest: request, status: StatusStarting, revision: 1, startedAt: time.Now().UTC(), ctx: ctx, cancel: cancel, initialized: make(chan struct{}), capabilities: map[string]any{}, breakpointSources: map[string]bool{}, breakpointStatuses: map[string]BreakpointStatus{}, adapterBreakpointIDs: map[int]string{}}
	s.mu.Lock()
	runtime := s.runtimeLocked(workspace.ID)
	runtime.sessions[current.id] = current
	if groupID != "" {
		if group := runtime.groups[groupID]; group != nil {
			group.sessionIDs = append(group.sessionIDs, current.id)
		}
	}
	s.mu.Unlock()
	s.publishSession(workspace.ID, current.id, "session_started", nil, "")
	go s.runSession(current)
	return current.id, nil
}

func (s *Service) Request(ctx context.Context, workspaceID, sessionID, command string, request ControlRequest) (RequestResponse, error) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return RequestResponse{}, err
	}
	s.mu.Unlock()
	if isSerializedCommand(command) {
		current.controlMu.Lock()
		defer current.controlMu.Unlock()
	}
	// Re-read and validate after entering the per-session command lane. This is
	// what makes two browsers acting on the same revision deterministic: only
	// the first control can reach the adapter.
	s.mu.Lock()
	active, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || active != current {
		s.mu.Unlock()
		if err != nil {
			return RequestResponse{}, err
		}
		return RequestResponse{}, ErrSessionNotFound
	}
	if request.ExpectedRevision != 0 && request.ExpectedRevision != current.revision {
		actual := current.revision
		s.mu.Unlock()
		return RequestResponse{}, &RevisionError{Expected: request.ExpectedRevision, Actual: actual}
	}
	if request.StopGeneration != 0 && request.StopGeneration != current.stopGeneration {
		actual := current.stopGeneration
		s.mu.Unlock()
		return RequestResponse{}, &RevisionError{Expected: request.StopGeneration, Actual: actual, Stop: true}
	}
	if err := requireCapability(current, command); err != nil {
		s.mu.Unlock()
		return RequestResponse{}, err
	}
	connection := current.conn
	sessionContext := current.ctx
	threadID := current.threadID
	s.mu.Unlock()
	if connection == nil {
		return RequestResponse{}, fmt.Errorf("debug adapter is not connected")
	}
	arguments := cloneMap(request.Arguments)
	translatedArguments, translateErr := s.translateDAPArguments(workspaceID, arguments)
	if translateErr != nil {
		return RequestResponse{}, translateErr
	}
	arguments, _ = translatedArguments.(map[string]any)
	if usesThread(command) && arguments["threadId"] == nil && threadID != 0 {
		arguments["threadId"] = threadID
	}
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	go func() {
		select {
		case <-sessionContext.Done():
			cancel()
		case <-callCtx.Done():
		}
	}()
	response, err := connection.request(callCtx, command, arguments)
	if err != nil {
		return RequestResponse{}, err
	}
	if command == "continue" || command == "next" || command == "stepIn" || command == "stepOut" || command == "stepBack" || command == "reverseContinue" {
		s.markRunning(workspaceID, sessionID, command)
	}
	s.mu.Lock()
	current, err = s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return RequestResponse{}, err
	}
	if request.StopGeneration != 0 && isStopSensitiveResponse(command) && request.StopGeneration != current.stopGeneration {
		actual := current.stopGeneration
		s.mu.Unlock()
		return RequestResponse{}, &RevisionError{Expected: request.StopGeneration, Actual: actual, Stop: true}
	}
	body := s.translateDAPBody(workspaceID, response.Body)
	result := RequestResponse{WorkspaceID: workspaceID, SessionID: sessionID, Revision: current.revision, StopGeneration: current.stopGeneration, Body: body}
	s.mu.Unlock()
	return result, nil
}

func (s *Service) Stop(workspaceID, sessionID string, terminate *bool) error {
	return s.stop(workspaceID, sessionID, 0, terminate)
}

// StopChecked prevents a browser from stopping a session after it has acted on
// a stale snapshot. Internal lifecycle cleanup uses Stop, which intentionally
// bypasses the browser revision precondition.
func (s *Service) StopChecked(workspaceID, sessionID string, expectedRevision uint64, terminate *bool) error {
	return s.stopWithMode(workspaceID, sessionID, expectedRevision, terminate, false)
}

func (s *Service) stop(workspaceID, sessionID string, expectedRevision uint64, terminate *bool) error {
	return s.stopWithMode(workspaceID, sessionID, expectedRevision, terminate, false)
}

func (s *Service) DisconnectChecked(workspaceID, sessionID string, expectedRevision uint64) error {
	terminate := false
	return s.stopWithMode(workspaceID, sessionID, expectedRevision, &terminate, false)
}

func (s *Service) TerminateChecked(workspaceID, sessionID string, expectedRevision uint64) error {
	terminate := true
	return s.stopWithMode(workspaceID, sessionID, expectedRevision, &terminate, true)
}

func (s *Service) stopWithMode(workspaceID, sessionID string, expectedRevision uint64, terminate *bool, preferTerminateRequest bool) error {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	current.controlMu.Lock()
	defer current.controlMu.Unlock()
	s.mu.Lock()
	active, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || active != current {
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return ErrSessionNotFound
	}
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	if current.status == StatusTerminated || current.status == StatusFailed {
		s.mu.Unlock()
		return nil
	}
	current.status = StatusTerminating
	current.revision++
	connection := current.conn
	supportsTerminate := boolCapability(current.capabilities, "supportsTerminateRequest")
	shouldTerminate := current.configuration.Request == "launch"
	if terminate != nil {
		shouldTerminate = *terminate
	}
	revision := current.revision
	s.mu.Unlock()
	s.publishSession(workspaceID, sessionID, "session_terminating", nil, "")
	if connection != nil {
		if preferTerminateRequest && supportsTerminate {
			terminateContext, terminateCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, terminateErr := connection.request(terminateContext, "terminate", map[string]any{"restart": false})
			terminateCancel()
			if terminateErr != nil {
				s.appendOutput(workspaceID, sessionID, "adapter", "DAP terminate request failed; disconnecting the session: "+terminateErr.Error()+"\n", nil)
			}
		}
		disconnectContext, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = connection.request(disconnectContext, "disconnect", map[string]any{"terminateDebuggee": shouldTerminate})
		disconnectCancel()
	}
	s.finishSession(workspaceID, sessionID, StatusTerminated, "", revision)
	return nil
}

func (s *Service) Restart(ctx context.Context, workspaceID, sessionID string) (Snapshot, error) {
	return s.RestartChecked(ctx, workspaceID, sessionID, 0)
}

func (s *Service) RestartChecked(ctx context.Context, workspaceID, sessionID string, expectedRevision uint64) (Snapshot, error) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return Snapshot{}, &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	nativeRestart := current.conn != nil && boolCapability(current.capabilities, "supportsRestartRequest") && !isTerminalStatus(current.status)
	s.mu.Unlock()
	if nativeRestart {
		return s.restartInAdapter(ctx, workspaceID, sessionID, expectedRevision, current)
	}
	s.mu.Lock()
	current, err = s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return Snapshot{}, &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	configuration := current.configuration
	request := current.startRequest
	groupID := current.groupID
	workspace, workspaceErr := s.workspace(workspaceID)
	s.mu.Unlock()
	if workspaceErr != nil {
		return Snapshot{}, workspaceErr
	}
	if err := s.StopChecked(workspaceID, sessionID, expectedRevision, nil); err != nil {
		return Snapshot{}, err
	}
	if _, err := s.createSession(workspace, configuration, groupID, "", request); err != nil {
		return Snapshot{}, err
	}
	return s.Snapshot(workspaceID)
}

func (s *Service) restartInAdapter(ctx context.Context, workspaceID, sessionID string, expectedRevision uint64, target *session) (Snapshot, error) {
	target.controlMu.Lock()
	defer target.controlMu.Unlock()
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || current != target {
		s.mu.Unlock()
		if err != nil {
			return Snapshot{}, err
		}
		return Snapshot{}, ErrSessionNotFound
	}
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return Snapshot{}, &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	if current.conn == nil || !boolCapability(current.capabilities, "supportsRestartRequest") {
		s.mu.Unlock()
		return Snapshot{}, fmt.Errorf("%w: restart", ErrUnsupported)
	}
	connection := current.conn
	previousStatus := current.status
	current.status = StatusConfiguring
	current.location = nil
	current.threadID = 0
	current.revision++
	sessionContext := current.ctx
	s.mu.Unlock()
	s.publishSession(workspaceID, sessionID, "session_restarting", nil, "")

	requestContext, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	go func() {
		select {
		case <-sessionContext.Done():
			cancel()
		case <-requestContext.Done():
		}
	}()
	if _, err := connection.request(requestContext, "restart", map[string]any{}); err != nil {
		s.mu.Lock()
		if active, lookupErr := s.sessionLocked(workspaceID, sessionID); lookupErr == nil && active == current && current.status == StatusConfiguring {
			current.status = previousStatus
			current.revision++
		}
		s.mu.Unlock()
		s.publishSession(workspaceID, sessionID, "restart_failed", nil, err.Error())
		return Snapshot{}, err
	}
	s.mu.Lock()
	if active, lookupErr := s.sessionLocked(workspaceID, sessionID); lookupErr == nil && active == current && current.status == StatusConfiguring {
		current.status = StatusRunning
		current.stopGeneration++
		current.revision++
	}
	s.mu.Unlock()
	go s.applyWorkspaceBreakpoints(workspaceID)
	s.publishSession(workspaceID, sessionID, "session_restarted", nil, "")
	return s.Snapshot(workspaceID)
}

func (s *Service) StopGroup(workspaceID, groupID string) {
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	group := runtime.groups[groupID]
	ids := []string{}
	if group != nil {
		ids = append(ids, group.sessionIDs...)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Stop(workspaceID, id, nil)
	}
}

func (s *Service) StopGroupChecked(workspaceID, groupID string, expected map[string]uint64) error {
	s.mu.Lock()
	runtime := s.runtimes[workspaceID]
	group := (*sessionGroup)(nil)
	if runtime != nil {
		group = runtime.groups[groupID]
	}
	if group == nil {
		s.mu.Unlock()
		return fmt.Errorf("debug compound group %q was not found", groupID)
	}
	ids := append([]string(nil), group.sessionIDs...)
	for _, id := range ids {
		current := runtime.sessions[id]
		want, ok := expected[id]
		if !ok || current == nil || want != current.revision {
			actual := uint64(0)
			if current != nil {
				actual = current.revision
			}
			s.mu.Unlock()
			return &RevisionError{Expected: want, Actual: actual}
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.StopChecked(workspaceID, id, expected[id], nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RestartGroup(ctx context.Context, workspaceID, groupID string, expected map[string]uint64) (Snapshot, error) {
	s.mu.Lock()
	runtime := s.runtimes[workspaceID]
	group := (*sessionGroup)(nil)
	var request StartRequest
	if runtime != nil {
		group = runtime.groups[groupID]
	}
	if group != nil && len(group.sessionIDs) > 0 {
		if current := runtime.sessions[group.sessionIDs[0]]; current != nil {
			request = current.startRequest
		}
	}
	compoundID := ""
	if group != nil {
		compoundID = group.compoundID
	}
	s.mu.Unlock()
	if group == nil || compoundID == "" {
		return Snapshot{}, fmt.Errorf("debug compound group %q was not found", groupID)
	}
	if err := s.StopGroupChecked(workspaceID, groupID, expected); err != nil {
		return Snapshot{}, err
	}
	request.ConfigurationID = ""
	request.CompoundID = compoundID
	return s.Start(ctx, workspaceID, request)
}

func (s *Service) StopWorkspace(workspaceID string) {
	s.mu.Lock()
	runtime := s.runtimes[workspaceID]
	ids := []string{}
	if runtime != nil {
		for id := range runtime.sessions {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Stop(workspaceID, id, nil)
	}
	done := s.cancelPostHooks(workspaceID)
	for _, finished := range done {
		<-finished
	}
	s.mu.Lock()
	delete(s.runtimes, workspaceID)
	s.mu.Unlock()
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	targets := map[string][]string{}
	for workspaceID, runtime := range s.runtimes {
		for id := range runtime.sessions {
			targets[workspaceID] = append(targets[workspaceID], id)
		}
	}
	s.mu.Unlock()
	for workspaceID, ids := range targets {
		for _, id := range ids {
			_ = s.Stop(workspaceID, id, nil)
		}
	}
	s.mu.Lock()
	workspaceIDs := make([]string, 0, len(s.runtimes))
	for workspaceID := range s.runtimes {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	s.mu.Unlock()
	for _, workspaceID := range workspaceIDs {
		for _, finished := range s.cancelPostHooks(workspaceID) {
			select {
			case <-finished:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (s *Service) cancelPostHooks(workspaceID string) []<-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimes[workspaceID]
	if runtime == nil {
		return nil
	}
	done := make([]<-chan struct{}, 0)
	for _, current := range runtime.sessions {
		if current.postCancel != nil {
			current.postCancel()
		}
		if current.postDone != nil {
			done = append(done, current.postDone)
		}
	}
	return done
}

func (s *Service) workspace(id string) (workspaces.Workspace, error) {
	id = strings.TrimSpace(id)
	workspace, ok, err := s.workspaces.Get(id)
	if err != nil {
		return workspaces.Workspace{}, err
	}
	if !ok {
		return workspaces.Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	return workspace, nil
}

func (s *Service) effectiveProfile(workspace workspaces.Workspace, id string) (debugconfig.AdapterProfile, error) {
	profiles, err := s.profiles.Profiles()
	if err != nil {
		return debugconfig.AdapterProfile{}, err
	}
	id = strings.ToLower(strings.TrimSpace(id))
	for _, profile := range profiles {
		if profile.ID == id {
			if override, ok := workspace.Debug.Overrides[id]; ok {
				profile = debugconfig.ApplyOverride(profile, override)
			}
			if err := profile.Validate(); err != nil {
				return debugconfig.AdapterProfile{}, err
			}
			return profile, nil
		}
	}
	return debugconfig.AdapterProfile{}, fmt.Errorf("debug adapter profile %q was not found", id)
}

func (s *Service) runtimeLocked(workspaceID string) *workspaceRuntime {
	runtime := s.runtimes[workspaceID]
	if runtime == nil {
		runtime = &workspaceRuntime{sessions: map[string]*session{}, groups: map[string]*sessionGroup{}}
		s.runtimes[workspaceID] = runtime
	}
	return runtime
}
func (s *Service) sessionLocked(workspaceID, sessionID string) (*session, error) {
	runtime := s.runtimes[workspaceID]
	if runtime == nil || runtime.sessions[sessionID] == nil {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	return runtime.sessions[sessionID], nil
}

func (s *Service) snapshotLocked(workspaceID string, runtime *workspaceRuntime, state debugconfig.State) Snapshot {
	result := Snapshot{
		WorkspaceID: workspaceID, Sequence: runtime.sequence, State: state,
		Sessions: make([]SessionSnapshot, 0, len(runtime.sessions)),
		Groups:   make([]GroupSnapshot, 0, len(runtime.groups)),
	}
	for _, current := range runtime.sessions {
		result.Sessions = append(result.Sessions, s.sessionSnapshotLocked(current))
	}
	sort.SliceStable(result.Sessions, func(i, j int) bool { return result.Sessions[i].StartedAt.Before(result.Sessions[j].StartedAt) })
	for _, group := range runtime.groups {
		result.Groups = append(result.Groups, GroupSnapshot{ID: group.id, Name: group.name, SessionIDs: append([]string(nil), group.sessionIDs...), StopAll: group.stopAll})
	}
	sort.SliceStable(result.Groups, func(i, j int) bool { return result.Groups[i].Name < result.Groups[j].Name })
	return result
}

func (s *Service) sessionSnapshotLocked(current *session) SessionSnapshot {
	breakpoints := make([]BreakpointStatus, 0, len(current.breakpointStatuses))
	for _, breakpoint := range current.breakpointStatuses {
		breakpoints = append(breakpoints, breakpoint)
	}
	sort.SliceStable(breakpoints, func(i, j int) bool { return breakpoints[i].StateID < breakpoints[j].StateID })
	return SessionSnapshot{ID: current.id, WorkspaceID: current.workspaceID, GroupID: current.groupID, ParentSessionID: current.parentID, ConfigurationID: current.configuration.ID, Configuration: current.configuration.Name, AdapterProfileID: current.profile.ID, Request: current.configuration.Request, Status: current.status, Revision: current.revision, StopGeneration: current.stopGeneration, StoppedReason: current.stoppedReason, StoppedText: current.stoppedText, ThreadID: current.threadID, AllThreadsStopped: current.allThreadsStopped, Capabilities: cloneMap(current.capabilities), Location: cloneLocation(current.location), Error: current.err, StartedAt: current.startedAt, EndedAt: current.endedAt, LastOutputSequence: current.outputSequence, Output: append([]OutputEntry(nil), current.output...), Breakpoints: breakpoints, TraceDAP: current.traceDAP}
}

func (s *Service) publishSession(workspaceID, sessionID, event string, body json.RawMessage, message string) {
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	current := runtime.sessions[sessionID]
	if current == nil {
		s.mu.Unlock()
		return
	}
	runtime.sequence++
	snapshot := s.sessionSnapshotLocked(current)
	notification := Event{Type: "debug_event", WorkspaceID: workspaceID, SessionID: sessionID, GroupID: current.groupID, Sequence: runtime.sequence, Revision: current.revision, Event: event, Body: body, Session: &snapshot, Message: message}
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(notification)
	}
}

func (s *Service) publishState(workspaceID string, state debugconfig.State) {
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	runtime.sequence++
	copyState := state
	notification := Event{Type: "debug_event", WorkspaceID: workspaceID, Sequence: runtime.sequence, Event: "state_changed", State: &copyState}
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(notification)
	}
}

func (s *Service) publishRaw(workspaceID, sessionID, event string, body json.RawMessage) {
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	current := runtime.sessions[sessionID]
	if current == nil {
		s.mu.Unlock()
		return
	}
	runtime.sequence++
	notification := Event{Type: "debug_event", WorkspaceID: workspaceID, SessionID: sessionID, GroupID: current.groupID, Sequence: runtime.sequence, Revision: current.revision, Event: event, Body: body}
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(notification)
	}
}

func cloneMap[T any](input map[string]T) map[string]T {
	if len(input) == 0 {
		return map[string]T{}
	}
	result := make(map[string]T, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
func cloneLocation(input *SourceLocation) *SourceLocation {
	if input == nil {
		return nil
	}
	result := *input
	if input.Ref != nil {
		ref := *input.Ref
		result.Ref = &ref
	}
	return &result
}
func usesThread(command string) bool {
	switch command {
	case "continue", "pause", "next", "stepIn", "stepOut", "stepBack", "reverseContinue", "stackTrace", "terminateThreads":
		return true
	}
	return false
}
func isSerializedCommand(command string) bool {
	switch command {
	case "continue", "pause", "next", "stepIn", "stepOut", "stepBack", "reverseContinue", "goto", "restartFrame", "terminateThreads", "setVariable", "setExpression", "writeMemory", "setBreakpoints", "setFunctionBreakpoints", "setExceptionBreakpoints", "setDataBreakpoints", "setInstructionBreakpoints":
		return true
	}
	return false
}
func isStopSensitiveResponse(command string) bool {
	switch command {
	case "continue", "next", "stepIn", "stepOut", "stepBack", "reverseContinue", "goto", "pause":
		return false
	}
	return true
}
func requireCapability(current *session, command string) error {
	capability := map[string]string{"stepBack": "supportsStepBack", "reverseContinue": "supportsStepBack", "restartFrame": "supportsRestartFrame", "goto": "supportsGotoTargetsRequest", "gotoTargets": "supportsGotoTargetsRequest", "terminateThreads": "supportsTerminateThreadsRequest", "setVariable": "supportsSetVariable", "setExpression": "supportsSetExpression", "completions": "supportsCompletionsRequest", "modules": "supportsModulesRequest", "loadedSources": "supportsLoadedSourcesRequest", "exceptionInfo": "supportsExceptionInfoRequest", "inlineValues": "supportsInlineValues", "readMemory": "supportsReadMemoryRequest", "writeMemory": "supportsWriteMemoryRequest", "disassemble": "supportsDisassembleRequest", "cancel": "supportsCancelRequest", "breakpointLocations": "supportsBreakpointLocationsRequest", "dataBreakpointInfo": "supportsDataBreakpoints", "setDataBreakpoints": "supportsDataBreakpoints", "setInstructionBreakpoints": "supportsInstructionBreakpoints", "setFunctionBreakpoints": "supportsFunctionBreakpoints"}[command]
	if capability != "" && !boolCapability(current.capabilities, capability) {
		return fmt.Errorf("%w: %s", ErrUnsupported, command)
	}
	return nil
}
func boolCapability(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}
func isTerminalStatus(status string) bool {
	return status == StatusTerminated || status == StatusFailed
}
func errorIsStale(err error) bool {
	return errors.Is(err, ErrStaleSession) || errors.Is(err, ErrStaleStop)
}
