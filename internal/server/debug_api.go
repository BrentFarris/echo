package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/workspaces"
)

const maxDebugConfigBytes = 2 << 20

func (s *Server) handleGetDebugAdapterProfiles(w http.ResponseWriter, _ *http.Request) {
	profiles, err := s.debugger.Profiles()
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"profiles": profiles, "templates": s.debugger.Templates()})
}

func (s *Server) handleCreateDebugAdapterProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID string                      `json:"templateId,omitempty"`
		Profile    *debugconfig.AdapterProfile `json:"profile,omitempty"`
	}
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		profile debugconfig.AdapterProfile
		err     error
	)
	if strings.TrimSpace(body.TemplateID) != "" && body.Profile != nil {
		writeError(w, http.StatusBadRequest, "provide templateId or profile, not both")
		return
	}
	if strings.TrimSpace(body.TemplateID) != "" {
		profile, err = s.debugger.AddTemplate(body.TemplateID)
	} else if body.Profile != nil {
		profile, err = s.debugger.AddProfile(*body.Profile)
	} else {
		writeError(w, http.StatusBadRequest, "templateId or profile is required")
		return
	}
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"profile": profile})
}

func (s *Server) handleUpdateDebugAdapterProfile(w http.ResponseWriter, r *http.Request) {
	var profile debugconfig.AdapterProfile
	if err := decodeLimitedJSON(w, r, &profile, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.debugger.UpdateProfile(r.PathValue("profileId"), profile)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"profile": updated})
}

func (s *Server) handleDeleteDebugAdapterProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.debugger.DeleteProfile(r.PathValue("profileId")); err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleGetWorkspaceDebugConfig(w http.ResponseWriter, r *http.Request) {
	config, profiles, err := s.debugger.WorkspaceConfig(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config, "profiles": profiles, "templates": s.debugger.Templates()})
}

func (s *Server) handlePutWorkspaceDebugConfig(w http.ResponseWriter, r *http.Request) {
	var config debugconfig.WorkspaceConfig
	if err := decodeLimitedJSON(w, r, &config, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, profiles, err := s.debugger.SetWorkspaceConfig(r.PathValue("id"), config)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": updated, "profiles": profiles})
}

func (s *Server) handleGetWorkspaceDebugState(w http.ResponseWriter, r *http.Request) {
	state, err := s.debugger.State(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) handlePutWorkspaceDebugState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision uint64            `json:"expectedRevision"`
		State            debugconfig.State `json:"state"`
	}
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := s.debugger.SetState(r.PathValue("id"), body.ExpectedRevision, body.State)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) handleDebugAdapterDiagnostic(w http.ResponseWriter, r *http.Request) {
	diagnostic, err := s.debugger.Diagnostic(r.Context(), r.PathValue("id"), r.PathValue("profileId"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"diagnostic": diagnostic})
}

func (s *Server) handleDebugSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.debugger.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleDebugProcesses(w http.ResponseWriter, r *http.Request) {
	processes, err := s.debugger.Processes(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"processes": processes})
}

func (s *Server) handleStartDebugSession(w http.ResponseWriter, r *http.Request) {
	var body debugger.StartRequest
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.debugger.Start(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"snapshot": snapshot})
}

var allowedDAPRequests = map[string]bool{
	"threads": true, "stackTrace": true, "scopes": true, "variables": true,
	"evaluate": true, "completions": true, "setVariable": true, "setExpression": true,
	"modules": true, "loadedSources": true, "source": true, "readMemory": true,
	"writeMemory": true, "disassemble": true, "continue": true, "pause": true,
	"next": true, "stepIn": true, "stepOut": true, "stepBack": true,
	"reverseContinue": true, "goto": true, "gotoTargets": true, "restartFrame": true,
	"terminateThreads": true, "exceptionInfo": true, "dataBreakpointInfo": true,
	"inlineValues":       true,
	"setDataBreakpoints": true, "setInstructionBreakpoints": true, "setFunctionBreakpoints": true,
	"setExceptionBreakpoints": true, "setBreakpoints": true, "breakpointLocations": true,
	"cancel": true,
}

func (s *Server) handleDebugRequest(w http.ResponseWriter, r *http.Request) {
	command := r.PathValue("command")
	if !allowedDAPRequests[command] {
		writeCodedError(w, http.StatusBadRequest, "debug_request_not_allowed", "this DAP request is managed by Echo or is not supported", map[string]any{"command": command})
		return
	}
	var body debugger.ControlRequest
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := s.debugger.Request(r.Context(), r.PathValue("id"), r.PathValue("sessionId"), command, body)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"response": response})
}

