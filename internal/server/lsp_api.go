package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	lspruntime "github.com/brent/echo/internal/lsp"
	"github.com/brent/echo/internal/lspconfig"
	"github.com/gorilla/websocket"
)

func (s *Server) handleGetLSPProfiles(w http.ResponseWriter, _ *http.Request) {
	profiles, err := s.lsp.Profiles()
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "lsp_profiles_load_failed", err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"profiles": profiles, "templates": lspconfig.Templates()})
}

func (s *Server) handleCreateLSPProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID string            `json:"templateId"`
		Profile    lspconfig.Profile `json:"profile"`
	}
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		return
	}
	var profile lspconfig.Profile
	var err error
	if body.TemplateID != "" {
		profile, err = s.lsp.AddTemplate(body.TemplateID)
	} else {
		profile, err = s.lsp.AddProfile(body.Profile)
	}
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_lsp_profile", err.Error(), nil)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"profile": profile})
}

func (s *Server) handleUpdateLSPProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile lspconfig.Profile `json:"profile"`
	}
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		return
	}
	profile, err := s.lsp.UpdateProfile(r.PathValue("profileId"), body.Profile)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_lsp_profile", err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"profile": profile})
}

func (s *Server) handleDeleteLSPProfile(w http.ResponseWriter, r *http.Request) {
	err := s.lsp.DeleteProfile(r.PathValue("profileId"))
	if err != nil {
		status := http.StatusBadRequest
		code := "lsp_profile_delete_failed"
		if errors.Is(err, lspruntime.ErrProfileInUse) {
			status = http.StatusConflict
			code = "lsp_profile_in_use"
		}
		writeCodedError(w, status, code, err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleGetWorkspaceLSPConfig(w http.ResponseWriter, r *http.Request) {
	config, profiles, statuses, err := s.lsp.WorkspaceConfig(r.PathValue("id"))
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "lsp_config_load_failed", err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config, "profiles": profiles, "statuses": statuses})
}

func (s *Server) handlePutWorkspaceLSPConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config lspconfig.WorkspaceConfig `json:"config"`
	}
	if err := decodeLimitedJSON(w, r, &body, 1<<20); err != nil {
		return
	}
	config, profiles, statuses, err := s.lsp.SetWorkspaceConfig(r.PathValue("id"), body.Config)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_lsp_config", err.Error(), nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": config, "profiles": profiles, "statuses": statuses})
}

func (s *Server) handleRestartLSP(w http.ResponseWriter, r *http.Request) {
	if err := s.lsp.Restart(r.PathValue("id"), r.PathValue("profileId")); err != nil {
		writeCodedError(w, http.StatusBadRequest, "lsp_restart_failed", err.Error(), nil)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"restarting": true})
}

type lspInboundMessage struct {
	Type           string               `json:"type"`
	ID             string               `json:"id,omitempty"`
	ProfileID      string               `json:"profileId,omitempty"`
	Method         string               `json:"method,omitempty"`
	Params         json.RawMessage      `json:"params,omitempty"`
	Document       lspruntime.Document  `json:"document,omitempty"`
	TakeOver       bool                 `json:"takeOver,omitempty"`
	URI            string               `json:"uri,omitempty"`
	Version        int                  `json:"version,omitempty"`
	ContentChanges json.RawMessage      `json:"contentChanges,omitempty"`
	Text           string               `json:"text,omitempty"`
	Result         json.RawMessage      `json:"result,omitempty"`
	Error          *lspruntime.RPCError `json:"error,omitempty"`
}

func (s *Server) handleLSPWebSocket(w http.ResponseWriter, r *http.Request) {
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("LSP websocket upgrade: %v", err)
		return
	}
	defer connection.Close()
	outbound := make(chan []byte, 256)
	done := make(chan struct{})
	var doneOnce sync.Once
	closeDone := func() { doneOnce.Do(func() { close(done) }) }
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			return
		}
		select {
		case outbound <- data:
		case <-done:
		}
	}
	client, err := s.lsp.Attach(r.PathValue("id"), send)
	if err != nil {
		closeDone()
		return
	}
	defer client.Close()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer closeDone()
		for {
			select {
			case data := <-outbound:
				_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := connection.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			case <-ticker.C:
				_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := connection.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	connection.SetReadLimit(maxMessageBytes)
	_ = connection.SetReadDeadline(time.Now().Add(90 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	requestMu := sync.Mutex{}
	requests := map[string]context.CancelFunc{}
	defer func() {
		requestMu.Lock()
		for _, cancel := range requests {
			cancel()
		}
		requestMu.Unlock()
		closeDone()
	}()
	for {
		_, data, err := connection.ReadMessage()
		if err != nil {
			return
		}
		var message lspInboundMessage
		if err := json.Unmarshal(data, &message); err != nil {
			sendLSPError(send, "", "", err)
			continue
		}
		switch message.Type {
		case "lsp_claim":
			if err := s.lsp.ClaimDocument(client, message.ProfileID, message.Document, message.TakeOver); err != nil {
				sendLSPError(send, message.ID, message.ProfileID, err)
			}
		case "lsp_change":
			if err := s.lsp.ChangeDocument(client, message.ProfileID, message.URI, message.Version, message.ContentChanges); err != nil {
				sendLSPError(send, message.ID, message.ProfileID, err)
			}
		case "lsp_save":
			if err := s.lsp.SaveDocument(client, message.ProfileID, message.URI, message.Text); err != nil {
				sendLSPError(send, message.ID, message.ProfileID, err)
			}
		case "lsp_close":
			if err := s.lsp.CloseDocument(client, message.ProfileID, message.URI); err != nil {
				sendLSPError(send, message.ID, message.ProfileID, err)
			}
		case "lsp_request":
			ctx, cancel := context.WithCancel(context.Background())
			requestMu.Lock()
			requests[message.ID] = cancel
			requestMu.Unlock()
			go func(message lspInboundMessage) {
				defer func() {
					requestMu.Lock()
					delete(requests, message.ID)
					requestMu.Unlock()
					cancel()
				}()
				result, err := s.lsp.Call(ctx, client, message.ID, message.ProfileID, message.Method, message.Params)
				if err != nil {
					sendLSPError(send, message.ID, message.ProfileID, err)
					return
				}
				send(map[string]any{"type": "lsp_response", "id": message.ID, "profileId": message.ProfileID, "result": result})
			}(message)
		case "lsp_cancel":
			requestMu.Lock()
			cancel := requests[message.ID]
			requestMu.Unlock()
			if cancel != nil {
				cancel()
			}
		case "lsp_server_response":
			if err := s.lsp.RespondServerRequest(client, message.ID, message.Result, message.Error); err != nil {
				sendLSPError(send, message.ID, message.ProfileID, err)
			}
		default:
			sendLSPError(send, message.ID, message.ProfileID, errors.New("unsupported LSP websocket message"))
		}
	}
}

func sendLSPError(send func(any), id, profileID string, err error) {
	send(map[string]any{"type": "lsp_error", "id": id, "profileId": profileID, "error": err.Error()})
}
