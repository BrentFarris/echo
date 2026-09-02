package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brent/echo/internal/debugconfig"
)

func (s *Service) runSession(current *session) {
	workspace, err := s.workspace(current.workspaceID)
	if err != nil {
		s.failSession(current.workspaceID, current.id, err)
		return
	}
	currentFile := ""
	if current.startRequest.CurrentFile != nil {
		currentFile, err = s.resolveSource(current.workspaceID, *current.startRequest.CurrentFile)
		if err != nil {
			s.failSession(current.workspaceID, current.id, err)
			return
		}
	}
	options, err := s.executionOptions(workspace, currentFile, current.startRequest.Inputs, current.startRequest.SelectedText)
	if err != nil {
		s.failSession(current.workspaceID, current.id, err)
		return
	}
	arguments := cloneMap(current.configuration.Arguments)
	arguments["name"] = current.configuration.Name
	arguments["type"] = current.profile.AdapterID
	arguments["request"] = current.configuration.Request
	if current.startRequest.NoDebug {
		arguments["noDebug"] = true
	}
	expanded, err := debugconfig.ExpandValue(arguments, options)
	if err != nil {
		s.failSession(current.workspaceID, current.id, err)
		return
	}
	launchArguments, _ := expanded.(map[string]any)
	prepareLaunchArguments(current.profile, current.configuration.Request, launchArguments)
	adapterWorkingDirectory := launchAdapterWorkingDirectory(current.profile, current.configuration.Request, launchArguments, options)
	logOutput := func(category, output string) { s.appendOutput(current.workspaceID, current.id, category, output, nil) }
	if err := s.runHook(current.ctx, workspace, current.configuration.PreLaunch, options, "lifecycle", logOutput); err != nil {
		s.failSession(current.workspaceID, current.id, fmt.Errorf("pre-launch hook: %w", err))
		return
	}
	handle, err := s.startAdapter(current.ctx, workspace, current.profile, options, adapterWorkingDirectory, logOutput)
	if err != nil {
		s.failSession(current.workspaceID, current.id, err)
		return
	}
	s.mu.Lock()
	active, activeErr := s.sessionLocked(current.workspaceID, current.id)
	if activeErr != nil || active != current || isTerminalStatus(current.status) {
		s.mu.Unlock()
		handle.stop()
		return
	}
	current.handle = handle
	s.mu.Unlock()
	connection := newDAPConnection(handle.transport, func(event dapEnvelope) { s.handleDAPEvent(current.workspaceID, current.id, event) }, func(ctx context.Context, request dapEnvelope) (any, error) {
		return s.handleReverseRequest(ctx, current.workspaceID, current.id, request)
	}, func(closeErr error) { s.handleDAPClose(current.workspaceID, current.id, closeErr) })
	connection.setTrace(func(direction string, data []byte) {
		s.appendDAPTrace(current.workspaceID, current.id, direction, data)
	})
	s.mu.Lock()
	active, activeErr = s.sessionLocked(current.workspaceID, current.id)
	if activeErr != nil || active != current || isTerminalStatus(current.status) {
		s.mu.Unlock()
		_ = connection.Close()
		handle.stop()
		return
	}
	current.conn = connection
	s.mu.Unlock()

	initializeCtx, cancel := context.WithTimeout(current.ctx, requestTimeout)
	initialize, err := connection.request(initializeCtx, "initialize", map[string]any{
		"clientID": "echo", "clientName": "Echo", "adapterID": current.profile.AdapterID, "locale": "en-us", "pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true,
		"supportsVariableType": true, "supportsVariablePaging": true, "supportsRunInTerminalRequest": s.supportsRunInTerminal(), "supportsMemoryReferences": true, "supportsProgressReporting": true, "supportsInvalidatedEvent": true, "supportsMemoryEvent": true, "supportsArgsCanBeInterpretedByShell": false, "supportsStartDebuggingRequest": true,
	})
	cancel()
	if err != nil {
		s.failSession(current.workspaceID, current.id, fmt.Errorf("initialize debugger: %w", err))
		return
	}
	capabilities := map[string]any{}
	_ = json.Unmarshal(initialize.Body, &capabilities)
	connection.setSupportsCancel(boolCapability(capabilities, "supportsCancelRequest"))
	s.mu.Lock()
	if active, _ := s.sessionLocked(current.workspaceID, current.id); active == current {
		current.capabilities = capabilities
		current.status = StatusConfiguring
		current.revision++
	}
	s.mu.Unlock()
	s.publishSession(current.workspaceID, current.id, "session_configuring", initialize.Body, "")

	launchCtx, launchCancel := context.WithTimeout(current.ctx, launchTimeout)
	defer launchCancel()
	launchResult := make(chan error, 1)
	go func() {
		_, callErr := connection.request(launchCtx, current.configuration.Request, launchArguments)
		launchResult <- callErr
	}()
	initializedTimer := time.NewTimer(initializedTimeout)
	defer initializedTimer.Stop()
	launchFinished := false
	waitingForInitialized := true
	for waitingForInitialized {
		select {
		case <-current.initialized:
			waitingForInitialized = false
		case launchErr := <-launchResult:
			launchFinished = true
			if launchErr != nil {
				s.failSession(current.workspaceID, current.id, fmt.Errorf("%s debugger: %w", current.configuration.Request, launchErr))
				return
			}
			launchResult = nil
		case <-initializedTimer.C:
			s.failSession(current.workspaceID, current.id, fmt.Errorf("timed out waiting for debugger initialization"))
			return
		case <-current.ctx.Done():
			return
		}
	}
	if err := s.sendAllBreakpoints(current); err != nil {
		s.failSession(current.workspaceID, current.id, err)
		return
	}
	if boolCapability(capabilities, "supportsConfigurationDoneRequest") {
		configureCtx, configureCancel := context.WithTimeout(current.ctx, requestTimeout)
		_, err = connection.request(configureCtx, "configurationDone", map[string]any{})
		configureCancel()
		if err != nil {
			s.failSession(current.workspaceID, current.id, fmt.Errorf("finish debug configuration: %w", err))
			return
		}
	}
	if !launchFinished {
		select {
		case launchErr := <-launchResult:
			if launchErr != nil {
				s.failSession(current.workspaceID, current.id, fmt.Errorf("%s debugger: %w", current.configuration.Request, launchErr))
				return
			}
		case <-launchCtx.Done():
			s.failSession(current.workspaceID, current.id, launchCtx.Err())
			return
		}
	}
	s.mu.Lock()
	if active, _ := s.sessionLocked(current.workspaceID, current.id); active == current && !isTerminalStatus(current.status) && current.status != StatusStopped {
		current.status = StatusRunning
		current.revision++
	}
	s.mu.Unlock()
	s.publishSession(current.workspaceID, current.id, "session_running", nil, "")
}

