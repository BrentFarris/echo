package flowlog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Controller owns the optional AI flow log. All writes are serialized so the
// sequence field provides a total ordering even when multiple agents run at
// the same time.
type Controller struct {
	mu            sync.Mutex
	logger        *slog.Logger
	file          *os.File
	generation    uint64
	sequence      uint64
	nextRequestID uint64
}

// RequestTrace ties all records emitted by one LLM request to the capture that
// was active when the request began. A trace never leaks into a later capture.
type RequestTrace struct {
	controller *Controller
	generation uint64
	requestID  uint64
}

func NewController() *Controller {
	return &Controller{}
}

func (c *Controller) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logger != nil
}

// Enable starts a new capture, truncating any existing file at path.
func (c *Controller) Enable(path string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logger != nil {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	c.file = file
	c.logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.generation++
	c.sequence = 0
	c.nextRequestID = 0
	c.logLocked(slog.LevelInfo, "capture_started")
	return nil
}

// Disable writes a final boundary record and closes the current capture.
func (c *Controller) Disable() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logger == nil {
		return nil
	}
	c.logLocked(slog.LevelInfo, "capture_stopped")
	file := c.file
	c.logger = nil
	c.file = nil
	c.generation++
	if file != nil {
		return file.Close()
	}
	return nil
}

func (c *Controller) Close() error {
	return c.Disable()
}

// Log writes a capture-level event when logging is enabled.
func (c *Controller) Log(level slog.Level, event string, attrs ...slog.Attr) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logLocked(level, event, attrs...)
}

// StartRequest logs the exact request payload and returns a trace for response
// records. The configured model is repeated as a structured field for review.
func (c *Controller) StartRequest(model string, payload []byte) *RequestTrace {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logger == nil {
		return nil
	}
	c.nextRequestID++
	trace := &RequestTrace{
		controller: c,
		generation: c.generation,
		requestID:  c.nextRequestID,
	}
	c.logLocked(slog.LevelInfo, "llm_request",
		slog.Uint64("request_id", trace.requestID),
		slog.String("model", model),
		slog.String("payload", string(payload)),
	)
	return trace
}

func (t *RequestTrace) Log(level slog.Level, event string, attrs ...slog.Attr) {
	if t == nil || t.controller == nil {
		return
	}
	c := t.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logger == nil || c.generation != t.generation {
		return
	}
	withRequest := make([]slog.Attr, 0, len(attrs)+1)
	withRequest = append(withRequest, slog.Uint64("request_id", t.requestID))
	withRequest = append(withRequest, attrs...)
	c.logLocked(level, event, withRequest...)
}

func (c *Controller) logLocked(level slog.Level, event string, attrs ...slog.Attr) {
	if c.logger == nil {
		return
	}
	c.sequence++
	fields := make([]slog.Attr, 0, len(attrs)+2)
	fields = append(fields,
		slog.Uint64("sequence", c.sequence),
		slog.String("event", event),
	)
	fields = append(fields, attrs...)
	c.logger.LogAttrs(context.Background(), level, event, fields...)
}
