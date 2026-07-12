package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

func (s *SystemService) SetDebugBreakpoints(workspaceID string, request DebugSetBreakpointsRequest) (DebugState, error) {
	workspace, _, err := s.workspaceAndSettings(strings.TrimSpace(workspaceID))
	if err != nil {
		return DebugState{}, err
	}
	if s.debugger == nil {
		return DebugState{}, fmt.Errorf("debug service is unavailable")
	}
	return s.debugger.setBreakpoints(workspace, request)
}

func (m *debugManager) setBreakpoints(workspace Workspace, request DebugSetBreakpointsRequest) (DebugState, error) {
	if strings.TrimSpace(request.SourcePath) == "" {
		return DebugState{}, fmt.Errorf("breakpoint source path is required")
	}
	firstRoot, err := workspaceFolderAbsolutePath(workspace.Folders[0])
	if err != nil {
		return DebugState{}, err
	}
	absolute, err := resolveDebugWorkspacePath(workspace, request.SourcePath, firstRoot)
	if err != nil {
		return DebugState{}, fmt.Errorf("breakpoint source: %w", err)
	}
	breakpoints, err := normalizeDebugSourceBreakpoints(request.Breakpoints)
	if err != nil {
		return DebugState{}, err
	}
	storedPath := workspaceRelativePath(workspace, absolute)

	m.mu.Lock()
	if active := m.session; active != nil && active.workspace.ID == workspace.ID && active.status != DebugStatusTerminated && active.status != DebugStatusError {
		if strings.TrimSpace(request.SessionID) == "" || request.SessionID != active.id {
			m.mu.Unlock()
			return DebugState{}, fmt.Errorf("debug session is stale")
		}
	}
	next := cloneDebugPersistentState(m.stored)
	if next.Workspaces[workspace.ID] == nil {
		next.Workspaces[workspace.ID] = make(map[string][]DebugSourceBreakpoint)
	}
	if len(breakpoints) == 0 {
		delete(next.Workspaces[workspace.ID], storedPath)
	} else {
		next.Workspaces[workspace.ID][storedPath] = append([]DebugSourceBreakpoint(nil), breakpoints...)
	}
	if len(next.Workspaces[workspace.ID]) == 0 {
		delete(next.Workspaces, workspace.ID)
	}
	if err := writeDebugPersistentState(debugPersistentStatePath(m.service.storePath), next); err != nil {
		m.mu.Unlock()
		return DebugState{}, err
	}
	m.stored = next
	session := m.session
	if session != nil && session.workspace.ID == workspace.ID {
		session.breakpoints[absolute] = pendingDebugBreakpoints(storedPath, breakpoints)
		if len(breakpoints) == 0 {
			delete(session.breakpoints, absolute)
		}
	}
	m.revision++
	var state DebugState
	if session != nil && session.workspace.ID == workspace.ID {
		state = m.snapshotLocked(session)
	} else {
		state = DebugState{WorkspaceID: workspace.ID, Revision: m.revision, Status: DebugStatusIdle, Breakpoints: m.storedBreakpointsLocked(workspace)}
	}
	connReady := session != nil && session.workspace.ID == workspace.ID && session.conn != nil && session.status != DebugStatusStarting && session.status != DebugStatusStopping && session.status != DebugStatusTerminated && session.status != DebugStatusError
	m.mu.Unlock()

	if connReady {
		if err := m.sendBreakpoints(session, absolute, breakpoints); err != nil {
			return state, err
		}
		m.mu.Lock()
		state = m.snapshotLocked(session)
		m.mu.Unlock()
	} else {
		m.emit(DebugEvent{Type: "breakpoints", State: &state})
	}
	return state, nil
}

func (m *debugManager) sendAllBreakpoints(session *debugSession) error {
	m.mu.Lock()
	sources := m.stored.Workspaces[session.workspace.ID]
	copySources := make(map[string][]DebugSourceBreakpoint, len(sources))
	for path, breakpoints := range sources {
		copySources[path] = append([]DebugSourceBreakpoint(nil), breakpoints...)
	}
	m.mu.Unlock()
	paths := make([]string, 0, len(copySources))
	for path := range copySources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, storedPath := range paths {
		firstRoot, err := workspaceFolderAbsolutePath(session.workspace.Folders[0])
		if err != nil {
			return err
		}
		absolute, err := resolveDebugWorkspacePath(session.workspace, storedPath, firstRoot)
		if err != nil {
			m.appendOutput(session, "console", fmt.Sprintf("Skipped breakpoint source %s: %v\n", storedPath, err))
			continue
		}
		if err := m.sendBreakpoints(session, absolute, copySources[storedPath]); err != nil {
			return fmt.Errorf("set breakpoints for %s: %w", storedPath, err)
		}
	}
	return nil
}

