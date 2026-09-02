package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	terminalruntime "github.com/brent/echo/internal/terminal"
)

type terminalSizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *Server) handleListTerminalSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.terminal.List(r.PathValue("id"))
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleStartTerminalSession(w http.ResponseWriter, r *http.Request) {
	var body terminalSizeRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	snapshot, err := s.terminal.Start(r.PathValue("id"), body.Cols, body.Rows)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (s *Server) handleSyncTerminalSession(w http.ResponseWriter, r *http.Request) {
	var after uint64
	if value := r.URL.Query().Get("afterSequence"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "afterSequence must be an unsigned integer")
			return
		}
		after = parsed
	}
	snapshot, err := s.terminal.Sync(r.PathValue("id"), r.PathValue("sessionId"), after)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (s *Server) handleWriteTerminalSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data string `json:"data"`
	}
	// A JSON string may expand each input byte to a six-byte \u00XX escape.
	// Keep the transport bound finite while allowing every valid 64 KiB input.
	r.Body = http.MaxBytesReader(w, r.Body, terminalruntime.MaxInput*6+1024)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := s.terminal.Write(r.PathValue("id"), r.PathValue("sessionId"), body.Data); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"written": len(body.Data)})
}

func (s *Server) handleResizeTerminalSession(w http.ResponseWriter, r *http.Request) {
	var body terminalSizeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := s.terminal.Resize(r.PathValue("id"), r.PathValue("sessionId"), body.Cols, body.Rows); err != nil {
		writeTerminalError(w, err)
		return
	}
	cols, rows := terminalruntime.ClampSize(body.Cols, body.Rows)
	writeData(w, http.StatusOK, map[string]any{"cols": cols, "rows": rows})
}

func (s *Server) handleStopTerminalSession(w http.ResponseWriter, r *http.Request) {
	if err := s.terminal.Stop(r.PathValue("id"), r.PathValue("sessionId")); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"stopped": true})
}

func (s *Server) handleRestartTerminalSession(w http.ResponseWriter, r *http.Request) {
	var body terminalSizeRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	snapshot, err := s.terminal.Restart(r.PathValue("id"), r.PathValue("sessionId"), body.Cols, body.Rows)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (s *Server) handleListSavedCommands(w http.ResponseWriter, r *http.Request) {
	commands, err := s.terminal.ListSavedCommands(r.PathValue("id"))
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"commands": commands})
}

func (s *Server) handleCreateSavedCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	command, err := s.terminal.CreateSavedCommand(r.PathValue("id"), body.Name, body.Command)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"command": command})
}

func (s *Server) handleUpdateSavedCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	command, err := s.terminal.UpdateSavedCommand(r.PathValue("id"), r.PathValue("commandId"), body.Name, body.Command)
	if err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"command": command})
}

func (s *Server) handleDeleteSavedCommand(w http.ResponseWriter, r *http.Request) {
	if err := s.terminal.DeleteSavedCommand(r.PathValue("id"), r.PathValue("commandId")); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func writeTerminalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, terminalruntime.ErrWorkspaceNotFound),
		errors.Is(err, terminalruntime.ErrSessionNotFound),
		errors.Is(err, terminalruntime.ErrSavedCommandNotFound):
		writeCodedError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, terminalruntime.ErrWorkspaceUnavailable):
		writeCodedError(w, http.StatusUnprocessableEntity, "workspace_unavailable", err.Error(), nil)
	case errors.Is(err, terminalruntime.ErrSessionNotRunning):
		writeCodedError(w, http.StatusConflict, "terminal_not_running", err.Error(), nil)
	case errors.Is(err, terminalruntime.ErrInputTooLarge):
		writeCodedError(w, http.StatusRequestEntityTooLarge, "terminal_input_too_large", err.Error(), nil)
	default:
		writeCodedError(w, http.StatusBadRequest, "terminal_error", err.Error(), nil)
	}
}
