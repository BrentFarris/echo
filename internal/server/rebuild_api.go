package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/rebuild"
	"github.com/brent/echo/internal/workspaces"
)

type rebuildCoordinator interface {
	BuildAndPrepare(context.Context, rebuild.Request) (rebuild.Result, error)
}

func (s *Server) handleRebuildRelaunch(w http.ResponseWriter, r *http.Request) {
	sourcePath, err := s.findEchoSource()
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "workspace_lookup_failed", "failed to inspect registered workspaces", nil)
		return
	}
	if sourcePath == "" {
		writeCodedError(w, http.StatusBadRequest, "echo_source_not_found", "Add the Echo source directory to a registered workspace first.", nil)
		return
	}

	result, err := s.rebuilder.BuildAndPrepare(r.Context(), rebuild.Request{
		SourceDir:  sourcePath,
		DataDir:    filepath.Dir(s.settingsPath),
		ProcessID:  s.processID,
		Arguments:  append([]string(nil), s.processArgs...),
		WorkingDir: s.workingDir,
	})
	if err != nil {
		if errors.Is(err, rebuild.ErrInProgress) {
			writeCodedError(w, http.StatusConflict, "rebuild_in_progress", err.Error(), nil)
			return
		}
		var buildErr *rebuild.BuildError
		if errors.As(err, &buildErr) {
			writeCodedError(w, http.StatusInternalServerError, "rebuild_failed", buildErr.Error(), map[string]any{
				"stage":   buildErr.Stage,
				"logPath": buildErr.LogPath,
			})
			return
		}
		writeCodedError(w, http.StatusInternalServerError, "rebuild_failed", err.Error(), nil)
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
