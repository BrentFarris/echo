package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Schema map[string]any

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

type ExecutionContext struct {
	Context        context.Context
	WorkspacePath  string
	WorkspaceRoots []WorkspaceRoot
	SearxngURL     string
	Emit           EventEmitter
	FileChanges    FileChangeSink
}

type WorkspaceRoot struct {
	ID    string
	Label string
	Path  string
}

func (c ExecutionContext) context() context.Context {
	if c.Context == nil {
		return context.Background()
	}
	return c.Context
}

func (c ExecutionContext) emit(event Event) {
	if c.Emit != nil {
		c.Emit(event)
	}
}

func (c ExecutionContext) recordFileChanges(changes ...FileChange) {
	if c.FileChanges == nil || len(changes) == 0 {
		return
	}
	filtered := make([]FileChange, 0, len(changes))
	for _, change := range changes {
		if change.Path == "" || IsIgnoredChangePath(change.Path) || changeSnapshotsEqual(change.Before, change.After) {
			continue
		}
		filtered = append(filtered, change)
	}
	if len(filtered) > 0 {
		c.FileChanges(filtered)
	}
}

type EventEmitter func(Event)

type FileChangeSink func([]FileChange)

type Event struct {
	Type    string `json:"type"`
	Tool    string `json:"tool,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
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
