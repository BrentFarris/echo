package server

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/auth"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspaces"
	"github.com/gorilla/websocket"
)

func (s *Server) sandboxGUIEnabled(workspaceID string) bool {
	return s.sandbox != nil && s.sandbox.IsEnabled(workspaceID)
}

func (s *Server) handleSandboxHost(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, s.sandbox.Host(r.Context()))
}

func (s *Server) handleGetWorkspaceSandbox(w http.ResponseWriter, r *http.Request) {
	workspace, ok, err := s.workspaces.Get(r.PathValue("id"))
	if err != nil || !ok {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace was not found", nil)
		return
	}
	status, err := s.sandbox.Status(r.Context(), workspace.ID)
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"config": workspace.Sandbox, "status": status})
}

func (s *Server) handlePutWorkspaceSandbox(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	var body struct {
		Config *workspaces.SandboxConfig `json:"config"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		return
	}
	if body.Config == nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_sandbox_config", "config is required", nil)
		return
	}
	config, err := workspaces.NormalizeSandboxConfig(*body.Config)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_sandbox_config", err.Error(), nil)
		return
	}
	current, ok, err := s.workspaces.Get(workspaceID)
	if err != nil || !ok {
		writeCodedError(w, http.StatusNotFound, "workspace_not_found", "workspace was not found", nil)
		return
	}
	transitionActive := current.Sandbox.Enabled != config.Enabled
	transitionCommitted := false
	if transitionActive {
		if err := s.sandbox.BeginPolicyTransition(workspaceID, config.Enabled); err != nil {
			writeSandboxError(w, err)
			return
		}
		defer func() {
			if !transitionCommitted {
				s.sandbox.EndPolicyTransition(workspaceID, current.Sandbox.Enabled)
			}
		}()
	}
	if config.Enabled {
		if _, err := s.sandbox.Preflight(r.Context(), workspaceID, config); err != nil {
			writeSandboxError(w, err)
			return
		}
		if !current.Sandbox.Enabled {
			// Do not let a terminal, LSP, Git operation, or chat tool that was
			// started on the host survive the execution-target transition.
			s.stopSandboxWorkspaceProcesses(workspaceID)
		}
	} else if current.Sandbox.Enabled {
		// Stop every workspace-scoped process before host routing becomes active.
		s.stopSandboxWorkspaceProcesses(workspaceID)
		if err := s.sandbox.Stop(r.Context(), workspaceID); err != nil {
			writeSandboxError(w, err)
			return
		}
	}
	if current.Sandbox.Enabled && config.Enabled {
		if err := s.sandbox.UpdateResources(r.Context(), workspaceID, config); err != nil {
			writeSandboxError(w, err)
			return
		}
	}
	workspace, err := s.workspaces.SetSandboxConfig(workspaceID, config)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, "sandbox_config_save_failed", err.Error(), nil)
		return
	}
	if transitionActive {
		s.sandbox.EndPolicyTransition(workspaceID, config.Enabled)
		transitionCommitted = true
	}
	s.refreshWorkspaceCaches(r.Context(), workspaceID)
	status, _ := s.sandbox.Status(r.Context(), workspaceID)
	writeData(w, http.StatusOK, map[string]any{"config": workspace.Sandbox, "status": status})
}

func (s *Server) handleWorkspaceSandboxAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action         string `json:"action"`
		ApprovedDigest string `json:"approvedDigest,omitempty"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		return
	}
	workspaceID := r.PathValue("id")
	var result any = map[string]any{"action": body.Action}
	var err error
	switch strings.TrimSpace(body.Action) {
	case "start":
		err = s.sandbox.Start(r.Context(), workspaceID)
	case "stop":
		s.stopSandboxWorkspaceProcesses(workspaceID)
		err = s.sandbox.Stop(r.Context(), workspaceID)
	case "pull":
		err = s.sandbox.Pull(r.Context(), workspaceID)
	case "recreate":
		s.stopSandboxWorkspaceProcesses(workspaceID)
		err = s.sandbox.Recreate(r.Context(), workspaceID)
	case "reset_workbench":
		s.stopSandboxWorkspaceProcesses(workspaceID)
		err = s.sandbox.Reset(r.Context(), workspaceID, "workbench")
	case "reset_browser":
		s.stopSandboxWorkspaceProcesses(workspaceID)
		err = s.sandbox.Reset(r.Context(), workspaceID, "browser")
	case "run_setup":
		result, err = s.sandbox.RunSetup(r.Context(), workspaceID, body.ApprovedDigest)
	default:
		writeCodedError(w, http.StatusBadRequest, "invalid_sandbox_action", "unsupported sandbox action", nil)
		return
	}
	if err != nil {
		if errors.Is(err, sandbox.ErrSetupApproval) {
			writeCodedError(w, http.StatusConflict, sandbox.ErrorCode(err), err.Error(), result)
			return
		}
		writeSandboxError(w, err)
		return
	}
	status, _ := s.sandbox.Status(r.Context(), workspaceID)
	writeData(w, http.StatusOK, map[string]any{"result": result, "status": status})
}

func (s *Server) handleDeleteWorkspaceSandbox(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	s.stopSandboxWorkspaceProcesses(workspaceID)
	if err := s.sandbox.Delete(r.Context(), workspaceID); err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true, "workspaceFilesRetained": true})
}

func (s *Server) stopSandboxWorkspaceProcesses(workspaceID string) {
	s.sessions.invalidate(workspaceID)
	s.terminal.StopWorkspace(workspaceID)
	s.lsp.StopWorkspaceProcesses(workspaceID)
	s.git.StopWorkspaceProcesses(workspaceID)
}

func (s *Server) handleGetSandboxNetworkGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := s.sandbox.NetworkGrants(r.PathValue("id"))
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"grants": grants})
}

func (s *Server) handleCreateSandboxNetworkGrant(w http.ResponseWriter, r *http.Request) {
	var grant sandbox.NetworkGrant
	if err := decodeLimitedJSON(w, r, &grant, 64<<10); err != nil {
		return
	}
	created, err := s.sandbox.AddNetworkGrant(r.Context(), r.PathValue("id"), grant)
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"grant": created})
}

func (s *Server) handleDeleteSandboxNetworkGrant(w http.ResponseWriter, r *http.Request) {
	grantID := strings.TrimSpace(r.URL.Query().Get("id"))
	if grantID == "" {
		writeCodedError(w, http.StatusBadRequest, "network_grant_id_required", "network grant id is required", nil)
		return
	}
	if err := s.sandbox.DeleteNetworkGrant(r.Context(), r.PathValue("id"), grantID); err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) authenticatedBrowserSession(r *http.Request) string {
	if current, ok := r.Context().Value(authContextKey{}).(auth.Session); ok {
		return current.ID
	}
	if s.authDisabled {
		return "local-auth-disabled"
	}
	return ""
}

func (s *Server) handleCreateSandboxDesktopSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session, err := s.sandbox.CreateDesktopSession(r.Context(), r.PathValue("id"), s.authenticatedBrowserSession(r))
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"session": session})
}

func (s *Server) handleDeleteSandboxDesktopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("id"))
	if sessionID == "" {
		writeCodedError(w, http.StatusBadRequest, "desktop_session_id_required", "desktop session id is required", nil)
		return
	}
	if err := s.sandbox.DeleteDesktopSession(r.PathValue("id"), sessionID, s.authenticatedBrowserSession(r)); err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleSandboxDesktopControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm,omitempty"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		return
	}
	var lease sandbox.DesktopLease
	var err error
	browserSession := s.authenticatedBrowserSession(r)
	switch body.Action {
	case "take":
		lease, err = s.sandbox.TakeUserControl(r.PathValue("id"), browserSession, body.Confirm)
	case "release":
		lease, err = s.sandbox.ReleaseUserControl(r.PathValue("id"), browserSession)
	default:
		writeCodedError(w, http.StatusBadRequest, "invalid_desktop_control_action", "action must be take or release", nil)
		return
	}
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"lease": lease})
}

var desktopUpgrader = websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10, CheckOrigin: requestOriginAllowed, Subprotocols: []string{"binary"}}

func (s *Server) handleSandboxDesktopWebSocket(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	sessionID := r.URL.Query().Get("sessionId")
	browserSessionID := s.authenticatedBrowserSession(r)
	connection, err := desktopUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	stream, err := s.sandbox.OpenDesktop(r.Context(), workspaceID, sessionID, browserSessionID)
	if err != nil {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, err.Error()), time.Now().Add(time.Second))
		return
	}
	defer stream.Close()
	defer s.sandbox.CloseDesktopConnection(workspaceID, sessionID, browserSessionID)
	clientFilter := newRFBClientFilter()
	var readers sync.WaitGroup
	var websocketWrite sync.Mutex
	readers.Add(1)
	go func() {
		defer readers.Done()
		defer connection.Close()
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := stream.Read(buffer)
			if count > 0 {
				websocketWrite.Lock()
				writeErr := connection.WriteMessage(websocket.BinaryMessage, buffer[:count])
				websocketWrite.Unlock()
				if writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		messageType, data, readErr := connection.ReadMessage()
		if readErr != nil {
			break
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		filtered, filterErr := clientFilter.Filter(data, s.sandbox.DesktopInputAllowed(workspaceID, browserSessionID))
		if filterErr != nil {
			websocketWrite.Lock()
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "unsupported desktop input"), time.Now().Add(time.Second))
			websocketWrite.Unlock()
			break
		}
		if len(filtered) == 0 {
			continue
		}
		if _, writeErr := stream.Write(filtered); writeErr != nil {
			break
		}
	}
	_ = stream.Close()
	_ = connection.Close()
	readers.Wait()
}

func writeSandboxError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := sandbox.ErrorCode(err)
	switch code {
	case "workspace_not_found", "desktop_session_not_found", "network_grant_not_found", "setup_recipe_missing":
		status = http.StatusNotFound
	case "authentication_required":
		status = http.StatusUnauthorized
	case "sandbox_disabled", "invalid_sandbox_config", "invalid_network_grant", "invalid_sandbox_action", "network_grant_id_required", "desktop_session_id_required":
		status = http.StatusBadRequest
	case "setup_approval_required", "desktop_control_conflict", "user_control_active", "network_grant_exists", "network_alias_conflict", "sandbox_transitioning", "sandbox_protocol_mismatch":
		status = http.StatusConflict
	case "sandbox_unavailable", "docker_unavailable", "docker_linux_engine_required", "docker_architecture_unsupported", "sandbox_images_missing", "desktop_unavailable", "sandbox_agent_unavailable", "sandbox_service_unavailable", "egress_unavailable":
		status = http.StatusServiceUnavailable
	}
	message := err.Error()
	var sandboxError *sandbox.Error
	if errors.As(err, &sandboxError) && sandboxError.Message != "" {
		message = sandboxError.Message
	}
	writeCodedError(w, status, code, message, nil)
}
