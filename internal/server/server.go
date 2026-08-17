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
	"github.com/brent/echo/internal/settings"
)

// Server is the Echo HTTP server. It serves the SPA frontend from a static
// directory, hosts JSON API endpoints under /api, and runs a WebSocket hub for
// real-time events.
type Server struct {
	httpServer   *http.Server
	webDir       string
	hub          *Hub
	settingsPath string
	store        *settings.Store
	llm          chatStreamer
	llmSettings  llm.Settings
}

// chatStreamer is the subset of the LLM client the chat endpoint needs. It is
// an interface so tests can inject a fake streamer.
type chatStreamer interface {
	StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream
}

// New constructs a Server bound to addr that serves frontend assets from
// webDir. It does not start listening; call ListenAndServe.
func New(addr, webDir string) *Server {
	return NewWithSettingsPath(addr, webDir, "")
}

// NewWithSettingsPath constructs a Server like New but with an explicit path
// to the settings file. When settingsPath is empty, the platform default
// (echo/echo.json under the user config dir) is used.
func NewWithSettingsPath(addr, webDir, settingsPath string) *Server {
	s := &Server{
		webDir: webDir,
		hub:    NewHub(),
	}
	if settingsPath == "" {
		path, err := settings.DefaultStorePath()
		if err != nil {
			logf("resolve settings path: %v", err)
			path = "echo.json"
		}
		settingsPath = path
	}
	s.settingsPath = settingsPath
	s.store = settings.NewStore(settingsPath)
	s.initLLM()
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.routes(),
	}
	return s
}

// initLLM builds the LLM client used by the chat endpoint. It loads settings
// from the store (falling back to defaults) and selects the chat endpoint.
func (s *Server) initLLM() {
	cfg, err := s.store.Load()
	if err != nil {
		logf("load settings: %v", err)
		cfg = llm.DefaultSettings()
	}
	s.llmSettings = cfg.ForInteraction(llm.InteractionChat)
	client, err := llm.NewClient(s.llmSettings)
	if err != nil {
		logf("init llm client: %v", err)
		return
	}
	s.llm = client
}

// routes builds the HTTP handler tree for the server.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// JSON API endpoints.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/echo", s.handleEcho)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)

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
