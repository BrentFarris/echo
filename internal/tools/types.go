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
	CodeNavigator  CodeNavigator
	Emit           EventEmitter
	FileChanges    FileChangeSink
}

type WorkspaceRoot struct {
	ID    string
	Label string
	Path  string
}

type CodeNavigator interface {
	QueryCode(ctx context.Context, request CodeNavigationRequest) (CodeNavigationResponse, error)
}

type CodeNavigationRequest struct {
	Operation          string `json:"operation"`
	Path               string `json:"path"`
	Line               int    `json:"line,omitempty"`
	Column             int    `json:"column,omitempty"`
	Position           *int   `json:"position,omitempty"`
	IncludeDeclaration *bool  `json:"includeDeclaration,omitempty"`
	MaxResults         int    `json:"maxResults,omitempty"`
	TriggerKind        int    `json:"triggerKind,omitempty"`
	TriggerCharacter   string `json:"triggerCharacter,omitempty"`
}

type CodeNavigationResponse struct {
	Operation               string               `json:"operation"`
	Path                    string               `json:"path"`
	LanguageID              string               `json:"languageId,omitempty"`
	Position                *CodePosition        `json:"position,omitempty"`
	Found                   bool                 `json:"found"`
	Message                 string               `json:"message,omitempty"`
	Locations               []CodeLocation       `json:"locations,omitempty"`
	Items                   []CodeCompletionItem `json:"items,omitempty"`
	Symbols                 []CodeSymbol         `json:"symbols,omitempty"`
	Hover                   string               `json:"hover,omitempty"`
	Range                   *CodeRange           `json:"range,omitempty"`
	ResultCount             int                  `json:"resultCount,omitempty"`
	ReturnedCount           int                  `json:"returnedCount,omitempty"`
	Truncated               bool                 `json:"truncated,omitempty"`
	SkippedOutsideWorkspace int                  `json:"skippedOutsideWorkspace,omitempty"`
}

type CodePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Offset int `json:"offset"`
}

type CodeRange struct {
	Start CodePosition `json:"start"`
	End   CodePosition `json:"end"`
}

type CodeLocation struct {
	Path    string    `json:"path"`
	Range   CodeRange `json:"range"`
	Preview string    `json:"preview,omitempty"`
}

type CodeCompletionItem struct {
	Label         string     `json:"label"`
	Kind          int        `json:"kind,omitempty"`
	KindName      string     `json:"kindName,omitempty"`
	Detail        string     `json:"detail,omitempty"`
	Documentation string     `json:"documentation,omitempty"`
	InsertText    string     `json:"insertText,omitempty"`
	ReplaceRange  *CodeRange `json:"replaceRange,omitempty"`
}

type CodeSymbol struct {
	Name           string     `json:"name"`
	Kind           int        `json:"kind,omitempty"`
	KindName       string     `json:"kindName,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	ContainerName  string     `json:"containerName,omitempty"`
	Path           string     `json:"path"`
	Range          CodeRange  `json:"range"`
	SelectionRange *CodeRange `json:"selectionRange,omitempty"`
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
