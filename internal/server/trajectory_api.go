package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	trajectorylog "github.com/brent/echo/internal/trajectory"
)

func (s *Server) mainTrajectory(workspaceID, chatID string) (*trajectorylog.Store, string, string, error) {
	parent, err := s.sessions.get(workspaceID)
	if err != nil {
		return nil, "", "", err
	}
	session, resolved, err := parent.resolveSurfaceTab(chatID, chatSurfaceMain)
	if err != nil {
		return nil, resolved, "", err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.trajectory == nil {
		if session.trajectoryWarning != "" {
			return nil, resolved, "", fmt.Errorf("trajectory is unavailable: %s", session.trajectoryWarning)
		}
		return nil, resolved, "", fmt.Errorf("trajectory is unavailable")
	}
	return session.trajectory, resolved, session.trajectoryWarning, nil
}

func (s *Server) handleGetTrajectory(w http.ResponseWriter, r *http.Request) {
	store, _, warning, err := s.mainTrajectory(r.PathValue("id"), r.PathValue("chatId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	before, err := trajectoryUintQuery(r, "beforeSeq")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	turnLimit, err := trajectoryIntQuery(r, "turnLimit", 20)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := store.Page(before, turnLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	page.Warning = warning
	writeData(w, http.StatusOK, page)
}

func (s *Server) handleSearchTrajectory(w http.ResponseWriter, r *http.Request) {
	store, _, warning, err := s.mainTrajectory(r.PathValue("id"), r.PathValue("chatId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	before, err := trajectoryUintQuery(r, "beforeSeq")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := trajectoryIntQuery(r, "limit", 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := store.Search(r.URL.Query().Get("q"), before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.Warning = warning
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleExportTrajectory(w http.ResponseWriter, r *http.Request) {
	store, resolved, _, err := s.mainTrajectory(r.PathValue("id"), r.PathValue("chatId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	file, err := store.Open()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-trajectory.jsonl"`, resolved))
	http.ServeContent(w, r, resolved+"-trajectory.jsonl", info.ModTime(), file)
}

func trajectoryUintQuery(r *http.Request, name string) (uint64, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	return parsed, nil
}

func trajectoryIntQuery(r *http.Request, name string, fallback int) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}
