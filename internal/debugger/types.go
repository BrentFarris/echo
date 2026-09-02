// Package debugger implements Echo's workspace-scoped Debug Adapter Protocol
// runtime. Configuration persistence is deliberately kept in debugconfig.
package debugger

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brent/echo/internal/debugconfig"
)

const (
	StatusStarting    = "starting"
	StatusConfiguring = "configuring"
	StatusRunning     = "running"
	StatusStopped     = "stopped"
	StatusTerminating = "terminating"
	StatusTerminated  = "terminated"
	StatusFailed      = "failed"

	requestTimeout     = 15 * time.Second
	launchTimeout      = 2 * time.Minute
	initializedTimeout = 30 * time.Second
	outputReplayBytes  = 2 << 20
)

var (
	ErrWorkspaceNotFound = errors.New("debug workspace not found")
	ErrSessionNotFound   = errors.New("debug session not found")
	ErrStaleSession      = errors.New("debug session revision is stale")
	ErrStaleStop         = errors.New("debug stop generation is stale")
	ErrUnsupported       = errors.New("debug adapter capability is not supported")
)

type OutputEntry struct {
	Sequence  uint64         `json:"sequence"`
	Category  string         `json:"category"`
	Output    string         `json:"output"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

type SourceLocation struct {
	Name            string                 `json:"name,omitempty"`
	Path            string                 `json:"path,omitempty"`
	Ref             *debugconfig.SourceRef `json:"ref,omitempty"`
	SourceReference int                    `json:"sourceReference,omitempty"`
	Line            int                    `json:"line,omitempty"`
	Column          int                    `json:"column,omitempty"`
}

type BreakpointStatus struct {
	StateID   string          `json:"stateId"`
	AdapterID int             `json:"adapterId,omitempty"`
	Kind      string          `json:"kind"`
	Verified  bool            `json:"verified"`
	Message   string          `json:"message,omitempty"`
	Line      int             `json:"line,omitempty"`
	Column    int             `json:"column,omitempty"`
	Source    *SourceLocation `json:"source,omitempty"`
}

type SessionSnapshot struct {
	ID                 string             `json:"id"`
	WorkspaceID        string             `json:"workspaceId"`
	GroupID            string             `json:"groupId,omitempty"`
	ParentSessionID    string             `json:"parentSessionId,omitempty"`
	ConfigurationID    string             `json:"configurationId,omitempty"`
	Configuration      string             `json:"configuration"`
	AdapterProfileID   string             `json:"adapterProfileId"`
	Request            string             `json:"request"`
	Status             string             `json:"status"`
	Revision           uint64             `json:"revision"`
	StopGeneration     uint64             `json:"stopGeneration"`
	StoppedReason      string             `json:"stoppedReason,omitempty"`
	StoppedText        string             `json:"stoppedText,omitempty"`
	ThreadID           int                `json:"threadId,omitempty"`
	AllThreadsStopped  bool               `json:"allThreadsStopped,omitempty"`
	Capabilities       map[string]any     `json:"capabilities,omitempty"`
	Location           *SourceLocation    `json:"location,omitempty"`
	Error              string             `json:"error,omitempty"`
	StartedAt          time.Time          `json:"startedAt"`
	EndedAt            *time.Time         `json:"endedAt,omitempty"`
	LastOutputSequence uint64             `json:"lastOutputSequence,omitempty"`
	Output             []OutputEntry      `json:"output,omitempty"`
	Breakpoints        []BreakpointStatus `json:"breakpoints,omitempty"`
	TraceDAP           bool               `json:"traceDAP,omitempty"`
}

type GroupSnapshot struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SessionIDs []string `json:"sessionIds"`
	StopAll    bool     `json:"stopAll"`
}

type Snapshot struct {
	WorkspaceID string            `json:"workspaceId"`
	Sequence    uint64            `json:"sequence"`
	Sessions    []SessionSnapshot `json:"sessions"`
	Groups      []GroupSnapshot   `json:"groups"`
	State       debugconfig.State `json:"state"`
}

type Event struct {
	Type        string             `json:"type"`
	WorkspaceID string             `json:"workspaceId"`
	SessionID   string             `json:"sessionId,omitempty"`
	GroupID     string             `json:"groupId,omitempty"`
	Sequence    uint64             `json:"sequence"`
	Revision    uint64             `json:"revision,omitempty"`
	Event       string             `json:"event"`
	Body        json.RawMessage    `json:"body,omitempty"`
	Session     *SessionSnapshot   `json:"session,omitempty"`
	State       *debugconfig.State `json:"state,omitempty"`
	Output      *OutputEntry       `json:"output,omitempty"`
	Message     string             `json:"message,omitempty"`
}

type StartRequest struct {
	ConfigurationID string                 `json:"configurationId,omitempty"`
	CompoundID      string                 `json:"compoundId,omitempty"`
	CurrentFile     *debugconfig.SourceRef `json:"currentFile,omitempty"`
	SelectedText    string                 `json:"selectedText,omitempty"`
	Inputs          map[string]string      `json:"inputs,omitempty"`
	NoDebug         bool                   `json:"noDebug,omitempty"`
}

type ControlRequest struct {
	ExpectedRevision uint64         `json:"expectedRevision"`
	StopGeneration   uint64         `json:"stopGeneration,omitempty"`
	Arguments        map[string]any `json:"arguments,omitempty"`
}

type RequestResponse struct {
	WorkspaceID    string          `json:"workspaceId"`
	SessionID      string          `json:"sessionId"`
	Revision       uint64          `json:"revision"`
	StopGeneration uint64          `json:"stopGeneration"`
	Body           json.RawMessage `json:"body,omitempty"`
}

type RevisionError struct {
	Expected, Actual uint64
	Stop             bool
}

func (e *RevisionError) Error() string {
	if e.Stop {
		return fmt.Sprintf("debug stop generation changed: expected %d, actual %d", e.Expected, e.Actual)
	}
	return fmt.Sprintf("debug session revision changed: expected %d, actual %d", e.Expected, e.Actual)
}
func (e *RevisionError) Unwrap() error {
	if e.Stop {
		return ErrStaleStop
	}
	return ErrStaleSession
}

type Diagnostic struct {
	ProfileID string `json:"profileId"`
	Available bool   `json:"available"`
	Execution string `json:"execution"`
	Command   string `json:"command,omitempty"`
	Message   string `json:"message"`
}

type ProcessInfo struct {
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	CommandLine string `json:"commandLine,omitempty"`
	Execution   string `json:"execution"`
}
