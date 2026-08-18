package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
)

type agentModeRequest struct {
	WorkspaceID string          `json:"workspaceId"`
	Mode        agentmodes.Mode `json:"mode"`
}

type agentModeTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (s *Server) handleGetAgentModes(w http.ResponseWriter, r *http.Request) {
	workspace, err := s.agentModeWorkspace(r.URL.Query().Get("workspaceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modes, err := s.modes.List(workspace.MainPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	registered := tools.Registered()
	available := make([]agentModeTool, 0, len(registered))
	for _, tool := range registered {
		metadata := tool.Metadata()
		available = append(available, agentModeTool{Name: metadata.Name, Description: metadata.Description})
	}
	writeData(w, http.StatusOK, map[string]any{
		"workspaceId": workspace.ID,
		"modes":       modes,
		"tools":       available,
	})
}

func (s *Server) handleCreateAgentMode(w http.ResponseWriter, r *http.Request) {
	var body agentModeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	workspace, err := s.agentModeWorkspace(body.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modes, err := s.modes.Create(workspace.MainPath, body.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"workspaceId": workspace.ID, "modes": modes})
}

func (s *Server) handleUpdateAgentMode(w http.ResponseWriter, r *http.Request) {
	var body agentModeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	workspace, err := s.agentModeWorkspace(body.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modes, err := s.modes.Update(workspace.MainPath, r.PathValue("id"), body.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"workspaceId": workspace.ID, "modes": modes})
}

func (s *Server) handleDeleteAgentMode(w http.ResponseWriter, r *http.Request) {
	workspace, err := s.agentModeWorkspace(r.URL.Query().Get("workspaceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modes, err := s.modes.Delete(workspace.MainPath, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"workspaceId": workspace.ID, "modes": modes})
}

func (s *Server) agentModeWorkspace(id string) (workspaces.Workspace, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		workspace, ok, err := s.workspaces.Active()
		if err != nil {
			return workspaces.Workspace{}, err
		}
		if !ok {
			return workspaces.Workspace{}, &agentModeWorkspaceError{"select a workspace before managing agent modes"}
		}
		return workspace, nil
	}
	workspace, ok, err := s.workspaces.Get(id)
	if err != nil {
		return workspaces.Workspace{}, err
	}
	if !ok {
		return workspaces.Workspace{}, &agentModeWorkspaceError{"workspace not found"}
	}
	return workspace, nil
}

type agentModeWorkspaceError struct{ message string }

func (e *agentModeWorkspaceError) Error() string { return e.message }