func (s *Service) handleDAPEvent(workspaceID, sessionID string, event dapEnvelope) {
	switch event.Event {
	case "initialized":
		s.mu.Lock()
		if current, err := s.sessionLocked(workspaceID, sessionID); err == nil {
			current.initializedOnce.Do(func() { close(current.initialized) })
		}
		s.mu.Unlock()
		s.publishRaw(workspaceID, sessionID, event.Event, event.Body)
	case "stopped":
		var body struct {
			Reason            string `json:"reason"`
			Description       string `json:"description"`
			Text              string `json:"text"`
			ThreadID          int    `json:"threadId"`
			AllThreadsStopped bool   `json:"allThreadsStopped"`
		}
		_ = json.Unmarshal(event.Body, &body)
		s.mu.Lock()
		if current, err := s.sessionLocked(workspaceID, sessionID); err == nil {
			current.status = StatusStopped
			current.stoppedReason = body.Reason
			current.stoppedText = firstNonEmpty(body.Description, body.Text)
			current.threadID = body.ThreadID
			current.allThreadsStopped = body.AllThreadsStopped
			current.stopGeneration++
			current.revision++
			current.location = nil
		}
		s.mu.Unlock()
		s.publishSession(workspaceID, sessionID, "stopped", event.Body, "")
		go s.hydrateLocation(workspaceID, sessionID)
	case "continued":
		s.markRunning(workspaceID, sessionID, "continued")
	case "output":
		var body struct {
			Category string         `json:"category"`
			Output   string         `json:"output"`
			Data     map[string]any `json:"data"`
		}
		_ = json.Unmarshal(event.Body, &body)
		metadata := map[string]any{}
		if translated := s.translateDAPBody(workspaceID, event.Body); json.Unmarshal(translated, &metadata) == nil {
			delete(metadata, "category")
			delete(metadata, "output")
			delete(metadata, "data")
			for key, value := range body.Data {
				metadata[key] = value
			}
		}
		if body.Category == "telemetry" {
			s.appendOutput(workspaceID, sessionID, "telemetry", body.Output, metadata)
		} else {
			s.appendOutput(workspaceID, sessionID, firstNonEmpty(body.Category, "console"), body.Output, metadata)
		}
	case "capabilities":
		var body struct {
			Capabilities map[string]any `json:"capabilities"`
		}
		_ = json.Unmarshal(event.Body, &body)
		s.mu.Lock()
		if current, err := s.sessionLocked(workspaceID, sessionID); err == nil {
			for key, value := range body.Capabilities {
				current.capabilities[key] = value
			}
			current.revision++
		}
		s.mu.Unlock()
		s.publishSession(workspaceID, sessionID, "capabilities", event.Body, "")
	case "terminated":
		s.finishSession(workspaceID, sessionID, StatusTerminated, "", 0)
	case "exited":
		s.publishRaw(workspaceID, sessionID, event.Event, event.Body)
	case "invalidated":
		s.mu.Lock()
		if current, err := s.sessionLocked(workspaceID, sessionID); err == nil && current.status == StatusStopped {
			current.stopGeneration++
			current.revision++
		}
		s.mu.Unlock()
		s.publishSession(workspaceID, sessionID, event.Event, event.Body, "")
	case "breakpoint":
		s.updateBreakpointEvent(workspaceID, sessionID, event.Body)
		s.publishSession(workspaceID, sessionID, event.Event, s.translateDAPBody(workspaceID, event.Body), "")
	case "thread", "process", "module", "loadedSource", "memory", "progressStart", "progressUpdate", "progressEnd":
		s.publishRaw(workspaceID, sessionID, event.Event, s.translateDAPBody(workspaceID, event.Body))
	case "debugpyAttach":
		go s.startDebugpyChild(workspaceID, sessionID, event.Body)
	default:
		s.publishRaw(workspaceID, sessionID, event.Event, s.translateDAPBody(workspaceID, event.Body))
	}
}

