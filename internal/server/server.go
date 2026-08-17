// Package server implements the browser-based Echo web server. It serves the
// single-page application frontend from a static directory and exposes JSON
// API endpoints plus a WebSocket endpoint for real-time, server-to-client push.
package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/llm"
)

// LLM endpoint configuration. These are hard-coded for now so the chat view
// can be tested against a known server; they will move into user-configurable
// settings (and be removed from here) in a later step.
const (
	llmEndpoint = "http://192.168.50.178:8023/v1"
	llmModel    = "deepseek-ai/DeepSeek-V4-Flash-0731"
)

// Server is the Echo HTTP server. It serves the SPA frontend from a static
// directory, hosts JSON API endpoints under /api, and runs a WebSocket hub for
// real-time events.
type Server struct {
	httpServer  *http.Server
	webDir      string
	hub         *Hub
	llm         chatStreamer
	llmSettings llm.Settings
}

// chatStreamer is the subset of the LLM client the chat endpoint needs. It is
// an interface so tests can inject a fake streamer.
type chatStreamer interface {
	StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream
}

// New constructs a Server bound to addr that serves frontend assets from
// webDir. It does not start listening; call ListenAndServe.
func New(addr, webDir string) *Server {
	s := &Server{
		webDir: webDir,
		hub:    NewHub(),
	}
	s.initLLM()
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.routes(),
	}
	return s
}

// initLLM builds the LLM client used by the chat endpoint. It uses the
// hard-coded endpoint/model until settings support is added.
func (s *Server) initLLM() {
	settings := llm.DefaultSettings()
	settings.Endpoint = llmEndpoint
	settings.Model = llmModel
	settings.MaxTokens = 2048
	client, err := llm.NewClient(settings)
	if err != nil {
		logf("init llm client: %v", err)
		return
	}
	s.llm = client
	s.llmSettings = settings
}

// routes builds the HTTP handler tree for the server.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// JSON API endpoints.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/echo", s.handleEcho)

	// WebSocket endpoint for real-time push.
	mux.HandleFunc("GET /ws", s.handleWebSocket)

	// Static assets from the web directory.
	fileServer := http.FileServer(http.Dir(s.webDir))
	mux.Handle("/", fileServer)

	// Wrap the mux with SPA fallback so unknown non-API paths serve index.html,
	// enabling client-side routing.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never rewrite API or WebSocket paths.
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			mux.ServeHTTP(w, r)
			return
		}
		// Let the file server handle real files; otherwise fall back to index.html.
		if isStaticAsset(r.URL.Path, s.webDir) {
			mux.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, s.webDir)
	})
}

// isStaticAsset reports whether path maps to an existing file under webDir.
func isStaticAsset(path, webDir string) bool {
	if path == "/" {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	full := filepath.Join(webDir, clean)
	info, err := os.Stat(full)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// serveIndex writes the SPA entry point for the given request.
func serveIndex(w http.ResponseWriter, r *http.Request, webDir string) {
	index := filepath.Join(webDir, "index.html")
	http.ServeFile(w, r, index)
}

// ListenAndServe starts serving HTTP requests on the configured address.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.hub.Shutdown()
	return s.httpServer.Shutdown(ctx)
}

// logf is a small logging helper so handlers can log without importing log
// everywhere.
func logf(format string, args ...any) {
	log.Printf(format, args...)
}
