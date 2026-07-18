package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	debugRequestTimeout     = 15 * time.Second
	debugLaunchTimeout      = 2 * time.Minute
	debugInitializedTimeout = 30 * time.Second
)

type debugSession struct {
	workspace     Workspace
	id            string
	configuration string
	adapterType   string
	config        map[string]any
	status        string
	err           string
	ctx           context.Context
	cancel        context.CancelFunc
	conn          *dapConnection
	adapter       *debugAdapterHandle

	initialized     chan struct{}
	initializedOnce sync.Once
	capabilities    json.RawMessage
	threadID        int
	frameID         int
	location        *DebugSourceLocation
	pauseGeneration uint64
	breakpoints     map[string][]DebugBreakpoint // absolute source path -> adapter results
	output          debugOutputBuffer
}

type debugManager struct {
	service  *SystemService
	registry *debugAdapterRegistry

	mu       sync.Mutex
	revision uint64
	session  *debugSession
	stored   debugPersistentState
}

func newDebugManager(service *SystemService) *debugManager {
	manager := &debugManager{service: service, registry: newDebugAdapterRegistry()}
	manager.stored = loadDebugPersistentState(debugPersistentStatePath(service.storePath))
	return manager
}

func (s *SystemService) LoadDebugState(workspaceID string) (DebugState, error) {
	workspace, _, err := s.workspaceAndSettings(strings.TrimSpace(workspaceID))
	if err != nil {
		return DebugState{}, err
	}
	if s.debugger == nil {
		return DebugState{WorkspaceID: workspace.ID, Status: DebugStatusIdle}, nil
	}
	return s.debugger.state(workspace), nil
}

func (s *SystemService) StartDebugSession(workspaceID string, request DebugStartRequest) (DebugState, error) {
	workspace, _, err := s.workspaceAndSettings(strings.TrimSpace(workspaceID))
	if err != nil {
		return DebugState{}, err
	}
	s.taskMu.Lock()
	raw, err := resolveWorkspaceDebugConfiguration(workspace, request.ConfigurationName, request.CurrentFile)
	s.taskMu.Unlock()
	if err != nil {
		return DebugState{}, err
	}
	config, err := prepareDebugConfiguration(workspace, raw, request.CurrentFile)
	if err != nil {
		return DebugState{}, err
	}
	if s.debugger == nil {
		return DebugState{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.start(workspace, config)
}

func (s *SystemService) ContinueDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	return s.controlDebugSession(workspaceID, request, "continue")
}

func (s *SystemService) PauseDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	return s.controlDebugSession(workspaceID, request, "pause")
}

func (s *SystemService) StepOverDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	return s.controlDebugSession(workspaceID, request, "next")
}

func (s *SystemService) StepIntoDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	return s.controlDebugSession(workspaceID, request, "stepIn")
}

func (s *SystemService) StepOutDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	return s.controlDebugSession(workspaceID, request, "stepOut")
}

func (s *SystemService) controlDebugSession(workspaceID string, request DebugSessionRequest, command string) (DebugState, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugState{}, err
	}
	if s.debugger == nil {
		return DebugState{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.control(workspaceID, request.SessionID, command)
}

func (s *SystemService) StopDebugSession(workspaceID string, request DebugSessionRequest) (DebugState, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugState{}, err
	}
	if s.debugger == nil {
		return DebugState{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.stop(workspaceID, request.SessionID)
}

func (m *debugManager) state(workspace Workspace) DebugState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil && m.session.workspace.ID == workspace.ID {
		return m.snapshotLocked(m.session)
	}
	return DebugState{
		WorkspaceID: workspace.ID,
		Revision:    m.revision,
		Status:      DebugStatusIdle,
		Breakpoints: m.storedBreakpointsLocked(workspace),
	}
}

func (m *debugManager) start(workspace Workspace, config map[string]any) (DebugState, error) {
	adapterType := strings.ToLower(debugString(config, "type"))
	adapter, err := m.registry.adapter(adapterType)
	if err != nil {
		return DebugState{}, err
	}

	m.mu.Lock()
	if existing := m.session; existing != nil && existing.status != DebugStatusTerminated && existing.status != DebugStatusError {
		state := m.snapshotLocked(existing)
		m.mu.Unlock()
		return state, fmt.Errorf("a debug session is already active")
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &debugSession{
		workspace:     workspace,
		id:            uuid.NewString(),
		configuration: debugString(config, "name"),
		adapterType:   adapterType,
		config:        cloneDebugMap(config),
		status:        DebugStatusStarting,
		ctx:           ctx,
		cancel:        cancel,
		initialized:   make(chan struct{}),
		breakpoints:   make(map[string][]DebugBreakpoint),
	}
	if session.configuration == "" {
		session.configuration = adapterType + " launch"
	}
	m.session = session
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "starting", State: &state})
	go m.runStart(session, adapter)
	return state, nil
}

