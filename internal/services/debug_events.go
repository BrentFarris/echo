package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func (m *debugManager) handleDAPEvent(session *debugSession, event dapEnvelope) {
	switch event.Event {
	case "initialized":
		session.initializedOnce.Do(func() { close(session.initialized) })
	case "stopped":
		var body struct {
			Reason      string `json:"reason"`
			Description string `json:"description"`
			ThreadID    int    `json:"threadId"`
			Text        string `json:"text"`
		}
		if err := json.Unmarshal(event.Body, &body); err != nil {
			m.failSession(session, fmt.Errorf("decode debugger stopped event: %w", err))
			return
		}
		m.mu.Lock()
		if m.session != session || session.status == DebugStatusStopping || session.status == DebugStatusTerminated || session.status == DebugStatusError {
			m.mu.Unlock()
			return
		}
		session.status = DebugStatusPaused
		session.threadID = body.ThreadID
		session.frameID = 0
		session.location = nil
		session.pauseGeneration++
		generation := session.pauseGeneration
		m.revision++
		state := m.snapshotLocked(session)
		m.mu.Unlock()
		message := firstDebugMessage(body.Description, body.Text, body.Reason)
		m.emit(DebugEvent{Type: "stopped", State: &state, Message: message})
		m.focusEchoWindow()
		go m.hydrateStoppedSession(session, generation)
	case "continued":
		m.mu.Lock()
		if m.session != session || session.status == DebugStatusStopping || session.status == DebugStatusTerminated || session.status == DebugStatusError {
			m.mu.Unlock()
			return
		}
		session.status = DebugStatusRunning
		session.frameID = 0
		session.location = nil
		session.pauseGeneration++
		m.revision++
		state := m.snapshotLocked(session)
		m.mu.Unlock()
		m.emit(DebugEvent{Type: "continued", State: &state})
	case "output":
		var body struct {
			Category string `json:"category"`
			Output   string `json:"output"`
		}
		if json.Unmarshal(event.Body, &body) == nil {
			m.appendOutput(session, body.Category, body.Output)
		}
	case "breakpoint":
		m.handleDAPBreakpointEvent(session, event.Body)
	case "terminated", "exited":
		m.finish(session, DebugStatusTerminated, "")
		session.cancel()
		go func() {
			if session.conn != nil {
				_ = session.conn.Close()
			}
			if session.adapter != nil {
				session.adapter.stop()
			}
		}()
	case "invalidated":
		m.mu.Lock()
		if m.session == session && session.status == DebugStatusPaused {
			session.frameID = 0
			session.location = nil
			session.pauseGeneration++
			m.revision++
			state := m.snapshotLocked(session)
			m.mu.Unlock()
			m.emit(DebugEvent{Type: "invalidated", State: &state})
			return
		}
		m.mu.Unlock()
	}
}

func (m *debugManager) hydrateStoppedSession(session *debugSession, generation uint64) {
	m.mu.Lock()
	if m.session != session || session.status != DebugStatusPaused || session.pauseGeneration != generation {
		m.mu.Unlock()
		return
	}
	threadID := session.threadID
	conn := session.conn
	m.mu.Unlock()
	if conn == nil {
		return
	}
	if threadID == 0 {
		ctx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
		response, err := conn.request(ctx, "threads", map[string]any{})
		cancel()
		if err != nil {
			return
		}
		var body struct {
			Threads []DebugThread `json:"threads"`
		}
		if json.Unmarshal(response.Body, &body) != nil || len(body.Threads) == 0 {
			return
		}
		threadID = body.Threads[0].ID
	}

	ctx, cancel := context.WithTimeout(session.ctx, debugRequestTimeout)
	response, err := conn.request(ctx, "stackTrace", map[string]any{
		"threadId":   threadID,
		"startFrame": 0,
		"levels":     1,
	})
	cancel()
	if err != nil {
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
	location := debugDAPLocation(session.workspace, frame.Source.Name, frame.Source.Path, frame.Source.SourceReference, frame.Line, frame.Column)

	m.mu.Lock()
	if m.session != session || session.status != DebugStatusPaused || session.pauseGeneration != generation {
		m.mu.Unlock()
		return
	}
	session.threadID = threadID
	session.frameID = frame.ID
	session.location = &location
	m.revision++
	state := m.snapshotLocked(session)
	m.mu.Unlock()
	m.emit(DebugEvent{Type: "location", State: &state})
}

func debugDAPLocation(workspace Workspace, name string, path string, sourceReference int, line int, column int) DebugSourceLocation {
	location := DebugSourceLocation{
		Name:      name,
		Line:      line,
		Column:    column,
		SourceRef: sourceReference,
	}
	if strings.TrimSpace(path) == "" {
		location.External = true
		return location
	}
	clean := filepath.Clean(path)
	if debugPathWithinWorkspace(workspace, clean) {
		location.Path = workspaceRelativePath(workspace, clean)
		return location
	}
	location.Path = filepath.ToSlash(clean)
	location.External = true
	return location
}

func firstDebugMessage(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
