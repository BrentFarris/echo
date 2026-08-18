package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/brent/echo/internal/workspaces"
)

// handleGetWorkspaces returns the list of registered workspaces and the id of
// the currently active workspace (if any).
func (s *Server) handleGetWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.workspaces.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspaces: "+err.Error())
		return
	}
	active, _, err := s.workspaces.Active()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load active workspace: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"workspaces": list,
		"activeId":   active.ID,
	})
}

// handleSetActiveWorkspace records the given workspace id as the active (last
// opened) workspace.
func (s *Server) handleSetActiveWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := s.workspaces.SetActive(body.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"activeId": body.ID})
}

// handleCreateWorkspace validates and registers a new workspace.
func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string           `json:"name"`
		MainPath string           `json:"mainPath"`
		Folders  []string         `json:"folders"`
		Icon     *workspaces.Icon `json:"icon,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{
		Name:     body.Name,
		MainPath: body.MainPath,
		Folders:  body.Folders,
		Icon:     body.Icon,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"workspace": ws})
}

// handleGetWorkspaceIcon serves a workspace's icon image from its .echo folder.
func (s *Server) handleGetWorkspaceIcon(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, err := s.workspaces.IconPath(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if path == "" {
		writeError(w, http.StatusNotFound, "workspace has no icon")
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "workspace icon not found")
		return
	}
	http.ServeFile(w, r, path)
}