func (m *debugManager) runStart(session *debugSession, adapter debugAdapter) {
	handle, err := adapter.Start(session.ctx, session.config, func(category string, output string) {
		m.appendOutput(session, category, output)
	})
	if err != nil {
		if session.ctx.Err() != nil {
			m.finish(session, DebugStatusTerminated, "")
		} else {
			m.failSession(session, err)
		}
		return
	}

	m.mu.Lock()
	if m.session != session || session.status != DebugStatusStarting {
		m.mu.Unlock()
		handle.stop()
		return
	}
	session.adapter = handle
	m.mu.Unlock()

	conn := newDAPConnection(handle.transport,
		func(event dapEnvelope) { m.handleDAPEvent(session, event) },
		func(err error) { m.handleDAPClose(session, err) },
	)
	m.mu.Lock()
	if m.session != session || session.status != DebugStatusStarting {
		m.mu.Unlock()
		_ = conn.Close()
		handle.stop()
		return
	}
	session.conn = conn
	m.mu.Unlock()

	initializeCtx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
	initialize, err := conn.request(initializeCtx, "initialize", map[string]any{
		"clientID":                     "echo",
		"clientName":                   "Echo",
		"adapterID":                    session.adapterType,
		"pathFormat":                   "path",
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"supportsVariableType":         true,
		"supportsVariablePaging":       true,
		"supportsRunInTerminalRequest": false,
	})
	cancel()
	if err != nil {
		m.failSession(session, fmt.Errorf("initialize debugger: %w", err))
		return
	}
	m.mu.Lock()
	if m.session == session {
		session.capabilities = append(json.RawMessage(nil), initialize.Body...)
	}
	m.mu.Unlock()

	launchArguments := cloneDebugMap(session.config)
	delete(launchArguments, "name")
	delete(launchArguments, "type")
	delete(launchArguments, "dlvCwd")
	launchCtx, launchCancel := context.WithTimeout(session.ctx, debugLaunchTimeout)
	launchResult := make(chan error, 1)
	go func() {
		_, err := conn.request(launchCtx, "launch", launchArguments)
		launchResult <- err
	}()

	initializedTimer := time.NewTimer(debugInitializedTimeout)
	defer initializedTimer.Stop()
	select {
	case <-session.initialized:
	case err := <-launchResult:
		launchCancel()
		if err == nil {
			err = fmt.Errorf("debug adapter completed launch before initialization")
		}
		m.failSession(session, fmt.Errorf("launch debugger: %w", err))
		return
	case <-initializedTimer.C:
		launchCancel()
		m.failSession(session, fmt.Errorf("timed out waiting for debugger initialization"))
		return
	case <-session.ctx.Done():
		launchCancel()
		m.finish(session, DebugStatusTerminated, "")
		return
	}

	if err := m.sendAllBreakpoints(session); err != nil {
		launchCancel()
		m.failSession(session, err)
		return
	}
	configureCtx, configureCancel := context.WithTimeout(session.ctx, debugRequestTimeout)
	_, err = conn.request(configureCtx, "configurationDone", map[string]any{})
	configureCancel()
	if err != nil {
		launchCancel()
		m.failSession(session, fmt.Errorf("finish debug configuration: %w", err))
		return
	}
	select {
	case err = <-launchResult:
	case <-launchCtx.Done():
		err = launchCtx.Err()
	}
	launchCancel()
	if err != nil {
		if session.ctx.Err() != nil {
			m.finish(session, DebugStatusTerminated, "")
		} else {
			m.failSession(session, fmt.Errorf("launch debugger: %w", err))
		}
		return
	}

	m.mu.Lock()
	if m.session != session || session.status != DebugStatusStarting {
		m.mu.Unlock()
		return
	}
	session.status = DebugStatusRunning
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "running", State: &state})
}

