package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"

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
	activeID, err := s.workspaces.ActiveID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load active workspace: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"workspaces": list,
		"activeId":   activeID,
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
	if err := s.lsp.Activate(body.ID); err != nil {
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
	before, _ := s.workspaces.List()
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
	for _, existing := range before {
		if existing.ID == ws.ID {
			s.refreshWorkspaceCaches(r.Context(), ws.ID)
			break
		}
	}
	writeData(w, http.StatusCreated, map[string]any{"workspace": ws})
}

// handleUpdateWorkspace patches selected workspace properties (name and/or folders).
func (s *Server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "workspace ID is required")
		return
	}
	var body struct {
		Name    string   `json:"name"`
		Folders []string `json:"folders,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	ws, err := s.workspaces.Update(id, body.Name, body.Folders)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.refreshWorkspaceCaches(r.Context(), id)
	writeData(w, http.StatusOK, map[string]any{"workspace": ws})
}

// handleDeleteWorkspace removes a workspace from the app data store. It does
// not delete the .echo directory or any workspace files on disk.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "workspace ID is required")
		return
	}
	if err := s.workspaces.Delete(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.refreshWorkspaceCaches(r.Context(), id)
	writeData(w, http.StatusOK, map[string]any{"deleted": id})
=======
	before, ok, err := s.workspaces.Get(id)
	if err != nil {
		writeWorkspaceMutationError(w, err)
		return
	}
	if !ok {
		writeWorkspaceMutationError(w, workspaces.ErrWorkspaceNotFound)
		return
	}
	updated, err := s.workspaces.Update(id, body)
	if err != nil {
		writeWorkspaceMutationError(w, err)
		return
	}
	if !slices.Equal(before.Folders, updated.Folders) {
		if err := s.sandbox.StopRetainingData(r.Context(), id); err != nil {
			logf("stop sandbox after workspace root update %s: %v", id, err)
		}
		s.refreshWorkspaceCaches(r.Context(), id)
	}
	writeData(w, http.StatusOK, map[string]any{"workspace": updated})
}

// handleDeleteWorkspace unregisters a workspace while retaining its project
// folder, .echo directory, and persistent sandbox data.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sandbox.StopRetainingData(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stop workspace sandbox: "+err.Error())
		return
	}
	activeID, err := s.workspaces.Unregister(id)
	if err != nil {
		writeWorkspaceMutationError(w, err)
		return
	}
	s.removeWorkspaceCaches(id)
	if err := s.debugState.Delete(id); err != nil {
		logf("delete debug state for workspace %s: %v", id, err)
	}
	if activeID != "" {
		if err := s.lsp.Activate(activeID); err != nil {
			logf("activate replacement workspace %s: %v", activeID, err)
		}
	}
	writeData(w, http.StatusOK, map[string]any{
		"deletedId": id, "activeId": activeID, "workspaceFilesRetained": true,
	})
}

func writeWorkspaceMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, workspaces.ErrWorkspaceNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
>>>>>>> origin/master
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
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}
