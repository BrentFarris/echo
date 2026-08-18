package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// Standard JSON envelope used by every API endpoint. Successful responses use
// {"ok":true,"data":...}; failures use {"ok":false,"error":"..."}.
type envelope struct {
	OK   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
}

type errorEnvelope struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details any    `json:"details,omitempty"`
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logf("writeJSON: %v", err)
	}
}

// writeData writes a successful envelope with the given payload.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, envelope{OK: true, Data: data})
}

// writeError writes a failure envelope with the given HTTP status and message.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorEnvelope{OK: false, Error: message})
}

func writeCodedError(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, errorEnvelope{OK: false, Error: message, Code: code, Details: details})
}

// handleHealth reports that the server is alive along with basic metadata.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{
		"service": "echo",
		"status":  "ok",
		"time":    time.Now().UTC().Format(time.RFC3339),
		"clients": s.hub.ClientCount(),
		"version": "0.1.0",
	})
}

// handleEcho is a sample endpoint that echoes back a message, demonstrating
// the request/response API pattern the frontend will use.
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	msg := r.URL.Query().Get("message")
	if msg == "" {
		msg = "hello from echo"
	}
	writeData(w, http.StatusOK, map[string]any{
		"message":  msg,
		"received": time.Now().UTC().Format(time.RFC3339),
	})
}