func (m *debugManager) control(workspaceID string, sessionID string, command string) (DebugState, error) {
	m.mu.Lock()
	session, err := m.requireSessionLocked(workspaceID, sessionID)
	if err != nil {
		m.mu.Unlock()
		return DebugState{}, err
	}
	if command == "pause" {
		if session.status != DebugStatusRunning {
			state := m.snapshotLocked(session)
			m.mu.Unlock()
			return state, fmt.Errorf("debugger is not running")
		}
	} else if session.status != DebugStatusPaused {
		state := m.snapshotLocked(session)
		m.mu.Unlock()
		return state, fmt.Errorf("debugger is not paused")
	}
	conn := session.conn
	threadID := session.threadID
	m.mu.Unlock()

	arguments := map[string]any{}
	if command == "pause" && threadID == 0 {
		ctx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
		response, threadErr := conn.request(ctx, "threads", map[string]any{})
		cancel()
		if threadErr == nil {
			var body struct {
				Threads []DebugThread `json:"threads"`
			}
			if json.Unmarshal(response.Body, &body) == nil && len(body.Threads) > 0 {
				threadID = body.Threads[0].ID
			}
		}
		// Delve documents -1 as its synthetic "current goroutine" thread
		// while the debuggee is running and no stopped thread is selected.
		if threadID == 0 && session.adapterType == "go" {
			threadID = -1
		}
	}
	if threadID > 0 {
		arguments["threadId"] = threadID
	} else if command == "pause" && threadID == -1 {
		arguments["threadId"] = threadID
	}
	ctx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
	_, err = conn.request(ctx, command, arguments)
	cancel()
	if err != nil {
		return DebugState{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != session {
		return DebugState{}, fmt.Errorf("debug session was replaced")
	}
	if command != "pause" {
		session.status = DebugStatusRunning
		session.frameID = 0
		session.location = nil
		session.pauseGeneration++
		m.revision++
		state := m.snapshotLocked(session)
		go m.emit(DebugEvent{Type: "continued", State: &state})
	}
	return m.snapshotLocked(session), nil
}

func (m *debugManager) stop(workspaceID string, sessionID string) (DebugState, error) {
	m.mu.Lock()
	session, err := m.requireSessionLocked(workspaceID, sessionID)
	if err != nil {
		m.mu.Unlock()
		return DebugState{}, err
	}
	if session.status == DebugStatusTerminated || session.status == DebugStatusError {
		state := m.snapshotLocked(session)
		m.mu.Unlock()
		return state, nil
	}
	if session.status != DebugStatusStopping {
		session.status = DebugStatusStopping
		m.revision++
	}
	conn := session.conn
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "stopping", State: &state})

	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), debugAdapterStopTimeout)
		_, _ = conn.request(ctx, "disconnect", map[string]any{"terminateDebuggee": true})
		cancel()
	}
	session.cancel()
	if conn != nil {
		_ = conn.Close()
	}
	if session.adapter != nil {
		session.adapter.stop()
	}
	m.finish(session, DebugStatusTerminated, "")
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(session), nil
}

