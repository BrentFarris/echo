package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *SystemService) LoadDebugThreads(workspaceID string, request DebugThreadsRequest) (DebugThreadsResponse, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugThreadsResponse{}, err
	}
	if s.debugger == nil {
		return DebugThreadsResponse{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.loadThreads(workspaceID, request)
}

func (s *SystemService) LoadDebugStackTrace(workspaceID string, request DebugStackTraceRequest) (DebugStackTraceResponse, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugStackTraceResponse{}, err
	}
	if s.debugger == nil {
		return DebugStackTraceResponse{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.loadStackTrace(workspaceID, request)
}

func (s *SystemService) LoadDebugScopes(workspaceID string, request DebugScopesRequest) (DebugScopesResponse, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugScopesResponse{}, err
	}
	if s.debugger == nil {
		return DebugScopesResponse{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.loadScopes(workspaceID, request)
}

func (s *SystemService) LoadDebugVariables(workspaceID string, request DebugVariablesRequest) (DebugVariablesResponse, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugVariablesResponse{}, err
	}
	if s.debugger == nil {
		return DebugVariablesResponse{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.loadVariables(workspaceID, request)
}

func (s *SystemService) EvaluateDebugExpression(workspaceID string, request DebugEvaluateRequest) (DebugEvaluateResponse, error) {
	if err := s.validateWorkspaceAvailable(strings.TrimSpace(workspaceID)); err != nil {
		return DebugEvaluateResponse{}, err
	}
	if s.debugger == nil {
		return DebugEvaluateResponse{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.evaluate(workspaceID, request)
}

type debugInspection struct {
	session    *debugSession
	conn       *dapConnection
	generation uint64
}

func (m *debugManager) inspection(workspaceID string, sessionID string) (debugInspection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, err := m.requireSessionLocked(workspaceID, sessionID)
	if err != nil {
		return debugInspection{}, err
	}
	if session.status != DebugStatusPaused || session.conn == nil {
		return debugInspection{}, fmt.Errorf("debugger is not paused")
	}
	return debugInspection{session: session, conn: session.conn, generation: session.pauseGeneration}, nil
}

func (m *debugManager) inspectionRevision(inspection debugInspection) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != inspection.session || inspection.session.status != DebugStatusPaused || inspection.session.pauseGeneration != inspection.generation {
		return 0, fmt.Errorf("debug inspection result is stale")
	}
	return m.revision, nil
}

func (m *debugManager) loadThreads(workspaceID string, request DebugThreadsRequest) (DebugThreadsResponse, error) {
	inspection, err := m.inspection(workspaceID, request.SessionID)
	if err != nil {
		return DebugThreadsResponse{}, err
	}
	ctx, cancel := context.WithTimeout(inspection.session.ctx, debugRequestTimeout)
	response, err := inspection.conn.request(ctx, "threads", map[string]any{})
	cancel()
	if err != nil {
		return DebugThreadsResponse{}, err
	}
	var body struct {
		Threads []DebugThread `json:"threads"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return DebugThreadsResponse{}, fmt.Errorf("decode debug threads: %w", err)
	}
	revision, err := m.inspectionRevision(inspection)
	if err != nil {
		return DebugThreadsResponse{}, err
	}
	if body.Threads == nil {
		body.Threads = []DebugThread{}
	}
	return DebugThreadsResponse{WorkspaceID: workspaceID, SessionID: request.SessionID, Revision: revision, Threads: body.Threads}, nil
}

func (m *debugManager) loadStackTrace(workspaceID string, request DebugStackTraceRequest) (DebugStackTraceResponse, error) {
	inspection, err := m.inspection(workspaceID, request.SessionID)
	if err != nil {
		return DebugStackTraceResponse{}, err
	}
	threadID := request.ThreadID
	if threadID <= 0 {
		threadID = inspection.session.threadID
	}
	if threadID <= 0 {
		return DebugStackTraceResponse{}, fmt.Errorf("debug thread id is required")
	}
	if request.StartFrame < 0 {
		return DebugStackTraceResponse{}, fmt.Errorf("stack start frame cannot be negative")
	}
	levels := request.Levels
	if levels <= 0 {
		levels = 50
	}
	if levels > 200 {
		levels = 200
	}
	ctx, cancel := context.WithTimeout(inspection.session.ctx, debugRequestTimeout)
	response, err := inspection.conn.request(ctx, "stackTrace", map[string]any{
		"threadId":   threadID,
		"startFrame": request.StartFrame,
		"levels":     levels,
	})
	cancel()
	if err != nil {
		return DebugStackTraceResponse{}, err
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
		TotalFrames int `json:"totalFrames"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return DebugStackTraceResponse{}, fmt.Errorf("decode debug stack trace: %w", err)
	}
	frames := make([]DebugStackFrame, 0, len(body.StackFrames))
	for _, frame := range body.StackFrames {
		frames = append(frames, DebugStackFrame{
			ID:       frame.ID,
			Name:     frame.Name,
			Location: debugDAPLocation(inspection.session.workspace, frame.Source.Name, frame.Source.Path, frame.Source.SourceReference, frame.Line, frame.Column),
		})
	}
	revision, err := m.inspectionRevision(inspection)
	if err != nil {
		return DebugStackTraceResponse{}, err
	}
	return DebugStackTraceResponse{
		WorkspaceID: workspaceID,
		SessionID:   request.SessionID,
		Revision:    revision,
		StackFrames: frames,
		TotalFrames: body.TotalFrames,
	}, nil
}

func (m *debugManager) loadScopes(workspaceID string, request DebugScopesRequest) (DebugScopesResponse, error) {
	inspection, err := m.inspection(workspaceID, request.SessionID)
	if err != nil {
		return DebugScopesResponse{}, err
	}
	frameID := request.FrameID
	if frameID <= 0 {
		frameID = inspection.session.frameID
	}
	if frameID <= 0 {
		return DebugScopesResponse{}, fmt.Errorf("debug frame id is required")
	}
	ctx, cancel := context.WithTimeout(inspection.session.ctx, debugRequestTimeout)
	response, err := inspection.conn.request(ctx, "scopes", map[string]any{"frameId": frameID})
	cancel()
	if err != nil {
		return DebugScopesResponse{}, err
	}
	var body struct {
		Scopes []struct {
			Name               string `json:"name"`
			PresentationHint   string `json:"presentationHint"`
			VariablesReference int    `json:"variablesReference"`
			NamedVariables     int    `json:"namedVariables"`
			IndexedVariables   int    `json:"indexedVariables"`
			Expensive          bool   `json:"expensive"`
			Line               int    `json:"line"`
			Column             int    `json:"column"`
			Source             struct {
				Name            string `json:"name"`
				Path            string `json:"path"`
				SourceReference int    `json:"sourceReference"`
			} `json:"source"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return DebugScopesResponse{}, fmt.Errorf("decode debug scopes: %w", err)
	}
	scopes := make([]DebugScope, 0, len(body.Scopes))
	for _, scope := range body.Scopes {
		item := DebugScope{
			Name:               scope.Name,
			PresentationHint:   scope.PresentationHint,
			VariablesReference: scope.VariablesReference,
			NamedVariables:     scope.NamedVariables,
			IndexedVariables:   scope.IndexedVariables,
			Expensive:          scope.Expensive,
		}
		if scope.Source.Path != "" || scope.Source.SourceReference != 0 {
			location := debugDAPLocation(inspection.session.workspace, scope.Source.Name, scope.Source.Path, scope.Source.SourceReference, scope.Line, scope.Column)
			item.Location = &location
		}
		scopes = append(scopes, item)
	}
	revision, err := m.inspectionRevision(inspection)
	if err != nil {
		return DebugScopesResponse{}, err
	}
	return DebugScopesResponse{WorkspaceID: workspaceID, SessionID: request.SessionID, Revision: revision, Scopes: scopes}, nil
}

func (m *debugManager) loadVariables(workspaceID string, request DebugVariablesRequest) (DebugVariablesResponse, error) {
	inspection, err := m.inspection(workspaceID, request.SessionID)
	if err != nil {
		return DebugVariablesResponse{}, err
	}
	if request.VariablesReference <= 0 {
		return DebugVariablesResponse{}, fmt.Errorf("variables reference is required")
	}
	if request.Start < 0 {
		return DebugVariablesResponse{}, fmt.Errorf("variables start cannot be negative")
	}
	count := request.Count
	if count <= 0 {
		count = 100
	}
	if count > 1000 {
		count = 1000
	}
	filter := strings.TrimSpace(request.Filter)
	if filter != "" && filter != "indexed" && filter != "named" {
		return DebugVariablesResponse{}, fmt.Errorf("variables filter must be indexed or named")
	}
	arguments := map[string]any{
		"variablesReference": request.VariablesReference,
		"start":              request.Start,
		"count":              count,
	}
	if filter != "" {
		arguments["filter"] = filter
	}
	ctx, cancel := context.WithTimeout(inspection.session.ctx, debugRequestTimeout)
	response, err := inspection.conn.request(ctx, "variables", arguments)
	cancel()
	if err != nil {
		return DebugVariablesResponse{}, err
	}
	var body struct {
		Variables []DebugVariable `json:"variables"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return DebugVariablesResponse{}, fmt.Errorf("decode debug variables: %w", err)
	}
	if body.Variables == nil {
		body.Variables = []DebugVariable{}
	}
	revision, err := m.inspectionRevision(inspection)
	if err != nil {
		return DebugVariablesResponse{}, err
	}
	return DebugVariablesResponse{WorkspaceID: workspaceID, SessionID: request.SessionID, Revision: revision, Variables: body.Variables}, nil
}

func (m *debugManager) evaluate(workspaceID string, request DebugEvaluateRequest) (DebugEvaluateResponse, error) {
	inspection, err := m.inspection(workspaceID, request.SessionID)
	if err != nil {
		return DebugEvaluateResponse{}, err
	}
	expression := strings.TrimSpace(request.Expression)
	if expression == "" {
		return DebugEvaluateResponse{}, fmt.Errorf("debug expression is required")
	}
	contextName := strings.TrimSpace(request.Context)
	if contextName == "" {
		contextName = "hover"
	}
	if contextName != "hover" && contextName != "watch" {
		return DebugEvaluateResponse{}, fmt.Errorf("debug evaluate context %q is not supported", contextName)
	}
	frameID := request.FrameID
	if frameID <= 0 {
		frameID = inspection.session.frameID
	}
	if frameID <= 0 {
		return DebugEvaluateResponse{}, fmt.Errorf("debug frame id is required")
	}
	ctx, cancel := context.WithTimeout(inspection.session.ctx, debugRequestTimeout)
	response, err := inspection.conn.request(ctx, "evaluate", map[string]any{
		"expression": expression,
		"frameId":    frameID,
		"context":    contextName,
	})
	cancel()
	if err != nil {
		return DebugEvaluateResponse{}, err
	}
	var body struct {
		Result             string `json:"result"`
		Type               string `json:"type"`
		VariablesReference int    `json:"variablesReference"`
		NamedVariables     int    `json:"namedVariables"`
		IndexedVariables   int    `json:"indexedVariables"`
		MemoryReference    string `json:"memoryReference"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return DebugEvaluateResponse{}, fmt.Errorf("decode debug evaluation: %w", err)
	}
	revision, err := m.inspectionRevision(inspection)
	if err != nil {
		return DebugEvaluateResponse{}, err
	}
	return DebugEvaluateResponse{
		WorkspaceID:        workspaceID,
		SessionID:          request.SessionID,
		Revision:           revision,
		Result:             body.Result,
		Type:               body.Type,
		VariablesReference: body.VariablesReference,
		NamedVariables:     body.NamedVariables,
		IndexedVariables:   body.IndexedVariables,
		MemoryReference:    body.MemoryReference,
	}, nil
}
