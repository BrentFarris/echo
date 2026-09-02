package debugger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/brent/echo/internal/debugconfig"
)

func (s *Service) applyWorkspaceBreakpoints(workspaceID string) {
	state, err := s.state.Load(workspaceID)
	if err != nil {
		return
	}
	s.mu.Lock()
	runtime := s.runtimes[workspaceID]
	sessions := []*session{}
	if runtime != nil {
		for _, current := range runtime.sessions {
			if current.conn != nil && !isTerminalStatus(current.status) {
				sessions = append(sessions, current)
			}
		}
	}
	s.mu.Unlock()
	for _, current := range sessions {
		_ = s.sendBreakpoints(current, state)
	}
}

func (s *Service) sendAllBreakpoints(current *session) error {
	state, err := s.state.Load(current.workspaceID)
	if err != nil {
		return err
	}
	return s.sendBreakpoints(current, state)
}

func (s *Service) sendBreakpoints(current *session, state debugconfig.State) error {
	current.controlMu.Lock()
	defer current.controlMu.Unlock()
	// State updates can arrive from several browsers while an adapter is still
	// answering the previous setBreakpoints call. Reload after entering the
	// command lane so an older queued update can never overwrite a newer one.
	if latest, err := s.state.Load(current.workspaceID); err == nil {
		state = latest
	} else {
		return err
	}
	s.mu.Lock()
	if current.conn == nil {
		s.mu.Unlock()
		return nil
	}
	connection := current.conn
	capabilities := cloneMap(current.capabilities)
	supportsConditions := boolCapability(capabilities, "supportsConditionalBreakpoints")
	supportsHitConditions := boolCapability(capabilities, "supportsHitConditionalBreakpoints")
	supportsLogPoints := boolCapability(capabilities, "supportsLogPoints")
	previous := cloneMap(current.breakpointSources)
	current.breakpointStatuses = map[string]BreakpointStatus{}
	current.adapterBreakpointIDs = map[int]string{}
	s.mu.Unlock()
	byPath := map[string][]map[string]any{}
	byPathIDs := map[string][]string{}
	for _, breakpoint := range state.SourceBreakpoints {
		if !breakpoint.Enabled {
			continue
		}
		hostPath, err := s.resolveSource(current.workspaceID, breakpoint.Source)
		if err != nil {
			continue
		}
		adapterPath := s.hostPathToAdapter(current.workspaceID, hostPath)
		entry := map[string]any{"line": breakpoint.Line}
		if breakpoint.Column > 0 {
			entry["column"] = breakpoint.Column
		}
		if breakpoint.Condition != "" && supportsConditions {
			entry["condition"] = breakpoint.Condition
		}
		if breakpoint.HitCondition != "" && supportsHitConditions {
			entry["hitCondition"] = breakpoint.HitCondition
		}
		if breakpoint.LogMessage != "" && supportsLogPoints {
			entry["logMessage"] = breakpoint.LogMessage
		}
		byPath[adapterPath] = append(byPath[adapterPath], entry)
		byPathIDs[adapterPath] = append(byPathIDs[adapterPath], breakpoint.ID)
		previous[adapterPath] = true
	}
	for path := range previous {
		breakpoints := byPath[path]
		ctx, cancel := context.WithTimeout(current.ctx, requestTimeout)
		response, err := connection.request(ctx, "setBreakpoints", map[string]any{"source": map[string]any{"path": path}, "breakpoints": breakpoints, "sourceModified": false})
		cancel()
		if err != nil {
			return fmt.Errorf("set breakpoints for %s: %w", path, err)
		}
		s.recordBreakpointResponse(current, "source", byPathIDs[path], response.Body)
		s.publishRaw(current.workspaceID, current.id, "breakpoints", s.translateDAPBody(current.workspaceID, response.Body))
	}
	if boolCapability(capabilities, "supportsFunctionBreakpoints") {
		entries := []map[string]any{}
		ids := []string{}
		for _, breakpoint := range state.FunctionBreakpoints {
			if !breakpoint.Enabled {
				continue
			}
			entry := map[string]any{"name": breakpoint.Name}
			if breakpoint.Condition != "" && supportsConditions {
				entry["condition"] = breakpoint.Condition
			}
			if breakpoint.HitCondition != "" && supportsHitConditions {
				entry["hitCondition"] = breakpoint.HitCondition
			}
			entries = append(entries, entry)
			ids = append(ids, breakpoint.ID)
		}
		if err := s.breakpointRequest(current, connection, "setFunctionBreakpoints", "function", ids, map[string]any{"breakpoints": entries}); err != nil {
			return err
		}
	}
	if boolCapability(capabilities, "supportsInstructionBreakpoints") {
		entries := []map[string]any{}
		ids := []string{}
		for _, breakpoint := range state.InstructionBreakpoints {
			if !breakpoint.Enabled {
				continue
			}
			entry := map[string]any{"instructionReference": breakpoint.InstructionReference}
			if breakpoint.Offset != 0 {
				entry["offset"] = breakpoint.Offset
			}
			if breakpoint.Condition != "" && supportsConditions {
				entry["condition"] = breakpoint.Condition
			}
			if breakpoint.HitCondition != "" && supportsHitConditions {
				entry["hitCondition"] = breakpoint.HitCondition
			}
			entries = append(entries, entry)
			ids = append(ids, breakpoint.ID)
		}
		if err := s.breakpointRequest(current, connection, "setInstructionBreakpoints", "instruction", ids, map[string]any{"breakpoints": entries}); err != nil {
			return err
		}
	}
	if boolCapability(capabilities, "supportsDataBreakpoints") {
		entries := []map[string]any{}
		ids := []string{}
		for _, breakpoint := range state.DataBreakpoints {
			if !breakpoint.Enabled || (breakpoint.AdapterProfileID != "" && breakpoint.AdapterProfileID != current.profile.ID) {
				continue
			}
			entry := map[string]any{"dataId": breakpoint.DataID}
			if breakpoint.AccessType != "" {
				entry["accessType"] = breakpoint.AccessType
			}
			if breakpoint.Condition != "" && supportsConditions {
				entry["condition"] = breakpoint.Condition
			}
			if breakpoint.HitCondition != "" && supportsHitConditions {
				entry["hitCondition"] = breakpoint.HitCondition
			}
			entries = append(entries, entry)
			ids = append(ids, breakpoint.ID)
		}
		if err := s.breakpointRequest(current, connection, "setDataBreakpoints", "data", ids, map[string]any{"breakpoints": entries}); err != nil {
			return err
		}
	}
	filters := []string{}
	filterOptions := []map[string]any{}
	for _, breakpoint := range state.ExceptionBreakpoints {
		if !breakpoint.Enabled {
			continue
		}
		filters = append(filters, breakpoint.Filter)
		if breakpoint.Condition != "" && boolCapability(capabilities, "supportsExceptionFilterOptions") {
			filterOptions = append(filterOptions, map[string]any{"filterId": breakpoint.Filter, "condition": breakpoint.Condition})
		}
	}
	arguments := map[string]any{"filters": filters}
	if len(filterOptions) > 0 {
		arguments["filterOptions"] = filterOptions
	}
	if err := s.breakpointRequest(current, connection, "setExceptionBreakpoints", "exception", nil, arguments); err != nil {
		return err
	}
	s.mu.Lock()
	if active, err := s.sessionLocked(current.workspaceID, current.id); err == nil && active == current {
		current.breakpointSources = map[string]bool{}
		for path := range byPath {
			current.breakpointSources[path] = true
		}
	}
	s.mu.Unlock()
	s.publishSession(current.workspaceID, current.id, "breakpoints_changed", nil, "")
	return nil
}

