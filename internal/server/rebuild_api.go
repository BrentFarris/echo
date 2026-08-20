package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/echoupdate"
	"github.com/brent/echo/internal/rebuild"
	"github.com/brent/echo/internal/workspaces"
)

type rebuildCoordinator interface {
	BuildAndPrepare(context.Context, rebuild.Request) (rebuild.Result, error)
	UpdateAndPrepare(context.Context, rebuild.Request) (rebuild.Result, error)
}

type echoUpdateChecker interface {
	Check(context.Context, string) (echoupdate.Status, error)
}

func (s *Server) handleEchoUpdateStatus(w http.ResponseWriter, r *http.Request) {
	sourcePath, ok := s.echoSourceForDevelopment(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	status, err := s.updateChecker.Check(ctx, sourcePath)
	if err != nil {
		writeCodedError(w, http.StatusServiceUnavailable, "update_check_failed", err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) handleEchoUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleDevelopmentRelaunch(w, r, true)
}

func (s *Server) handleRebuildRelaunch(w http.ResponseWriter, r *http.Request) {
	s.handleDevelopmentRelaunch(w, r, false)
}

func (s *Server) handleDevelopmentRelaunch(w http.ResponseWriter, r *http.Request, update bool) {
	sourcePath, ok := s.echoSourceForDevelopment(w)
	if !ok {
		return
	}
	request := rebuild.Request{
		SourceDir:  sourcePath,
		DataDir:    filepath.Dir(s.settingsPath),
		ProcessID:  s.processID,
		Arguments:  append([]string(nil), s.processArgs...),
		WorkingDir: s.workingDir,
	}
	var result rebuild.Result
	var err error
	if update {
		result, err = s.rebuilder.UpdateAndPrepare(r.Context(), request)
	} else {
		result, err = s.rebuilder.BuildAndPrepare(r.Context(), request)
	}
	if err != nil {
		s.writeDevelopmentError(w, err, update)
		return
	}

	writeData(w, http.StatusAccepted, map[string]any{
		"status":     "restarting",
		"instanceId": s.instanceID,
		"sourcePath": result.SourcePath,
		"logPath":    result.LogPath,
	})
	// The host performs the existing graceful shutdown. The detached launcher
	// is already waiting for this exact process before replacing it.
	s.requestRestart()
}

func (s *Server) echoSourceForDevelopment(w http.ResponseWriter) (string, bool) {
	sourcePath, err := s.findEchoSource()
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "workspace_lookup_failed", "failed to inspect registered workspaces", nil)
		return "", false
	}
	if sourcePath == "" {
		writeCodedError(w, http.StatusBadRequest, "echo_source_not_found", "Add the Echo source directory to a registered workspace first.", nil)
		return "", false
	}
	return sourcePath, true
}

func (s *Server) writeDevelopmentError(w http.ResponseWriter, err error, update bool) {
	if errors.Is(err, rebuild.ErrInProgress) {
		writeCodedError(w, http.StatusConflict, "development_operation_in_progress", err.Error(), nil)
		return
	}
	if errors.Is(err, rebuild.ErrMasterNotCheckedOut) {
		writeCodedError(w, http.StatusConflict, "update_requires_master", err.Error(), buildErrorDetails(err))
		return
	}
	var buildErr *rebuild.BuildError
	if errors.As(err, &buildErr) {
		code := "rebuild_failed"
		if update && (buildErr.Stage == "git pull" || buildErr.Stage == "update branch check") {
			code = "update_failed"
		}
		writeCodedError(w, http.StatusInternalServerError, code, buildErr.Error(), buildErrorDetails(err))
		return
	}
	code := "rebuild_failed"
	if update {
		code = "update_failed"
	}
	writeCodedError(w, http.StatusInternalServerError, code, err.Error(), nil)
}

func buildErrorDetails(err error) map[string]any {
	var buildErr *rebuild.BuildError
	if !errors.As(err, &buildErr) {
		return nil
	}
	return map[string]any{"stage": buildErr.Stage, "logPath": buildErr.LogPath}
}

func (s *Server) findEchoSource() (string, error) {
	registered, err := s.workspaces.List()
	if err != nil {
		return "", err
	}
	activeID, err := s.workspaces.ActiveID()
	if err != nil {
		return "", err
	}

	ordered := make([]workspaces.Workspace, 0, len(registered))
	for _, workspace := range registered {
		if workspace.ID == activeID {
			ordered = append(ordered, workspace)
			break
		}
	}
	for _, workspace := range registered {
		if workspace.ID != activeID {
			ordered = append(ordered, workspace)
		}
	}

	seen := make(map[string]bool)
	for _, workspace := range ordered {
		folders := append([]string{workspace.MainPath}, workspace.Folders...)
		for _, folder := range folders {
			folder = strings.TrimSpace(folder)
			if folder == "" {
				continue
			}
			folder = filepath.Clean(folder)
			identity := strings.ToLower(folder)
			if seen[identity] {
				continue
			}
			seen[identity] = true
			if rebuild.IsEchoSource(folder) {
				return folder, nil
			}
		}
	}
	return "", nil
}
