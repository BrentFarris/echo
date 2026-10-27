package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/workspacegroups"
)

// workspaceGroupView is the API shape of a workspace group. Workspace names are
// resolved so the frontend can render the membership without extra lookups.
type workspaceGroupView struct {
	ID           string                        `json:"id"`
	Name         string                        `json:"name"`
	WorkspaceIDs []string                      `json:"workspaceIds"`
	Workspaces   []workspaceGroupWorkspaceRef  `json:"workspaces"`
	PendingTasks []workspacegroups.PendingTask `json:"pendingTasks,omitempty"`
	CreatedAt    string                        `json:"createdAt"`
	UpdatedAt    string                        `json:"updatedAt"`
}

type workspaceGroupWorkspaceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) groupViews() ([]workspaceGroupView, error) {
	groups, err := s.workspaceGroups.List()
	if err != nil {
		return nil, err
	}
	views := make([]workspaceGroupView, 0, len(groups))
	for _, group := range groups {
		views = append(views, s.groupView(group))
	}
	return views, nil
}

func (s *Server) groupView(group workspacegroups.WorkspaceGroup) workspaceGroupView {
	refs := make([]workspaceGroupWorkspaceRef, 0, len(group.WorkspaceIDs))
	for _, id := range group.WorkspaceIDs {
		name := id
		if workspace, ok, err := s.workspaces.Get(id); err == nil && ok {
			name = workspace.Name
		}
		refs = append(refs, workspaceGroupWorkspaceRef{ID: id, Name: name})
	}
	return workspaceGroupView{
		ID:           group.ID,
		Name:         group.Name,
		WorkspaceIDs: group.WorkspaceIDs,
		Workspaces:   refs,
		PendingTasks: group.PendingTasks,
		CreatedAt:    group.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    group.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// broadcastWorkspaceGroupsChanged notifies every connected client that the
// workspace groups (or their pending tasks) changed.
func (s *Server) broadcastWorkspaceGroupsChanged() {
	s.hub.Broadcast(map[string]any{"type": "workspace_groups_changed"})
}

func (s *Server) handleGetWorkspaceGroups(w http.ResponseWriter, r *http.Request) {
	views, err := s.groupViews()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workspace groups: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"groups": views})
}

func (s *Server) handleCreateWorkspaceGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string   `json:"name"`
		WorkspaceIDs []string `json:"workspaceIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	group, err := s.workspaceGroups.Create(body.Name, body.WorkspaceIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	s.orchestrator.kick()
	writeData(w, http.StatusCreated, map[string]any{"group": s.groupView(group)})
}

func (s *Server) handleUpdateWorkspaceGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name         *string   `json:"name"`
		WorkspaceIDs *[]string `json:"workspaceIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	var name *string
	if body.Name != nil {
		trimmed := strings.TrimSpace(*body.Name)
		name = &trimmed
	}
	var workspaceIDs []string
	if body.WorkspaceIDs != nil {
		workspaceIDs = *body.WorkspaceIDs
	}
	group, err := s.workspaceGroups.Update(id, name, workspaceIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	s.orchestrator.kick()
	writeData(w, http.StatusOK, map[string]any{"group": s.groupView(group)})
}

func (s *Server) handleDeleteWorkspaceGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.workspaceGroups.Delete(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	writeData(w, http.StatusOK, map[string]any{"deletedId": id})
}

func (s *Server) handleCreateGroupTask(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	var body struct {
		Prompt          string   `json:"prompt"`
		Model           string   `json:"model"`
		ReasoningEffort string   `json:"reasoningEffort"`
		Temperature     *float64 `json:"temperature"`
		Ready           *bool    `json:"ready"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	ready := true
	if body.Ready != nil {
		ready = *body.Ready
	}
	group, err := s.workspaceGroups.AddTask(groupID, workspacegroups.PendingTask{
		Prompt:          body.Prompt,
		Model:           body.Model,
		ReasoningEffort: body.ReasoningEffort,
		Temperature:     body.Temperature,
		Ready:           ready,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	s.orchestrator.kick()
	writeData(w, http.StatusCreated, map[string]any{"group": s.groupView(group)})
}

func (s *Server) handleUpdateGroupTask(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	taskID := r.PathValue("taskId")
	var body struct {
		Prompt          *string   `json:"prompt"`
		Model           *string   `json:"model"`
		ReasoningEffort *string   `json:"reasoningEffort"`
		Temperature     **float64 `json:"temperature"`
		Ready           *bool     `json:"ready"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	group, err := s.workspaceGroups.UpdateTask(groupID, taskID, func(task *workspacegroups.PendingTask) {
		if body.Prompt != nil {
			task.Prompt = strings.TrimSpace(*body.Prompt)
		}
		if body.Model != nil {
			task.Model = *body.Model
		}
		if body.ReasoningEffort != nil {
			task.ReasoningEffort = *body.ReasoningEffort
		}
		if body.Temperature != nil {
			task.Temperature = *body.Temperature
		}
		if body.Ready != nil {
			task.Ready = *body.Ready
		}
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	s.orchestrator.kick()
	writeData(w, http.StatusOK, map[string]any{"group": s.groupView(group)})
}

func (s *Server) handleDeleteGroupTask(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	taskID := r.PathValue("taskId")
	group, err := s.workspaceGroups.DeleteTask(groupID, taskID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.broadcastWorkspaceGroupsChanged()
	writeData(w, http.StatusOK, map[string]any{"group": s.groupView(group)})
}
