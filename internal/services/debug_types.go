package services

import "encoding/json"

const (
	DebugRuntimeEventName = debugEventName
	debugEventName        = "echo:debug:event"

	DebugStatusIdle       = "idle"
	DebugStatusStarting   = "starting"
	DebugStatusRunning    = "running"
	DebugStatusPaused     = "paused"
	DebugStatusStopping   = "stopping"
	DebugStatusTerminated = "terminated"
	DebugStatusError      = "error"
)

// DebugStartRequest selects and starts a workspace debug configuration. An
// empty configurationName uses the workspace selection (and, for a
// single-folder Go workspace, the implicit Go launch configuration).
type DebugStartRequest struct {
	ConfigurationName string `json:"configurationName,omitempty"`
	CurrentFile       string `json:"currentFile,omitempty"`
}

// DebugSessionRequest is used by execution-control operations.
type DebugSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type DebugSourceBreakpoint struct {
	Line   int `json:"line"`
	Column int `json:"column,omitempty"`
}

type DebugBreakpoint struct {
	ID       int    `json:"id,omitempty"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Verified bool   `json:"verified"`
	Message  string `json:"message,omitempty"`
}

type DebugSetBreakpointsRequest struct {
	SessionID   string                  `json:"sessionId,omitempty"`
	SourcePath  string                  `json:"sourcePath"`
	Breakpoints []DebugSourceBreakpoint `json:"breakpoints"`
}

type DebugSourceLocation struct {
	Path      string `json:"path,omitempty"`
	Name      string `json:"name,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	External  bool   `json:"external,omitempty"`
	SourceRef int    `json:"sourceReference,omitempty"`
}

// DebugState is the recoverable public snapshot of the one app-wide debug
// session. References are valid only while this exact session is paused.
type DebugState struct {
	WorkspaceID     string               `json:"workspaceId,omitempty"`
	SessionID       string               `json:"sessionId,omitempty"`
	Revision        uint64               `json:"revision"`
	Status          string               `json:"status"`
	Configuration   string               `json:"configuration,omitempty"`
	AdapterType     string               `json:"adapterType,omitempty"`
	ThreadID        int                  `json:"threadId,omitempty"`
	FrameID         int                  `json:"frameId,omitempty"`
	CurrentLocation *DebugSourceLocation `json:"currentLocation,omitempty"`
	Breakpoints     []DebugBreakpoint    `json:"breakpoints,omitempty"`
	Output          string               `json:"output,omitempty"`
	Error           string               `json:"error,omitempty"`
	Capabilities    json.RawMessage      `json:"capabilities,omitempty"`
}

type DebugEvent struct {
	WorkspaceID string      `json:"workspaceId,omitempty"`
	SessionID   string      `json:"sessionId,omitempty"`
	Revision    uint64      `json:"revision"`
	Type        string      `json:"type"`
	State       *DebugState `json:"state,omitempty"`
	Category    string      `json:"category,omitempty"`
	Output      string      `json:"output,omitempty"`
	Message     string      `json:"message,omitempty"`
}

type DebugThreadsRequest struct {
	SessionID string `json:"sessionId"`
}

type DebugThread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type DebugThreadsResponse struct {
	WorkspaceID string        `json:"workspaceId"`
	SessionID   string        `json:"sessionId"`
	Revision    uint64        `json:"revision"`
	Threads     []DebugThread `json:"threads"`
}

type DebugStackTraceRequest struct {
	SessionID  string `json:"sessionId"`
	ThreadID   int    `json:"threadId"`
	StartFrame int    `json:"startFrame,omitempty"`
	Levels     int    `json:"levels,omitempty"`
}

type DebugStackFrame struct {
	ID       int                 `json:"id"`
	Name     string              `json:"name"`
	Location DebugSourceLocation `json:"location"`
}

type DebugStackTraceResponse struct {
	WorkspaceID string            `json:"workspaceId"`
	SessionID   string            `json:"sessionId"`
	Revision    uint64            `json:"revision"`
	StackFrames []DebugStackFrame `json:"stackFrames"`
	TotalFrames int               `json:"totalFrames,omitempty"`
}

type DebugScopesRequest struct {
	SessionID string `json:"sessionId"`
	FrameID   int    `json:"frameId"`
}

type DebugScope struct {
	Name               string               `json:"name"`
	PresentationHint   string               `json:"presentationHint,omitempty"`
	VariablesReference int                  `json:"variablesReference"`
	NamedVariables     int                  `json:"namedVariables,omitempty"`
	IndexedVariables   int                  `json:"indexedVariables,omitempty"`
	Expensive          bool                 `json:"expensive"`
	Location           *DebugSourceLocation `json:"location,omitempty"`
}

type DebugScopesResponse struct {
	WorkspaceID string       `json:"workspaceId"`
	SessionID   string       `json:"sessionId"`
	Revision    uint64       `json:"revision"`
	Scopes      []DebugScope `json:"scopes"`
}

type DebugVariablesRequest struct {
	SessionID          string `json:"sessionId"`
	VariablesReference int    `json:"variablesReference"`
	Filter             string `json:"filter,omitempty"`
	Start              int    `json:"start,omitempty"`
	Count              int    `json:"count,omitempty"`
}

type DebugVariable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	EvaluateName       string `json:"evaluateName,omitempty"`
	VariablesReference int    `json:"variablesReference"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	MemoryReference    string `json:"memoryReference,omitempty"`
}

type DebugVariablesResponse struct {
	WorkspaceID string          `json:"workspaceId"`
	SessionID   string          `json:"sessionId"`
	Revision    uint64          `json:"revision"`
	Variables   []DebugVariable `json:"variables"`
}

type DebugEvaluateRequest struct {
	SessionID  string `json:"sessionId"`
	Expression string `json:"expression"`
	FrameID    int    `json:"frameId,omitempty"`
	Context    string `json:"context,omitempty"`
}

type DebugEvaluateResponse struct {
	WorkspaceID        string `json:"workspaceId"`
	SessionID          string `json:"sessionId"`
	Revision           uint64 `json:"revision"`
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference,omitempty"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	MemoryReference    string `json:"memoryReference,omitempty"`
}