func (m *debugManager) finish(session *debugSession, status string, message string) {
	m.mu.Lock()
	if m.session != session {
		m.mu.Unlock()
		return
	}
	if session.status == DebugStatusTerminated && status == DebugStatusTerminated {
		m.mu.Unlock()
		return
	}
	session.status = status
	session.err = message
	session.frameID = 0
	session.location = nil
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: status, State: &state, Message: message})
}

func (m *debugManager) failSession(session *debugSession, err error) {
	if err == nil {
		return
	}
	m.finish(session, DebugStatusError, err.Error())
	session.cancel()
	if session.conn != nil {
		_ = session.conn.Close()
	}
	if session.adapter != nil {
		session.adapter.stop()
	}
}

func (m *debugManager) handleDAPClose(session *debugSession, err error) {
	m.mu.Lock()
	if m.session != session || session.status == DebugStatusStopping || session.status == DebugStatusTerminated || session.status == DebugStatusError {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	if isDAPConnectionClosed(err) || err == io.EOF {
		m.finish(session, DebugStatusTerminated, "")
		return
	}
	m.failSession(session, fmt.Errorf("debug adapter connection closed: %w", err))
}

func (m *debugManager) requireSessionLocked(workspaceID string, sessionID string) (*debugSession, error) {
	session := m.session
	if session == nil {
		return nil, fmt.Errorf("no debug session is active")
	}
	if strings.TrimSpace(workspaceID) == "" || session.workspace.ID != strings.TrimSpace(workspaceID) {
		return nil, fmt.Errorf("debug session belongs to a different workspace")
	}
	if strings.TrimSpace(sessionID) == "" || session.id != strings.TrimSpace(sessionID) {
		return nil, fmt.Errorf("debug session is stale")
	}
	return session, nil
}

func (m *debugManager) appendOutput(session *debugSession, category string, output string) {
	m.mu.Lock()
	if m.session != session {
		m.mu.Unlock()
		return
	}
	session.output.append(output)
	m.revision++
	revision := m.revision
	m.mu.Unlock()
	m.emit(DebugEvent{
		WorkspaceID: session.workspace.ID,
		SessionID:   session.id,
		Revision:    revision,
		Type:        "output",
		Category:    category,
		Output:      output,
	})
}

func (m *debugManager) snapshotLocked(session *debugSession) DebugState {
	state := DebugState{
		WorkspaceID:     session.workspace.ID,
		SessionID:       session.id,
		Revision:        m.revision,
		Status:          session.status,
		Configuration:   session.configuration,
		AdapterType:     session.adapterType,
		ThreadID:        session.threadID,
		FrameID:         session.frameID,
		CurrentLocation: cloneDebugLocation(session.location),
		Breakpoints:     flattenDebugBreakpoints(session.breakpoints),
		Output:          session.output.String(),
		Error:           session.err,
		Capabilities:    append(json.RawMessage(nil), session.capabilities...),
	}
	if len(state.Breakpoints) == 0 {
		state.Breakpoints = m.storedBreakpointsLocked(session.workspace)
	}
	return state
}

func (m *debugManager) emit(event DebugEvent) {
	if event.State != nil {
		event.WorkspaceID = event.State.WorkspaceID
		event.SessionID = event.State.SessionID
		event.Revision = event.State.Revision
	}
	m.service.emitRuntimeEvent(debugEventName, event)
	if m.service.ctx != nil {
		runtime.EventsEmit(m.service.ctx, debugEventName, event)
	}
}

func (m *debugManager) focusEchoWindow() {
	// Wails' WindowShow restores a minimised window and, on desktop
	// platforms, brings it to the foreground and focuses it. A nil context is
	// expected in service tests and web-only use.
	if m.service.ctx != nil {
		runtime.WindowShow(m.service.ctx)
	}
}

func (m *debugManager) shutdown() {
	m.mu.Lock()
	session := m.session
	m.mu.Unlock()
	if session == nil {
		return
	}
	_, _ = m.stop(session.workspace.ID, session.id)
}

func cloneDebugLocation(location *DebugSourceLocation) *DebugSourceLocation {
	if location == nil {
		return nil
	}
	clone := *location
	return &clone
}
