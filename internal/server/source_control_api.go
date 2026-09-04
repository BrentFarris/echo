package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
)

func (s *Server) handleSourceControlProviders(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	if _, ok, err := s.workspaces.Get(workspaceID); err != nil || !ok {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"providers": s.sourceControl.Providers(r.Context(), workspaceID)})
}

func (s *Server) handleSourceControlRepositories(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	repositories, err := s.sourceControl.Repositories(r.Context(), workspaceID)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil || !ok {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"repositories": repositories, "providers": s.sourceControl.Providers(r.Context(), workspaceID),
		"searchParentRepositories": workspace.SearchParentRepositories || workspace.SearchParentGitRepositories,
	})
}

func (s *Server) handleSourceControlStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.sourceControl.Status(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"))
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) handleSourceControlDiff(w http.ResponseWriter, r *http.Request) {
	target := sourcecontrol.DiffTarget{
		Kind: r.URL.Query().Get("kind"), GroupID: r.URL.Query().Get("groupId"),
		Path: r.URL.Query().Get("path"), OldPath: r.URL.Query().Get("oldPath"),
		BaseRef: r.URL.Query().Get("baseRef"), Ref: r.URL.Query().Get("ref"),
	}
	if target.Kind == "" {
		target.Kind = "change"
	}
	document, err := s.sourceControl.Diff(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"), target)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, document)
}

func (s *Server) handleSourceControlMetadata(w http.ResponseWriter, r *http.Request) {
	metadata, err := s.sourceControl.Metadata(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"))
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, metadata)
}

func (s *Server) handleSourceControlHistory(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	history, err := s.sourceControl.History(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"), offset, limit)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, history)
}

func (s *Server) handleSourceControlRevisionDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.sourceControl.RevisionDetail(
		r.Context(), r.PathValue("id"), r.PathValue("repositoryId"),
		r.URL.Query().Get("ref"), r.URL.Query().Get("kind"),
	)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, detail)
}

func (s *Server) handleSourceControlAction(w http.ResponseWriter, r *http.Request) {
	var request sourcecontrol.ActionRequest
	if err := decodeLimitedJSON(w, r, &request, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.sourceControl.Action(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"), request)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleSourceControlSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		workspace, ok, err := s.workspaces.Get(r.PathValue("id"))
		if err != nil || !ok {
			writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
			return
		}
		writeData(w, http.StatusOK, map[string]any{
			"searchParentRepositories": workspace.SearchParentRepositories || workspace.SearchParentGitRepositories,
		})
		return
	}
	var body struct {
		SearchParentRepositories bool `json:"searchParentRepositories"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workspace, err := s.workspaces.SetSearchParentRepositories(r.PathValue("id"), body.SearchParentRepositories)
	if err != nil {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
		return
	}
	repositories, err := s.sourceControl.Repositories(r.Context(), workspace.ID)
	if err != nil {
		writeSourceControlError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"workspace": workspace, "repositories": repositories})
}

func writeSourceControlError(w http.ResponseWriter, err error) {
	var fsError *workspacefs.Error
	if errors.As(err, &fsError) {
		writeWorkspaceFSError(w, err)
		return
	}
	var sourceError *sourcecontrol.Error
	if !errors.As(err, &sourceError) {
		writeCodedError(w, http.StatusInternalServerError, "source_control_error", "Source control operation failed", nil)
		return
	}
	status := http.StatusBadRequest
	switch sourceError.Code {
	case "workspace_not_found", "repository_not_found":
		status = http.StatusNotFound
	case "path_outside_workspace":
		status = http.StatusForbidden
	case "hidden_changes", "hidden_staged_changes", "stale_source_control_revision", "clone_destination_exists":
		status = http.StatusConflict
	case "git_authentication_failed", "fossil_authentication_failed":
		status = http.StatusUnauthorized
	case "git_unavailable", "fossil_unavailable", "fossil_checkout_unavailable_in_sandbox":
		status = http.StatusServiceUnavailable
	case "git_timeout", "fossil_timeout":
		status = http.StatusGatewayTimeout
	case "git_command_failed", "fossil_command_failed", "invalid_fossil_checkout":
		status = http.StatusUnprocessableEntity
	}
	writeCodedError(w, status, sourceError.Code, sourceError.Message, sourceError.Details)
}
