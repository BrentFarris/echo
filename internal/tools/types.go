package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Schema map[string]any

const (
	labeledPathSchemaHint         = "Start concrete paths with the workspace folder label, for example echo/frontend/src/main.ts; do not omit the label as in frontend/src/main.ts. Use . only for the virtual workspace root or all workspace folders when the tool allows it."
	labeledChangedPathsSchemaHint = "Every path must start with the workspace folder label, for example echo/frontend/src/main.ts; do not omit the label as in frontend/src/main.ts."
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

// AttachedImage carries a chat-attached image's metadata for tools that accept
// in-memory image input without writing to disk.
type AttachedImage struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	DataURL   string `json:"dataUrl"`
}

type ExecutionContext struct {
	Context          context.Context
	WorkspacePath    string
	WorkspaceRoots   []WorkspaceRoot
	SearxngURL               string
	ComfyuiURL               string
	ComfyuiDefaultCheckpoint string
	ComfyuiTxt2imgWorkflow   string
	ComfyuiImg2imgWorkflow   string
	CodeNavigator            CodeNavigator
	WorkspaceContext WorkspaceContextProvider
	WorkspaceSkills  WorkspaceSkillsProvider
	WorkspaceTasks   WorkspaceTasksProvider
	Emit             EventEmitter
	FileChanges      FileChangeSink
	// ToolScopes is the unified per-tool permission and path-scope checker.
	// Use this instead of ToolPermissions and PathPermissions.
	ToolScopes *ToolScopeChecker
	// ToolPermissions is deprecated; use ToolScopes.
	ToolPermissions *ToolPermissionChecker `json:"-"`
	// PathPermissions is deprecated; use ToolScopes.
	PathPermissions *PathMatcher `json:"-"`
	AgentModes      AgentModeProvider
	KanbanExecutor  KanbanExecutor
	KanbanManager   KanbanManager
	AttachedImages []AttachedImage
	// GeneratedImages tracks images produced by tools during the current turn,
	// keyed by ImageID. Used by save_image to resolve image data.
	GeneratedImages map[string]AttachedImage
	ResearchAgents  ResearchAgentCoordinator
}

type ResearchAgentSpec struct {
	Name string `json:"name,omitempty"`
	Task string `json:"task"`
}

