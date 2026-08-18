package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/workspacefs"
)

func (s *Server) handleGitRepositories(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	repositories, err := s.git.Repositories(r.Context(), workspaceID)
	if err != nil {
		writeGitError(w, err)
		return
	}
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil || !ok {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"repositories":                repositories,
		"searchParentGitRepositories": workspace.SearchParentGitRepositories,
	})
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.git.Status(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	document, err := s.git.Diff(
		r.Context(), r.PathValue("id"), r.PathValue("repositoryId"),
		r.URL.Query().Get("scope"), r.URL.Query().Get("path"),
		r.URL.Query().Get("oldPath"), r.URL.Query().Get("ref"),
	)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, document)
}

func (s *Server) handleGitMetadata(w http.ResponseWriter, r *http.Request) {
	metadata, err := s.git.Metadata(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, metadata)
}

func (s *Server) handleGitHistory(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	history, err := s.git.History(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"), offset, limit)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, history)
}

func (s *Server) handleGitCommitDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.git.CommitDetail(
		r.Context(), r.PathValue("id"), r.PathValue("repositoryId"),
		r.URL.Query().Get("ref"), r.URL.Query().Get("kind") == "stash",
	)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, detail)
}

func (s *Server) handleGitAction(w http.ResponseWriter, r *http.Request) {
	var request gitservice.ActionRequest
	if err := decodeLimitedJSON(w, r, &request, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.git.Action(r.Context(), r.PathValue("id"), r.PathValue("repositoryId"), request)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleGitClone(w http.ResponseWriter, r *http.Request) {
	var request gitservice.CloneRequest
	if err := decodeLimitedJSON(w, r, &request, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repositories, err := s.git.Clone(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"repositories": repositories})
}

func (s *Server) handleGitInitialize(w http.ResponseWriter, r *http.Request) {
	var request gitservice.InitRequest
	if err := decodeLimitedJSON(w, r, &request, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repositories, err := s.git.Initialize(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"repositories": repositories})
}

func (s *Server) handleGitSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SearchParentGitRepositories bool `json:"searchParentGitRepositories"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workspace, err := s.workspaces.SetSearchParentGitRepositories(r.PathValue("id"), body.SearchParentGitRepositories)
	if err != nil {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace not found", nil)
		return
	}
	repositories, err := s.git.Repositories(r.Context(), workspace.ID)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"workspace": workspace, "repositories": repositories})
}

func writeGitError(w http.ResponseWriter, err error) {
	var fsError *workspacefs.Error
	if errors.As(err, &fsError) {
		writeWorkspaceFSError(w, err)
		return
	}
	var gitError *gitservice.Error
	if !errors.As(err, &gitError) {
		writeCodedError(w, http.StatusInternalServerError, "git_error", "Git operation failed", nil)
		return
	}
	status := http.StatusBadRequest
	switch gitError.Code {
	case "workspace_not_found", "repository_not_found":
		status = http.StatusNotFound
	case "path_outside_workspace":
		status = http.StatusForbidden
	case "hidden_staged_changes", "clone_destination_exists":
		status = http.StatusConflict
	case "git_command_failed":
		status = http.StatusUnprocessableEntity
	case "git_authentication_failed":
		status = http.StatusUnauthorized
	case "git_unavailable":
		status = http.StatusServiceUnavailable
	case "git_timeout":
		status = http.StatusGatewayTimeout
	}
	writeCodedError(w, status, gitError.Code, gitError.Message, gitError.Details)
}
