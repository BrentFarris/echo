package server

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/brent/echo/internal/plugins"
)

func (s *Server) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	catalog, err := s.plugins.Catalog(strings.TrimSpace(r.URL.Query().Get("workspaceId")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, catalog)
}

func (s *Server) handlePluginStage(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body struct {
		Source plugins.Source `json:"source"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var stage plugins.StageRecord
	var err error
	switch body.Source.Type {
	case "local":
		stage, err = s.plugins.StageLocal(r.Context(), body.Source.Path)
	case "github":
		stage, err = s.plugins.StageGitHub(r.Context(), body.Source)
	case "builtin":
		stage, err = s.plugins.StageBuiltin(r.Context(), body.Source.Builtin)
	default:
		err = errors.New("plugin source type must be local, github, or builtin")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"stage": stage})
}

func (s *Server) handlePluginApprove(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body plugins.ApprovalRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plugin, err := s.plugins.Approve(r.Context(), r.PathValue("stageId"), body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"plugin": plugin})
}

func (s *Server) handlePluginReject(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	if err := s.plugins.RejectStage(r.PathValue("stageId")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"rejected": true})
}

func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body plugins.ActionRequest
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var stage plugins.StageRecord
	var err error
	switch body.Action {
	case "reload":
		stage, err = s.plugins.StageReload(r.Context(), r.PathValue("id"))
	case "update-check":
		stage, err = s.plugins.StageUpdate(r.Context(), r.PathValue("id"))
	default:
		err = s.plugins.Action(r.Context(), r.PathValue("id"), body)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data := map[string]any{"changed": true}
	if stage.ID != "" {
		data["stage"] = stage
	}
	writeData(w, http.StatusOK, data)
}

func (s *Server) handlePluginConfig(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body plugins.ConfigUpdate
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.plugins.UpdateConfig(r.Context(), r.PathValue("id"), body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"updated": true})
}

func (s *Server) handlePluginLog(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	data, err := s.plugins.Log(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"log": string(data)})
}

func (s *Server) handlePluginIcon(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		http.NotFound(w, r)
		return
	}
	data, contentType, digest, err := s.plugins.Icon(r.PathValue("id"), r.PathValue("viewId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+digest+`"`)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	_, _ = w.Write(data)
}

func (s *Server) handlePluginUISession(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body struct {
		WorkspaceID string `json:"workspaceId,omitempty"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.WorkspaceID != "" {
		if _, ok, err := s.workspaces.Get(body.WorkspaceID); err != nil || !ok {
			writeError(w, http.StatusBadRequest, "workspace was not found")
			return
		}
	}
	session, err := s.plugins.CreateUISession(r.PathValue("id"), r.PathValue("viewId"), body.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"session": session})
}

func (s *Server) handlePluginUIBridge(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	var body plugins.UIBridgeRequest
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.plugins.InvokeUIBridge(r.Context(), r.PathValue("token"), body)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handlePluginUIClose(w http.ResponseWriter, r *http.Request) {
	if s.plugins != nil {
		s.plugins.CloseUISession(r.PathValue("token"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePluginUIAsset(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		http.NotFound(w, r)
		return
	}
	asset, err := s.plugins.UIAsset(r.PathValue("token"), r.PathValue("path"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("serve plugin asset: %v", err)
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=1800")
	w.Header().Set("ETag", `"`+asset.Digest+`"`)
	_, _ = w.Write(asset.Data)
}

func (s *Server) handlePluginRemoveData(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	if err := s.plugins.RemoveData(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"removed": true})
}
