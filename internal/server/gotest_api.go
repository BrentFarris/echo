package server

import (
	"errors"
	"net/http"

	"github.com/brent/echo/internal/gotest"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

const maxGoTestingRequestBytes = gotest.MaxSourceBytes*6 + 64<<10

func (s *Server) handleGetGoTestingConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.goTests.Config(r.PathValue("id"))
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config})
}

func (s *Server) handlePutGoTestingConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config gotestconfig.GoConfig `json:"config"`
	}
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	config, err := s.goTests.SetConfig(r.PathValue("id"), body.Config)
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config})
}

func (s *Server) handleGetGoTestingCoverage(w http.ResponseWriter, r *http.Request) {
	coverage, revision, err := s.goTests.Coverage(r.PathValue("id"))
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"coverage": coverage, "revision": revision})
}

func (s *Server) handleGoTestingLenses(w http.ResponseWriter, r *http.Request) {
	var body gotest.LensRequest
	if err := decodeLimitedJSON(w, r, &body, maxGoTestingRequestBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	lenses, err := s.goTests.Lenses(r.PathValue("id"), body)
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"lenses": lenses})
}

func (s *Server) handleStartGoTestingRun(w http.ResponseWriter, r *http.Request) {
	var body gotest.RunRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.goTests.Run(r.PathValue("id"), body)
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"session": snapshot})
}

func (s *Server) handleRerunGoTestingRun(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.goTests.Rerun(r.PathValue("id"), r.PathValue("sessionId"))
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"session": snapshot})
}

func (s *Server) handleStartGoTestingDebugSession(w http.ResponseWriter, r *http.Request) {
	var body gotest.RunRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.goTests.Debug(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeGoTestingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"snapshot": snapshot})
}

func writeGoTestingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gotest.ErrNotGoTestFile):
		writeCodedError(w, http.StatusUnprocessableEntity, "not_go_test_file", err.Error(), nil)
	case errors.Is(err, gotest.ErrTargetNotFound):
		writeCodedError(w, http.StatusConflict, "go_test_target_not_found", err.Error(), nil)
	case errors.Is(err, gotest.ErrRunNotFound), errors.Is(err, terminal.ErrSessionNotFound), errors.Is(err, workspaces.ErrWorkspaceNotFound):
		writeCodedError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	default:
		var fsError *workspacefs.Error
		if errors.As(err, &fsError) {
			writeCodedError(w, http.StatusBadRequest, fsError.Code, fsError.Message, nil)
			return
		}
		writeCodedError(w, http.StatusBadRequest, "go_testing_error", err.Error(), nil)
	}
}
