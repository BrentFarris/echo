package server

import (
	"net/http"
)

func (s *Server) handleTerminateEcho(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusAccepted, map[string]any{"status": "terminating"})
	s.requestTermination()
}