func (m *debugManager) sendBreakpoints(session *debugSession, absolute string, requested []DebugSourceBreakpoint) error {
	m.mu.Lock()
	if m.session != session || session.conn == nil {
		m.mu.Unlock()
		return fmt.Errorf("debug session is unavailable")
	}
	conn := session.conn
	m.mu.Unlock()
	arguments := map[string]any{
		"source":      map[string]any{"name": filepath.Base(absolute), "path": absolute},
		"breakpoints": requested,
	}
	ctx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
	response, err := conn.request(ctx, "setBreakpoints", arguments)
	cancel()
	if err != nil {
		return err
	}
	var body struct {
		Breakpoints []struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return fmt.Errorf("decode breakpoint response: %w", err)
	}
	path := workspaceRelativePath(session.workspace, absolute)
	results := make([]DebugBreakpoint, 0, len(requested))
	for index, source := range requested {
		result := DebugBreakpoint{Path: path, Line: source.Line, Column: source.Column}
		if index < len(body.Breakpoints) {
			adapter := body.Breakpoints[index]
			result.ID = adapter.ID
			result.Verified = adapter.Verified
			result.Message = adapter.Message
			if adapter.Line > 0 {
				result.Line = adapter.Line
			}
			if adapter.Column > 0 {
				result.Column = adapter.Column
			}
		}
		results = append(results, result)
	}
	m.mu.Lock()
	if m.session != session {
		m.mu.Unlock()
		return fmt.Errorf("debug session was replaced")
	}
	if len(results) == 0 {
		delete(session.breakpoints, absolute)
	} else {
		session.breakpoints[absolute] = results
	}
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "breakpoints", State: &state})
	return nil
}

func (m *debugManager) handleDAPBreakpointEvent(session *debugSession, data json.RawMessage) {
	var body struct {
		Reason     string `json:"reason"`
		Breakpoint struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		} `json:"breakpoint"`
	}
	if json.Unmarshal(data, &body) != nil {
		return
	}
	m.mu.Lock()
	if m.session != session {
		m.mu.Unlock()
		return
	}
	changed := false
	for path, breakpoints := range session.breakpoints {
		for i := range breakpoints {
			if body.Breakpoint.ID == 0 || breakpoints[i].ID != body.Breakpoint.ID {
				continue
			}
			breakpoints[i].Verified = body.Breakpoint.Verified
			breakpoints[i].Message = body.Breakpoint.Message
			if body.Breakpoint.Line > 0 {
				breakpoints[i].Line = body.Breakpoint.Line
			}
			if body.Breakpoint.Column > 0 {
				breakpoints[i].Column = body.Breakpoint.Column
			}
			session.breakpoints[path] = breakpoints
			changed = true
		}
	}
	if !changed {
		m.mu.Unlock()
		return
	}
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "breakpoints", State: &state, Message: body.Reason})
}

func normalizeDebugSourceBreakpoints(input []DebugSourceBreakpoint) ([]DebugSourceBreakpoint, error) {
	seen := make(map[[2]int]struct{}, len(input))
	output := make([]DebugSourceBreakpoint, 0, len(input))
	for _, breakpoint := range input {
		if breakpoint.Line <= 0 {
			return nil, fmt.Errorf("breakpoint line must be positive")
		}
		if breakpoint.Column < 0 {
			return nil, fmt.Errorf("breakpoint column cannot be negative")
		}
		key := [2]int{breakpoint.Line, breakpoint.Column}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, breakpoint)
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Line != output[j].Line {
			return output[i].Line < output[j].Line
		}
		return output[i].Column < output[j].Column
	})
	return output, nil
}

func pendingDebugBreakpoints(path string, source []DebugSourceBreakpoint) []DebugBreakpoint {
	output := make([]DebugBreakpoint, 0, len(source))
	for _, breakpoint := range source {
		output = append(output, DebugBreakpoint{Path: path, Line: breakpoint.Line, Column: breakpoint.Column})
	}
	return output
}

func (m *debugManager) storedBreakpointsLocked(workspace Workspace) []DebugBreakpoint {
	var output []DebugBreakpoint
	for path, breakpoints := range m.stored.Workspaces[workspace.ID] {
		output = append(output, pendingDebugBreakpoints(path, breakpoints)...)
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Path != output[j].Path {
			return output[i].Path < output[j].Path
		}
		if output[i].Line != output[j].Line {
			return output[i].Line < output[j].Line
		}
		return output[i].Column < output[j].Column
	})
	return output
}

func flattenDebugBreakpoints(input map[string][]DebugBreakpoint) []DebugBreakpoint {
	var output []DebugBreakpoint
	for _, breakpoints := range input {
		output = append(output, breakpoints...)
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Path != output[j].Path {
			return output[i].Path < output[j].Path
		}
		return output[i].Line < output[j].Line
	})
	return output
}

func (m *debugManager) dropWorkspace(workspaceID string) error {
	m.mu.Lock()
	next := cloneDebugPersistentState(m.stored)
	delete(next.Workspaces, workspaceID)
	if err := writeDebugPersistentState(debugPersistentStatePath(m.service.storePath), next); err != nil {
		m.mu.Unlock()
		return err
	}
	m.stored = next
	session := m.session
	m.mu.Unlock()
	if session != nil && session.workspace.ID == workspaceID {
		_, _ = m.stop(workspaceID, session.id)
	}
	return nil
}
