//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

const accessibilitySocket = "/run/echo/accessibility/control.sock"

// The helper has no TCP listener and runs without the agent's root credentials.
// Disconnecting the proxy cancels traversal and prevents any subsequent action.
func (a *agent) uiCall(w http.ResponseWriter, r *http.Request) {
	if a.role != "runtime" {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "ui_capability_unavailable", "error": "UI control requires the unified runtime"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !json.Valid(data) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "code": "invalid_arguments", "error": "UI request must be bounded JSON"})
		return
	}
	// Compact to one line before using the line-delimited socket protocol.
	var request json.RawMessage
	_ = json.Unmarshal(data, &request)
	data, _ = json.Marshal(request)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", accessibilitySocket)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "code": "ui_capability_unavailable", "error": "Native accessibility is unavailable; refresh the sandbox runtime image"})
		return
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(45 * time.Second))
	if _, err = connection.Write(append(data, '\n')); err == nil {
		data, err = bufio.NewReader(io.LimitReader(connection, 4<<20)).ReadBytes('\n')
	}
	if err != nil || !json.Valid(data) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "code": "ui_native_interrupted", "error": "Accessibility operation interrupted; execution may be unknown. Observe before continuing"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