func (s *Service) breakpointRequest(current *session, connection *dapConnection, command, kind string, ids []string, arguments map[string]any) error {
	ctx, cancel := context.WithTimeout(current.ctx, requestTimeout)
	response, err := connection.request(ctx, command, arguments)
	cancel()
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	s.recordBreakpointResponse(current, kind, ids, response.Body)
	var body map[string]any
	if json.Unmarshal(response.Body, &body) == nil {
		s.publishRaw(current.workspaceID, current.id, "breakpoints", s.translateDAPBody(current.workspaceID, response.Body))
	}
	return nil
}

func (s *Service) recordBreakpointResponse(current *session, kind string, ids []string, body json.RawMessage) {
	if len(ids) == 0 {
		return
	}
	var response struct {
		Breakpoints []struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Message  string `json:"message"`
			Source   *struct {
				Name            string `json:"name"`
				Path            string `json:"path"`
				SourceReference int    `json:"sourceReference"`
			} `json:"source"`
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return
	}
	statuses := make([]BreakpointStatus, 0, len(ids))
	for index, stateID := range ids {
		status := BreakpointStatus{StateID: stateID, Kind: kind}
		if index < len(response.Breakpoints) {
			item := response.Breakpoints[index]
			status.AdapterID, status.Verified, status.Message, status.Line, status.Column = item.ID, item.Verified, item.Message, item.Line, item.Column
			if item.Source != nil {
				host := s.adapterPathToHost(current.workspaceID, item.Source.Path)
				status.Source = &SourceLocation{Name: item.Source.Name, Path: host, Ref: s.fileRefForPath(current.workspaceID, host), SourceReference: item.Source.SourceReference, Line: item.Line, Column: item.Column}
			}
		} else {
			status.Message = "The adapter did not return a breakpoint result"
		}
		statuses = append(statuses, status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.sessionLocked(current.workspaceID, current.id)
	if err != nil || active != current {
		return
	}
	for _, status := range statuses {
		current.breakpointStatuses[status.StateID] = status
		if status.AdapterID != 0 {
			current.adapterBreakpointIDs[status.AdapterID] = status.StateID
		}
	}
}

func (s *Service) updateBreakpointEvent(workspaceID, sessionID string, body json.RawMessage) {
	var event struct {
		Breakpoint struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Message  string `json:"message"`
			Source   *struct {
				Name            string `json:"name"`
				Path            string `json:"path"`
				SourceReference int    `json:"sourceReference"`
			} `json:"source"`
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"breakpoint"`
	}
	if json.Unmarshal(body, &event) != nil || event.Breakpoint.ID == 0 {
		return
	}
	var source *SourceLocation
	if item := event.Breakpoint.Source; item != nil {
		host := s.adapterPathToHost(workspaceID, item.Path)
		source = &SourceLocation{Name: item.Name, Path: host, Ref: s.fileRefForPath(workspaceID, host), SourceReference: item.SourceReference, Line: event.Breakpoint.Line, Column: event.Breakpoint.Column}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	if err != nil {
		return
	}
	stateID := current.adapterBreakpointIDs[event.Breakpoint.ID]
	if stateID == "" {
		return
	}
	status := current.breakpointStatuses[stateID]
	status.AdapterID, status.Verified, status.Message = event.Breakpoint.ID, event.Breakpoint.Verified, event.Breakpoint.Message
	status.Line, status.Column, status.Source = event.Breakpoint.Line, event.Breakpoint.Column, source
	current.breakpointStatuses[stateID] = status
	current.revision++
}
