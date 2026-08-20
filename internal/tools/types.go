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

// AttachedVideo carries video output metadata from media-producing tools (e.g.
// comfyui_generate_video). The host registers results under VideoID so later
// tool calls (save_video) can resolve the payload without re-fetching.
type AttachedVideo struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	DataURL   string `json:"dataUrl"`
}

// VideoIDProvider is implemented by tool output types that produce a unique
// video ID usable by subsequent tool calls (e.g., save_video).
type VideoIDProvider interface {
	VideoID() string
}

// ExecutionContext carries the per-call state tools need to resolve and
// validate workspace paths.
type ExecutionContext struct {
	Context        context.Context
	WorkspacePath  string
	WorkspaceRoots []WorkspaceRoot
	// ResolveWorkspacePath and ResolveWorkspaceChildPath let the host route
	// tools through its canonical workspace confinement service. Tests and
	// standalone callers retain the local fallback when these are nil.
	ResolveWorkspacePath      func(string) (string, error)
	ResolveWorkspaceChildPath func(string) (string, error)
	// SearxngURL is the configured SearXNG endpoint used by the web_search tool.
	SearxngURL string
	// ComfyuiURL is the configured ComfyUI server base URL used by
	// comfyui_generate (e.g. "http://localhost:8188").
	ComfyuiURL string
	// ComfyuiDefaultCheckpoint is the default checkpoint/model name used by
	// comfyui_generate when no model argument is supplied.
	ComfyuiDefaultCheckpoint string
	// ComfyuiTxt2imgWorkflow is a workspace-relative path to the default
	// txt2img workflow used by comfyui_generate when none is supplied.
	ComfyuiTxt2imgWorkflow string
	// ComfyuiImg2imgWorkflow is a workspace-relative path to the default
	// img2img workflow used by comfyui_generate when an input image is present.
	ComfyuiImg2imgWorkflow string
	// ComfyuiVideoWorkflow is a workspace-relative path to the default video
	// generation workflow used by comfyui_generate_video when none is supplied.
	ComfyuiVideoWorkflow string
	// AttachedImages carries chat-attached images for tools that accept
	// in-memory image input (e.g. comfyui_generate img2img).
	AttachedImages []AttachedImage
	// FileChanges, when set, receives any file mutations a tool records during
	// execution so the caller can surface workspace changes to the user.
	FileChanges FileChangeSink
	// GeneratedImages tracks images produced by tools during the current turn,
	// keyed by ImageID. Used by save_image to resolve image data.
	GeneratedImages map[string]AttachedImage
	// GeneratedVideos tracks videos produced by tools during the current turn,
	// keyed by VideoID. Used by save_video to resolve video data.
	GeneratedVideos map[string]AttachedVideo
	// ToolScopes enforces the selected agent mode's tool and path allowlist.
	ToolScopes *ToolScopeChecker
	// AgentModes creates workspace-scoped custom agent modes when the
	// create_agent_mode tool is available to the current chat mode.
	AgentModes AgentModeProvider
	// WorkspaceSkills supplies workspace-local reusable guidance. It is set by
	// the chat host and intentionally does not participate in project file
	// change tracking because skills live under Echo's .echo metadata folder.
	WorkspaceSkills WorkspaceSkillsProvider
}

// AgentModeCreationRequest carries the model-provided definition for a custom
// agent mode. Permissions is the preferred per-tool path mapping. The flat
// tool/path lists remain supported for compatibility with legacy prompts.
type AgentModeCreationRequest struct {
	Name            string              `json:"name"`
	Prompt          string              `json:"prompt,omitempty"`
	ToolPermissions []string            `json:"toolPermissions,omitempty"`
	PathPermissions []string            `json:"pathPermissions,omitempty"`
	Permissions     map[string][]string `json:"permissions,omitempty"`
}

// AgentModeCreationResult describes the mode persisted by the host.
type AgentModeCreationResult struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Prompt      string                    `json:"prompt"`
	Permissions map[string]ToolPermission `json:"permissions,omitempty"`
}

// AgentModeProvider is supplied by the chat host so the tools package remains
// independent of the concrete agent-mode persistence implementation.
type AgentModeProvider interface {
	CreateAgentMode(context.Context, AgentModeCreationRequest) (AgentModeCreationResult, error)
}

const (
	DefaultWorkspaceSkillSearchLimit = 5
	MaxWorkspaceSkillSearchLimit     = 10
)

type WorkspaceSkillSearchRequest struct {
	Query  string `json:"query"`
	Folder string `json:"folder,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type WorkspaceSkillSearchResponse struct {
	Query    string                  `json:"query"`
	Skills   []WorkspaceSkillSummary `json:"skills"`
	Warnings []string                `json:"warnings,omitempty"`
}

type WorkspaceSkillReadRequest struct {
	ID string `json:"id"`
}

type WorkspaceSkillRecordRequest struct {
	Action           string   `json:"action"`
	Reason           string   `json:"reason,omitempty"`
	Folder           string   `json:"folder,omitempty"`
	Name             string   `json:"name,omitempty"`
	Description      string   `json:"description,omitempty"`
	Triggers         []string `json:"triggers,omitempty"`
	Body             string   `json:"body,omitempty"`
	DurabilityReason string   `json:"durabilityReason,omitempty"`
	FutureTasks      []string `json:"futureTasks,omitempty"`
	ExpectedRevision string   `json:"expectedRevision,omitempty"`
}

type WorkspaceSkillSummary struct {
	ID          string   `json:"id"`
	Folder      string   `json:"folder"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers,omitempty"`
}

type WorkspaceSkill struct {
	WorkspaceSkillSummary
	Body       string `json:"body"`
	Revision   string `json:"revision"`
	ModifiedAt string `json:"modifiedAt"`
}

type WorkspaceSkillRecordResponse struct {
	Action    string          `json:"action"`
	Reason    string          `json:"reason,omitempty"`
	Skill     *WorkspaceSkill `json:"skill,omitempty"`
	Created   bool            `json:"created,omitempty"`
	Unchanged bool            `json:"unchanged,omitempty"`
}

type WorkspaceSkillsProvider interface {
	SearchWorkspaceSkills(context.Context, WorkspaceSkillSearchRequest) (WorkspaceSkillSearchResponse, error)
	ReadWorkspaceSkill(context.Context, WorkspaceSkillReadRequest) (WorkspaceSkill, error)
	RecordWorkspaceSkill(context.Context, WorkspaceSkillRecordRequest) (WorkspaceSkillRecordResponse, error)
}

func NormalizeWorkspaceSkillSearchLimit(value int) int {
	if value <= 0 {
		return DefaultWorkspaceSkillSearchLimit
	}
	if value > MaxWorkspaceSkillSearchLimit {
		return MaxWorkspaceSkillSearchLimit
	}
	return value
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
