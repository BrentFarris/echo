package server

import (
	"encoding/json"
	"net/http"

	"github.com/brent/echo/internal/llm"
)

// handleGetSettings returns the current settings loaded from the store.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load settings: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"settings":    cfg,
		"storagePath": s.store.Path(),
	})
}

// handlePutSettings validates and persists the settings from the request body.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings llm.Settings `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := body.Settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.Save(body.Settings); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save settings: "+err.Error())
		return
	}
	// Reload the LLM client so chat uses the newly configured endpoint.
	s.initLLM()
	writeData(w, http.StatusOK, map[string]any{
		"settings":    body.Settings.NormalizedEndpointProfiles(),
		"storagePath": s.store.Path(),
	})
}