func (s *Server) handleSetDebugTrace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
		Enabled          bool   `json:"enabled"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, err := s.debugger.SetTrace(r.PathValue("id"), r.PathValue("sessionId"), body.ExpectedRevision, body.Enabled)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"session": session})
}

func (s *Server) handleStopDebugSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision  uint64 `json:"expectedRevision"`
		TerminateDebuggee *bool  `json:"terminateDebuggee,omitempty"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.debugger.StopChecked(r.PathValue("id"), r.PathValue("sessionId"), body.ExpectedRevision, body.TerminateDebuggee); err != nil {
		writeDebugError(w, err)
		return
	}
	snapshot, err := s.debugger.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleDisconnectDebugSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.debugger.DisconnectChecked(r.PathValue("id"), r.PathValue("sessionId"), body.ExpectedRevision); err != nil {
		writeDebugError(w, err)
		return
	}
	snapshot, err := s.debugger.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleTerminateDebugSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.debugger.TerminateChecked(r.PathValue("id"), r.PathValue("sessionId"), body.ExpectedRevision); err != nil {
		writeDebugError(w, err)
		return
	}
	snapshot, err := s.debugger.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleRestartDebugSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.debugger.RestartChecked(r.Context(), r.PathValue("id"), r.PathValue("sessionId"), body.ExpectedRevision)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleStopDebugGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevisions map[string]uint64 `json:"expectedRevisions"`
	}
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.debugger.StopGroupChecked(r.PathValue("id"), r.PathValue("groupId"), body.ExpectedRevisions); err != nil {
		writeDebugError(w, err)
		return
	}
	snapshot, err := s.debugger.Snapshot(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func (s *Server) handleRestartDebugGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedRevisions map[string]uint64 `json:"expectedRevisions"`
	}
	if err := decodeLimitedJSON(w, r, &body, maxDebugConfigBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.debugger.RestartGroup(r.Context(), r.PathValue("id"), r.PathValue("groupId"), body.ExpectedRevisions)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"snapshot": snapshot})
}

func (s *Server) handlePreviewVSCodeDebugImport(w http.ResponseWriter, r *http.Request) {
	workspace, ok, err := s.workspaces.Get(r.PathValue("id"))
	if err != nil {
		writeDebugError(w, err)
		return
	}
	if !ok {
		writeDebugError(w, debugger.ErrWorkspaceNotFound)
		return
	}
	launchPath := filepath.Join(workspace.MainPath, ".vscode", "launch.json")
	launch, err := readDebugImportFile(launchPath, true)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	tasksPath := filepath.Join(workspace.MainPath, ".vscode", "tasks.json")
	tasks, tasksErr := readDebugImportFile(tasksPath, false)
	profiles, err := s.debugger.Profiles()
	if err != nil {
		writeDebugError(w, err)
		return
	}
	preview, err := debugconfig.PreviewVSCodeImport(launch, tasks, profiles)
	if err != nil {
		writeDebugError(w, err)
		return
	}
	if tasksErr != nil && !errors.Is(tasksErr, os.ErrNotExist) {
		preview.Warnings = append(preview.Warnings, debugconfig.ImportWarning{Code: "tasks_read_failed", Message: tasksErr.Error()})
	}
	writeData(w, http.StatusOK, map[string]any{
		"preview": preview,
		"sources": map[string]any{"launch": ".vscode/launch.json", "tasks": ".vscode/tasks.json", "tasksFound": tasksErr == nil},
	})
}

func readDebugImportFile(path string, required bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && required {
			return nil, fmt.Errorf(".vscode/launch.json was not found: %w", err)
		}
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxDebugConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxDebugConfigBytes {
		return nil, fmt.Errorf("%s exceeds the %d byte import limit", filepath.Base(path), maxDebugConfigBytes)
	}
	return content, nil
}

func writeDebugError(w http.ResponseWriter, err error) {
	var stateConflict *debugconfig.RevisionConflict
	if errors.As(err, &stateConflict) {
		writeCodedError(w, http.StatusConflict, "debug_state_revision_conflict", err.Error(), map[string]any{"expectedRevision": stateConflict.Expected, "actualRevision": stateConflict.Actual})
		return
	}
	var sessionConflict *debugger.RevisionError
	if errors.As(err, &sessionConflict) {
		code := "debug_session_revision_conflict"
		if sessionConflict.Stop {
			code = "debug_stop_generation_conflict"
		}
		writeCodedError(w, http.StatusConflict, code, err.Error(), map[string]any{"expected": sessionConflict.Expected, "actual": sessionConflict.Actual})
		return
	}
	switch {
	case errors.Is(err, debugger.ErrWorkspaceNotFound), errors.Is(err, workspaces.ErrWorkspaceNotFound):
		writeCodedError(w, http.StatusNotFound, "debug_workspace_not_found", err.Error(), nil)
	case errors.Is(err, debugger.ErrSessionNotFound):
		writeCodedError(w, http.StatusNotFound, "debug_session_not_found", err.Error(), nil)
	case errors.Is(err, debugger.ErrUnsupported):
		writeCodedError(w, http.StatusUnprocessableEntity, "debug_capability_unsupported", err.Error(), nil)
	case errors.Is(err, debugconfig.ErrProfileInUse):
		writeCodedError(w, http.StatusConflict, "debug_profile_in_use", err.Error(), nil)
	case errors.Is(err, os.ErrNotExist):
		writeCodedError(w, http.StatusNotFound, "debug_source_not_found", err.Error(), nil)
	default:
		writeCodedError(w, http.StatusBadRequest, "debug_request_failed", err.Error(), nil)
	}
}
