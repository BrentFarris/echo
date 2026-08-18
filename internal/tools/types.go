package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Schema map[string]any

const (
	// labeledPathSchemaHint teaches models how to address paths inside a
	// workspace that has one or more labeled root folders.
	labeledPathSchemaHint = "Start concrete paths with the workspace folder label, for example echo/frontend/src/main.ts; do not omit the label as in frontend/src/main.ts. Use . only for the virtual workspace root or all workspace folders when the tool allows it."
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  Schema `json:"parameters,omitempty"`
}

type Tool interface {
	Metadata() Metadata
	Execute(ctx ExecutionContext, arguments json.RawMessage) (any, error)
}

type ToolFunc struct {
	Meta Metadata
	Run  func(ctx ExecutionContext, arguments json.RawMessage) (any, error)
}

func (t ToolFunc) Metadata() Metadata {
	return t.Meta
}

func (t ToolFunc) Execute(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if t.Run == nil {
		return nil, fmt.Errorf("tool handler is not configured")
	}
	return t.Run(ctx, arguments)
}

// WorkspaceRoot describes one labeled root folder a tool may operate on.
type WorkspaceRoot struct {
	ID    string
	Label string
	Path  string
}

// AttachedImage carries a chat-attached image's metadata for tools that accept
// in-memory image input without writing to disk.
type AttachedImage struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	DataURL   string `json:"dataUrl"`
}

// ExecutionContext carries the per-call state tools need to resolve and
// validate workspace paths.
type ExecutionContext struct {
	Context        context.Context
	WorkspacePath  string
	WorkspaceRoots []WorkspaceRoot
	// SearxngURL is the configured SearXNG endpoint used by the web_search tool.
	SearxngURL string
	// FileChanges, when set, receives any file mutations a tool records during
	// execution so the caller can surface workspace changes to the user.
	FileChanges FileChangeSink
	// GeneratedImages tracks images produced by tools during the current turn,
	// keyed by ImageID. Used by save_image to resolve image data.
	GeneratedImages map[string]AttachedImage
}

func (c ExecutionContext) context() context.Context {
	if c.Context == nil {
		return context.Background()
	}
	return c.Context
}

type ExecutionResult struct {
	Tool    string          `json:"tool"`
	Success bool            `json:"success"`
	Output  any             `json:"output,omitempty"`
	Error   *ExecutionError `json:"error,omitempty"`
}

type ExecutionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SafeError struct {
	Code    string
	Message string
}

func (e SafeError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

func safeError(code string, err error) *ExecutionError {
	if err == nil {
		return nil
	}
	var safe SafeError
	if errors.As(err, &safe) {
		if safe.Code == "" {
			safe.Code = code
		}
		return &ExecutionError{Code: safe.Code, Message: safe.Message}
	}
	return &ExecutionError{Code: code, Message: err.Error()}
}