func (s *Service) handleReverseRequest(ctx context.Context, workspaceID, sessionID string, request dapEnvelope) (any, error) {
	switch request.Command {
	case "runInTerminal":
		s.mu.Lock()
		run := s.runInTerminal
		s.mu.Unlock()
		if run == nil {
			return nil, fmt.Errorf("%w: integrated debug terminals are unavailable", ErrUnsupported)
		}
		var arguments map[string]any
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return nil, err
		}
		kind, _ := arguments["kind"].(string)
		if kind == "external" {
			return nil, fmt.Errorf("external GUI terminals are not supported; use integratedTerminal")
		}
		return run(ctx, workspaceID, sessionID, arguments)
	case "startDebugging":
		var arguments struct {
			Configuration map[string]any `json:"configuration"`
			Request       string         `json:"request"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return nil, err
		}
		return s.startChildConfiguration(workspaceID, sessionID, arguments.Configuration, arguments.Request)
	default:
		return nil, fmt.Errorf("%w: adapter reverse request %s", ErrUnsupported, request.Command)
	}
}

func (s *Service) startChildConfiguration(workspaceID, parentID string, arguments map[string]any, request string) (any, error) {
	s.mu.Lock()
	parent, err := s.sessionLocked(workspaceID, parentID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	configuration := debugconfig.Configuration{ID: "", Name: stringValue(arguments["name"], "Child process"), AdapterProfileID: parent.profile.ID, Request: firstNonEmpty(request, stringValue(arguments["request"], "attach")), Arguments: cloneMap(arguments)}
	delete(configuration.Arguments, "name")
	delete(configuration.Arguments, "request")
	workspace, workspaceErr := s.workspace(workspaceID)
	groupID := parent.groupID
	startRequest := parent.startRequest
	s.mu.Unlock()
	if workspaceErr != nil {
		return nil, workspaceErr
	}
	id, err := s.createSession(workspace, configuration, groupID, parentID, startRequest)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": id}, nil
}

func (s *Service) startDebugpyChild(workspaceID, parentID string, body json.RawMessage) {
	var configuration map[string]any
	if json.Unmarshal(body, &configuration) != nil {
		return
	}
	_, _ = s.startChildConfiguration(workspaceID, parentID, configuration, "attach")
}

func (s *Service) handleDAPClose(workspaceID, sessionID string, closeErr error) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || isTerminalStatus(current.status) || current.status == StatusTerminating {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if isDAPClosed(closeErr) || closeErr == io.EOF {
		s.finishSession(workspaceID, sessionID, StatusTerminated, "", 0)
		return
	}
	s.failSession(workspaceID, sessionID, closeErr)
}

func (s *Service) failSession(workspaceID, sessionID string, failure error) {
	if failure == nil {
		return
	}
	s.finishSession(workspaceID, sessionID, StatusFailed, failure.Error(), 0)
}

func (s *Service) finishSession(workspaceID, sessionID, status, message string, minimumRevision uint64) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || isTerminalStatus(current.status) {
		s.mu.Unlock()
		return
	}
	current.status = status
	current.err = message
	current.threadID = 0
	current.location = nil
	current.revision++
	if current.revision < minimumRevision {
		current.revision = minimumRevision
	}
	ended := time.Now().UTC()
	current.endedAt = &ended
	connection, handle, cancel := current.conn, current.handle, current.cancel
	stopTerminals := s.stopTerminals
	current.conn = nil
	current.handle = nil
	runPost := !current.postStarted
	current.postStarted = true
	configuration := current.configuration
	startRequest := current.startRequest
	groupID := current.groupID
	var postContext context.Context
	var postCancel context.CancelFunc
	if runPost && configuration.PostDebug != nil {
		postContext, postCancel = context.WithTimeout(context.Background(), time.Duration(configuration.PostDebug.TimeoutMS)*time.Millisecond)
		current.postCancel = postCancel
		current.postDone = make(chan struct{})
	}
	postDone := current.postDone
	s.mu.Unlock()
	cancel()
	if connection != nil {
		_ = connection.Close()
	}
	if handle != nil {
		handle.stop()
	}
	if stopTerminals != nil {
		stopTerminals(workspaceID, sessionID)
	}
	s.publishSession(workspaceID, sessionID, status, nil, message)
	if runPost && configuration.PostDebug != nil {
		go func() {
			defer close(postDone)
			defer postCancel()
			defer func() {
				s.mu.Lock()
				if active, lookupErr := s.sessionLocked(workspaceID, sessionID); lookupErr == nil && active == current {
					active.postCancel = nil
				}
				s.mu.Unlock()
			}()
			workspace, workspaceErr := s.workspace(workspaceID)
			if workspaceErr != nil {
				return
			}
			currentFile := ""
			if startRequest.CurrentFile != nil {
				currentFile, _ = s.resolveSource(workspaceID, *startRequest.CurrentFile)
			}
			options, optionsErr := s.executionOptions(workspace, currentFile, startRequest.Inputs, startRequest.SelectedText)
			if optionsErr != nil {
				return
			}
			s.appendOutput(workspaceID, sessionID, "lifecycle", "Running post-debug hook…\n", nil)
			if hookErr := s.runHook(postContext, workspace, configuration.PostDebug, options, "lifecycle", func(category, output string) { s.appendOutput(workspaceID, sessionID, category, output, nil) }); hookErr != nil {
				s.appendOutput(workspaceID, sessionID, "lifecycle", hookErr.Error()+"\n", nil)
			}
		}()
	}
	if groupID != "" {
		s.handleGroupCompletion(workspaceID, groupID, sessionID, status)
	}
}

func (s *Service) handleGroupCompletion(workspaceID, groupID, sessionID, status string) {
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	group := runtime.groups[groupID]
	if group == nil {
		s.mu.Unlock()
		return
	}
	stopAll := group.stopAll && status == StatusFailed
	ids := append([]string(nil), group.sessionIDs...)
	s.mu.Unlock()
	if stopAll {
		for _, id := range ids {
			if id != sessionID {
				_ = s.Stop(workspaceID, id, nil)
			}
		}
	}
}

func (s *Service) appendOutput(workspaceID, sessionID, category, output string, data map[string]any) {
	if output == "" && len(data) == 0 {
		return
	}
	s.mu.Lock()
	runtime := s.runtimeLocked(workspaceID)
	current := runtime.sessions[sessionID]
	if current == nil {
		s.mu.Unlock()
		return
	}
	current.outputSequence++
	entry := OutputEntry{Sequence: current.outputSequence, Category: firstNonEmpty(category, "console"), Output: output, Timestamp: time.Now().UTC(), Data: data}
	current.output = append(current.output, entry)
	current.outputBytes += len(output)
	truncated := false
	for current.outputBytes > outputReplayBytes && len(current.output) > 1 {
		current.outputBytes -= len(current.output[0].Output)
		current.output = current.output[1:]
		truncated = true
	}
	if truncated && len(current.output) > 0 && current.output[0].Category != "echo" {
		current.output = append([]OutputEntry{{Sequence: current.output[0].Sequence - 1, Category: "echo", Output: "Earlier debug output was truncated.\n", Timestamp: time.Now().UTC()}}, current.output...)
	}
	runtime.sequence++
	notification := Event{Type: "debug_event", WorkspaceID: workspaceID, SessionID: sessionID, GroupID: current.groupID, Sequence: runtime.sequence, Revision: current.revision, Event: "output", Output: &entry}
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(notification)
	}
}

func (s *Service) appendDAPTrace(workspaceID, sessionID, direction string, payload []byte) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	enabled := err == nil && current.traceDAP
	s.mu.Unlock()
	if !enabled {
		return
	}
	var value any
	if json.Unmarshal(payload, &value) != nil {
		s.appendOutput(workspaceID, sessionID, "dap", direction+" [malformed DAP payload]\n", nil)
		return
	}
	redactDAPValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	if len(encoded) > 64<<10 {
		encoded = append(encoded[:64<<10], []byte("…[trace truncated]")...)
	}
	s.appendOutput(workspaceID, sessionID, "dap", direction+" "+string(encoded)+"\n", nil)
}

func redactDAPValue(value any) {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			redactDAPValue(item)
		}
	case map[string]any:
		for key, item := range current {
			normalized := strings.ToLower(key)
			if strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") || strings.Contains(normalized, "authorization") || normalized == "env" || normalized == "environment" || normalized == "expression" || normalized == "value" || normalized == "result" || normalized == "output" || normalized == "content" || normalized == "data" {
				current[key] = "[redacted]"
				continue
			}
			redactDAPValue(item)
		}
	}
}

func (s *Service) markRunning(workspaceID, sessionID, reason string) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err == nil && !isTerminalStatus(current.status) {
		changed := current.status != StatusRunning
		current.status = StatusRunning
		current.threadID = 0
		current.location = nil
		current.stoppedReason = ""
		current.stoppedText = ""
		current.allThreadsStopped = false
		if changed {
			current.stopGeneration++
			current.revision++
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.publishSession(workspaceID, sessionID, "continued", nil, reason)
	}
}

func (s *Service) hydrateLocation(workspaceID, sessionID string) {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil || current.conn == nil || current.status != StatusStopped {
		s.mu.Unlock()
		return
	}
	connection := current.conn
	threadID := current.threadID
	generation := current.stopGeneration
	ctx := current.ctx
	s.mu.Unlock()
	if threadID == 0 {
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	response, callErr := connection.request(requestCtx, "stackTrace", map[string]any{"threadId": threadID, "startFrame": 0, "levels": 1})
	cancel()
	if callErr != nil {
		return
	}
	var body struct {
		StackFrames []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Line   int    `json:"line"`
			Column int    `json:"column"`
			Source struct {
				Name            string `json:"name"`
				Path            string `json:"path"`
				SourceReference int    `json:"sourceReference"`
			} `json:"source"`
		} `json:"stackFrames"`
	}
	if json.Unmarshal(response.Body, &body) != nil || len(body.StackFrames) == 0 {
		return
	}
	frame := body.StackFrames[0]
	path := s.adapterPathToHost(workspaceID, frame.Source.Path)
	location := &SourceLocation{Name: firstNonEmpty(frame.Source.Name, frame.Name), Path: path, Ref: s.fileRefForPath(workspaceID, path), SourceReference: frame.Source.SourceReference, Line: frame.Line, Column: frame.Column}
	s.mu.Lock()
	if current, err := s.sessionLocked(workspaceID, sessionID); err == nil && current.stopGeneration == generation && current.status == StatusStopped {
		current.location = location
		current.revision++
	}
	s.mu.Unlock()
	s.publishSession(workspaceID, sessionID, "location", nil, "")
}

func (s *Service) supportsRunInTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runInTerminal != nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}