type ResearchAgentSnapshot struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Phase    string `json:"phase,omitempty"`
	Report   string `json:"report,omitempty"`
	Error    string `json:"error,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
}

type ResearchAgentWaitResult struct {
	ConditionMet bool                    `json:"conditionMet"`
	Agents       []ResearchAgentSnapshot `json:"agents"`
}

// ResearchAgentCoordinator is attached only to a parent chat tool context.
// Research agents themselves never receive this interface, preventing nesting.
type ResearchAgentCoordinator interface {
	SpawnResearchAgents(ctx context.Context, agents []ResearchAgentSpec) ([]ResearchAgentSnapshot, error)
	SendResearchAgentMessage(ctx context.Context, agentID string, message string) (ResearchAgentSnapshot, error)
	WaitResearchAgents(ctx context.Context, agentIDs []string, waitFor string, timeout time.Duration) (ResearchAgentWaitResult, error)
	CancelResearchAgents(ctx context.Context, agentIDs []string) ([]ResearchAgentSnapshot, error)
}

// AgentModeSummary describes an available agent mode without the full prompt.
type AgentModeSummary struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ToolPermissions []string `json:"toolPermissions,omitempty"`
	PathPermissions []string `json:"pathPermissions,omitempty"`
	BuiltIn         bool     `json:"builtIn"`
}

// AgentModeCreationResult describes a newly created agent mode.
type AgentModeCreationResult struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Prompt          string   `json:"prompt"`
	ToolPermissions []string `json:"toolPermissions,omitempty"`
	PathPermissions []string `json:"pathPermissions,omitempty"`
}

// AgentModeCreationRequest carries the parameters for creating an agent mode.
type AgentModeCreationRequest struct {
	Name            string              `json:"name"`
	Prompt          string              `json:"prompt,omitempty"`
	ToolPermissions []string            `json:"toolPermissions,omitempty"`
	PathPermissions []string            `json:"pathPermissions,omitempty"`
	Permissions     map[string][]string `json:"permissions,omitempty"`
}

// AgentModeProvider supplies agent mode data to tools at execution time.
type AgentModeProvider interface {
	// ListModes returns the summaries of all available agent modes.
	ListModes() []AgentModeSummary
	// ResolveMode returns the summary for the given mode ID, or nil if not found.
	ResolveMode(id string) *AgentModeSummary
	// CreateMode creates a new user-defined agent mode with explicit parameters.
	CreateMode(ctx context.Context, request AgentModeCreationRequest) (AgentModeCreationResult, error)
	// CreateAgentModeFromChat analyzes the current chat transcript and creates
	// a new agent mode from synthesized tool usage patterns.
	CreateAgentModeFromChat(workspaceID string) (AgentModeCreationResult, error)
	// CreateModePerTool creates a new user-defined agent mode with per-tool
	// path permissions alongside name and prompt.
	CreateModePerTool(ctx context.Context, name string, prompt string, permissions map[string][]string) (AgentModeCreationResult, error)
}

type WorkspaceRoot struct {
	ID    string
	Label string
	Path  string
}

type CodeNavigator interface {
	QueryCode(ctx context.Context, request CodeNavigationRequest) (CodeNavigationResponse, error)
}

type WorkspaceContextProvider interface {
	QueryWorkspaceContext(ctx context.Context, request WorkspaceContextRequest) (WorkspaceContextResponse, error)
}

type WorkspaceSkillsProvider interface {
	SearchWorkspaceSkills(ctx context.Context, request WorkspaceSkillSearchRequest) (WorkspaceSkillSearchResponse, error)
	ReadWorkspaceSkill(ctx context.Context, request WorkspaceSkillReadRequest) (WorkspaceSkill, error)
	RecordWorkspaceSkill(ctx context.Context, request WorkspaceSkillRecordRequest) (WorkspaceSkillRecordResponse, error)
}

type WorkspaceTasksProvider interface {
	ListWorkspaceTasks(ctx context.Context, request WorkspaceTaskListRequest) (WorkspaceTaskListResponse, error)
	CreateWorkspaceTask(ctx context.Context, request WorkspaceTaskCreateRequest) (WorkspaceTaskMutationResponse, error)
	ConvertTaskToKanbanCard(ctx context.Context, request WorkspaceTaskConvertRequest) (WorkspaceTaskConversionResponse, error)
	UpdateWorkspaceTask(ctx context.Context, request WorkspaceTaskUpdateRequest) (WorkspaceTaskMutationResponse, error)
	DeleteWorkspaceTask(ctx context.Context, request WorkspaceTaskDeleteRequest) error
	SetWorkspaceTaskCompleted(ctx context.Context, request WorkspaceTaskCompleteRequest) (WorkspaceTaskMutationResponse, error)
	MoveWorkspaceTask(ctx context.Context, request WorkspaceTaskMoveRequest) (WorkspaceTaskMutationResponse, error)
	ReorderWorkspaceTasks(ctx context.Context, request WorkspaceTaskReorderRequest) (WorkspaceTaskMutationResponse, error)
}

// KanbanManager manages kanban card operations.
type KanbanManager interface {
	MoveKanbanCard(ctx context.Context, workspaceID string, cardID string, lane string) (KanbanBoard, error)
	DeleteKanbanCard(ctx context.Context, workspaceID string, cardID string) (KanbanBoard, error)
	ResetKanbanCard(ctx context.Context, workspaceID string, cardID string) (KanbanBoard, error)
	UpdateKanbanCardDescription(ctx context.Context, workspaceID string, cardID string, description string) (KanbanBoard, error)
	StopKanbanCard(ctx context.Context, workspaceID string, cardID string) error
}

// KanbanBoard groups kanban cards by lane.
type KanbanBoard struct {
	WorkspaceID string       `json:"workspaceId"`
	Ready       []KanbanCard `json:"ready"`
	InProgress  []KanbanCard `json:"inProgress"`
	Blocked     []KanbanCard `json:"blocked"`
	Done        []KanbanCard `json:"done"`
}

// KanbanCard represents a kanban execution card.
type KanbanCard struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspaceId"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Lane               string   `json:"lane"`
	Status             string   `json:"status"`
}

// KanbanExecutor starts kanban execution for a workspace.
type KanbanExecutor interface {
	StartKanbanExecutionWithContext(ctx context.Context, workspaceID string, concurrency int) error
}

type WorkspaceTaskListRequest struct {
	Priority         string `json:"priority,omitempty"`
	IncludeCompleted bool   `json:"includeCompleted,omitempty"`
}

type WorkspaceTaskCreateRequest struct {
	Title              string   `json:"title"`
	Details            string   `json:"details,omitempty"`
	Epic               string   `json:"epic,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority,omitempty"`
}

type WorkspaceTask struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Details            string   `json:"details,omitempty"`
	Epic               string   `json:"epic,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority"`
	SortOrder          int      `json:"sortOrder"`
	Completed          bool     `json:"completed"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
	CompletedAt        string   `json:"completedAt,omitempty"`
}

type WorkspaceTaskListResponse struct {
	StoragePath string          `json:"storagePath"`
	Tasks       []WorkspaceTask `json:"tasks"`
}

type WorkspaceTaskMutationResponse struct {
	Created WorkspaceTask   `json:"created"`
	Tasks   []WorkspaceTask `json:"tasks"`
}

