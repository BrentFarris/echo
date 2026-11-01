package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/editorfs"
	"sort"
	"sync"
	"time"
)

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
	provider             Provider
	launch               *Launch
	release              func()
	done                 chan struct{}
	cleaned              chan struct{}
	ready                chan struct{}
	readyOnce            sync.Once
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
	handle               *Adapter
	output               []OutputEntry
	outputBytes          int
	outputSequence       uint64
	breakpointSources    map[string]bool
	breakpointStatuses   map[string]BreakpointStatus
	adapterBreakpointIDs map[int]string
	traceDAP             bool
	controlMu            sync.Mutex
	step                 *stepOperation
	sources              map[string]adapterSource
	frames               map[int]Frame
}

type Service struct {
	closed        bool
	lifetimes     sync.WaitGroup
	options       Options
	state         *debugconfig.StateStore
	fs            *editorfs.Service
	mu            sync.Mutex
	runtimes      map[string]*workspaceRuntime
	notify        func(Event)
	runInTerminal func(context.Context, string, string, map[string]any) (map[string]any, error)
	stopTerminals func(string, string)
}

func New(options Options) *Service {
	return &Service{options: options, state: options.State, fs: options.FS, runtimes: map[string]*workspaceRuntime{}}
}
func (s *Service) workspace(id string) (string, error) {
	if s.options.Resolve == nil {
		return id, nil
	}
	return id, s.options.Resolve(id)
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
	if request.ExpectedRevision != 0 && request.ExpectedRevision != current.revision && (isSerializedCommand(command) || request.StopGeneration == 0) {
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
	generation := current.stopGeneration
	if command == "pause" && current.step != nil {
		current.step.cancel()
		result := RequestResponse{WorkspaceID: workspaceID, SessionID: sessionID, Revision: current.revision, StopGeneration: current.stopGeneration}
		s.mu.Unlock()
		return result, nil
	}
	if current.step != nil && isSerializedCommand(command) {
		s.mu.Unlock()
		return RequestResponse{}, fmt.Errorf("a debug step is in progress; pause or stop it first")
	}
	s.mu.Unlock()
	if connection == nil {
		return RequestResponse{}, fmt.Errorf("debug adapter is not connected")
	}
	arguments := cloneMap(request.Arguments)
	if command == "source" {
		return s.sourceRequest(ctx, current, request)
	}
	if command == "evaluate" {
		s.mu.Lock()
		if current.launch != nil && current.launch.Expression != nil {
			if expression, ok := arguments["expression"].(string); ok {
				arguments["expression"] = current.launch.Expression(current.frames[intValue(arguments["frameId"])], expression)
			}
		}
		s.mu.Unlock()
	}
	translatedArguments, translateErr := s.translateDAPArguments(workspaceID, arguments)
	if translateErr != nil {
		return RequestResponse{}, translateErr
	}
	arguments, _ = translatedArguments.(map[string]any)
	if usesThread(command) && arguments["threadId"] == nil && threadID != 0 {
		arguments["threadId"] = threadID
	}
	if result, started := s.startSourceStep(current, command, arguments); started {
		return result, nil
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
		s.markRunningIfGeneration(workspaceID, sessionID, command, generation)
	}
	body := s.translateSessionBody(current, response.Body)
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
	// Frame IDs may be reused after a stop. A late stack response must not
	// change how expressions in the new stop's selected frame are parsed.
	if command == "stackTrace" && generation == current.stopGeneration {
		var stack struct {
			Frames []Frame `json:"stackFrames"`
		}
		if json.Unmarshal(response.Body, &stack) == nil {
			if current.frames == nil {
				current.frames = map[int]Frame{}
			}
			for _, frame := range stack.Frames {
				current.frames[frame.ID] = frame
			}
		}
	}
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
	if expectedRevision != 0 && current.revision != expectedRevision {
		actual := current.revision
		s.mu.Unlock()
		return &RevisionError{Expected: expectedRevision, Actual: actual}
	}
	if isStoppingOrTerminal(current.status) {
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
	cancel := current.cancel
	s.mu.Unlock()
	s.publishSession(workspaceID, sessionID, "session_terminating", nil, "")
	// Stop is the escape hatch for every debugger lifecycle phase. Cancel the
	// session before entering the serialized control lane so an unbounded launch
	// or restart request cannot prevent the developer from stopping it.
	cancel()
	current.controlMu.Lock()
	defer current.controlMu.Unlock()
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

func isStoppingOrTerminal(status string) bool {
	return status == StatusTerminating || isTerminalStatus(status)
}

func errorIsStale(err error) bool {
	return errors.Is(err, ErrStaleSession) || errors.Is(err, ErrStaleStop)
}
