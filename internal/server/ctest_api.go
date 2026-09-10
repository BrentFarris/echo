package server

import (
	"errors"
	"net/http"

	"github.com/brent/echo/internal/ctest"
	"github.com/brent/echo/internal/gotest"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

const maxCTestingRequestBytes = ctest.MaxSourceBytes*6 + 64<<10

func (s *Server) handleGetCTestingConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.cTests.Config(r.PathValue("id"))
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config})
}

func (s *Server) handlePutCTestingConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config gotestconfig.CConfig `json:"config"`
	}
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	config, err := s.cTests.SetConfig(r.PathValue("id"), body.Config)
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config})
}

func (s *Server) handleGetCTestingCoverage(w http.ResponseWriter, r *http.Request) {
	coverage, revision, err := s.cTests.Coverage(r.PathValue("id"))
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"coverage": coverage, "revision": revision})
}

func (s *Server) handleCTestingLenses(w http.ResponseWriter, r *http.Request) {
	var body ctest.LensRequest
	if err := decodeLimitedJSON(w, r, &body, maxCTestingRequestBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	lenses, err := s.cTests.Lenses(r.PathValue("id"), body)
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"lenses": lenses})
}

func (s *Server) handleStartCTestingRun(w http.ResponseWriter, r *http.Request) {
	var body ctest.RunRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.cTests.Run(r.PathValue("id"), body)
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"session": snapshot})
}

func (s *Server) handleRerunCTestingRun(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.cTests.Rerun(r.PathValue("id"), r.PathValue("sessionId"))
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"session": snapshot})
}

func (s *Server) handleRerunTestingRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, sessionID := r.PathValue("id"), r.PathValue("sessionId")
	snapshot, err := s.goTests.Rerun(workspaceID, sessionID)
	cRun := false
	if errors.Is(err, gotest.ErrRunNotFound) {
		cRun = true
		snapshot, err = s.cTests.Rerun(workspaceID, sessionID)
	}
	if err != nil {
		if cRun {
			writeCTestingError(w, err)
		} else {
			writeGoTestingError(w, err)
		}
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"session": snapshot})
}

func (s *Server) handleStartCTestingDebugSession(w http.ResponseWriter, r *http.Request) {
	var body ctest.RunRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.cTests.Debug(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeCTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"snapshot": snapshot})
}

func writeCTestingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ctest.ErrNotCSourceFile):
		writeCodedError(w, http.StatusUnprocessableEntity, "not_c_source_file", err.Error(), nil)
	case errors.Is(err, ctest.ErrTargetNotFound), errors.Is(err, ctest.ErrEntryNotFound):
		writeCodedError(w, http.StatusConflict, "c_test_target_invalid", err.Error(), nil)
	case errors.Is(err, ctest.ErrRunNotFound), errors.Is(err, terminal.ErrSessionNotFound), errors.Is(err, workspaces.ErrWorkspaceNotFound):
		writeCodedError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	default:
		var fsError *workspacefs.Error
		if errors.As(err, &fsError) {
			writeCodedError(w, http.StatusBadRequest, fsError.Code, fsError.Message, nil)
			return
		}
		writeCodedError(w, http.StatusBadRequest, "c_testing_error", err.Error(), nil)
	}
}