type WorkspaceTaskConvertRequest struct {
	TaskID             string   `json:"taskID"`
	Title              string   `json:"title,omitempty"`
	Description        string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	ExpectedUpdatedAt  string   `json:"expectedUpdatedAt"`
}

type WorkspaceTaskConversionResponse struct {
	TaskID       string          `json:"taskID"`
	Task         *WorkspaceTask  `json:"task"`
	KanbanCardID string          `json:"kanbanCardID"`
	Tasks        []WorkspaceTask `json:"tasks"`
}

type WorkspaceTaskUpdateRequest struct {
	Title              string   `json:"title,omitempty"`
	Details            string   `json:"details,omitempty"`
	Epic               string   `json:"epic,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	TaskID             string   `json:"taskID"`
	ExpectedUpdatedAt  string   `json:"expectedUpdatedAt"`
}

type WorkspaceTaskDeleteRequest struct {
	TaskID            string `json:"taskID"`
	ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
}

type WorkspaceTaskCompleteRequest struct {
	Completed         bool   `json:"completed"`
	TaskID            string `json:"taskID"`
	ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
}

type WorkspaceTaskMoveRequest struct {
	Priority          string `json:"priority"`
	TaskID            string `json:"taskID"`
	ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
}

type WorkspaceTaskReorderRequest struct {
	TaskIDs  []string `json:"taskIDs"`
	Priority string   `json:"priority"`
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

func NormalizeWorkspaceSkillSearchLimit(value int) int {
	if value <= 0 {
		return DefaultWorkspaceSkillSearchLimit
	}
	if value > MaxWorkspaceSkillSearchLimit {
		return MaxWorkspaceSkillSearchLimit
	}
	return value
}

const (
	DefaultWorkspaceContextMaxFiles = 12
	MaxWorkspaceContextMaxFiles     = 30
	WorkspaceContextBriefMaxBytes   = 32 * 1024
)

type WorkspaceContextRequest struct {
	Task         string   `json:"task"`
	Path         string   `json:"path,omitempty"`
	ChangedPaths []string `json:"changedPaths,omitempty"`
	MaxFiles     int      `json:"maxFiles,omitempty"`
}

type WorkspaceContextResponse struct {
	Task                 string                        `json:"task"`
	Path                 string                        `json:"path,omitempty"`
	Brief                string                        `json:"brief"`
	ProjectRoots         []WorkspaceContextProjectRoot `json:"projectRoots,omitempty"`
	DetectedLanguages    []string                      `json:"detectedLanguages,omitempty"`
	Manifests            []WorkspaceContextManifest    `json:"manifests,omitempty"`
	LikelyCommands       []WorkspaceContextCommand     `json:"likelyCommands,omitempty"`
	RelevantFiles        []WorkspaceContextFile        `json:"relevantFiles,omitempty"`
	LikelyTestFiles      []WorkspaceContextFile        `json:"likelyTestFiles,omitempty"`
	VerificationCommands []WorkspaceContextCommand     `json:"verificationCommands,omitempty"`
	Warnings             []string                      `json:"warnings,omitempty"`
	Truncated            bool                          `json:"truncated,omitempty"`
}

type WorkspaceContextProjectRoot struct {
	Path      string   `json:"path"`
	Kind      string   `json:"kind"`
	Manifests []string `json:"manifests,omitempty"`
}

type WorkspaceContextManifest struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type WorkspaceContextCommand struct {
	Kind             string `json:"kind"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
}

type WorkspaceContextFile struct {
	Path    string                  `json:"path"`
	Kind    string                  `json:"kind,omitempty"`
	Reason  string                  `json:"reason,omitempty"`
	Score   int                     `json:"score,omitempty"`
	Matches []WorkspaceContextMatch `json:"matches,omitempty"`
	Symbols []CodeSymbol            `json:"symbols,omitempty"`
}

type WorkspaceContextMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func NormalizeWorkspaceContextRequest(request WorkspaceContextRequest) WorkspaceContextRequest {
	request.Task = strings.TrimSpace(request.Task)
	request.Path = strings.TrimSpace(strings.ReplaceAll(request.Path, "\\", "/"))
	if request.Path == "" {
		request.Path = "."
	}
	request.MaxFiles = NormalizeWorkspaceContextMaxFiles(request.MaxFiles)

	seen := map[string]bool{}
	changed := make([]string, 0, len(request.ChangedPaths))
	for _, path := range request.ChangedPaths {
		path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
		path = strings.Trim(path, "/")
		if path == "" || path == "." {
			continue
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		changed = append(changed, path)
	}
	sort.Strings(changed)
	request.ChangedPaths = changed
	return request
}

func NormalizeWorkspaceContextMaxFiles(value int) int {
	if value <= 0 {
		return DefaultWorkspaceContextMaxFiles
	}
	if value > MaxWorkspaceContextMaxFiles {
		return MaxWorkspaceContextMaxFiles
	}
	return value
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
