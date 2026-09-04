// Package sourcecontrol defines Echo's provider-neutral source control model.
// Provider implementations keep absolute paths and native command details on
// the server; browser callers address repositories by opaque IDs and files by
// workspace-relative references.
package sourcecontrol

import (
	"errors"

	"github.com/brent/echo/internal/workspacefs"
)

const (
	StatusLimit     = 10_000
	HistoryPageSize = 100
)

var (
	ErrNotFound    = errors.New("source control repository not found")
	ErrInvalidPath = errors.New("invalid source control path")
)

// Error is safe to return through the HTTP API.
type Error struct {
	Code    string
	Message string
	Cause   error
	Details any
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type Capability string

const (
	CapabilityStatus         Capability = "status"
	CapabilityDiff           Capability = "diff"
	CapabilityHistory        Capability = "history"
	CapabilityStage          Capability = "stage"
	CapabilityTrack          Capability = "track"
	CapabilityCommitAll      Capability = "commitAll"
	CapabilityCommitSelected Capability = "commitSelected"
	CapabilityUpdate         Capability = "update"
	CapabilitySync           Capability = "sync"
	CapabilityPull           Capability = "pull"
	CapabilityPush           Capability = "push"
	CapabilityBranches       Capability = "branches"
	CapabilityMerge          Capability = "merge"
	CapabilityStashes        Capability = "stashes"
	CapabilityInitialize     Capability = "initialize"
	CapabilityClone          Capability = "clone"
)

type ProviderDescriptor struct {
	ID           string       `json:"id"`
	Label        string       `json:"label"`
	Available    bool         `json:"available"`
	Version      string       `json:"version,omitempty"`
	Diagnostic   string       `json:"diagnostic,omitempty"`
	Capabilities []Capability `json:"capabilities"`
}

type Scope struct {
	RootID     string `json:"rootId"`
	RootLabel  string `json:"rootLabel"`
	RepoPrefix string `json:"repoPrefix"`
}

type Repository struct {
	ID            string               `json:"id"`
	ProviderID    string               `json:"providerId"`
	ProviderLabel string               `json:"providerLabel"`
	Label         string               `json:"label"`
	RootRef       *workspacefs.FileRef `json:"rootRef,omitempty"`
	Parent        bool                 `json:"parent"`
	Scopes        []Scope              `json:"scopes"`
	Revision      uint64               `json:"revision"`
	Available     bool                 `json:"available"`
	Diagnostic    string               `json:"diagnostic,omitempty"`
	Capabilities  []Capability         `json:"capabilities"`
}

type Change struct {
	Path       string               `json:"path"`
	OldPath    string               `json:"oldPath,omitempty"`
	Ref        *workspacefs.FileRef `json:"ref,omitempty"`
	Status     string               `json:"status"`
	StatusCode string               `json:"statusCode"`
	Kind       string               `json:"kind,omitempty"`
	GroupID    string               `json:"groupId"`
	Submodule  bool                 `json:"submodule,omitempty"`
}

type ChangeGroup struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Role    string   `json:"role"`
	Changes []Change `json:"changes"`
	Actions []string `json:"actions"`
}

type RepositoryState struct {
	MergeInProgress      bool `json:"mergeInProgress,omitempty"`
	RebaseInProgress     bool `json:"rebaseInProgress,omitempty"`
	CherryPickInProgress bool `json:"cherryPickInProgress,omitempty"`
}

type StatusSnapshot struct {
	WorkspaceID       string          `json:"workspaceId"`
	RepositoryID      string          `json:"repositoryId"`
	ProviderID        string          `json:"providerId"`
	Revision          uint64          `json:"revision"`
	Branch            string          `json:"branch"`
	Head              string          `json:"head,omitempty"`
	Detached          bool            `json:"detached"`
	Upstream          string          `json:"upstream,omitempty"`
	Ahead             int             `json:"ahead,omitempty"`
	Behind            int             `json:"behind,omitempty"`
	StashCount        int             `json:"stashCount,omitempty"`
	Groups            []ChangeGroup   `json:"groups"`
	HiddenChangeCount int             `json:"hiddenChangeCount,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	TotalChangeCount  int             `json:"totalChangeCount"`
	State             RepositoryState `json:"state"`
}

type DiffSide struct {
	Label   string `json:"label"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
	EOL     string `json:"eol"`
	HasBOM  bool   `json:"hasBom,omitempty"`
}

type DiffTarget struct {
	Kind    string `json:"kind"`
	GroupID string `json:"groupId,omitempty"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	BaseRef string `json:"baseRef,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

type DiffDocument struct {
	RepositoryID      string               `json:"repositoryId"`
	ProviderID        string               `json:"providerId"`
	Target            DiffTarget           `json:"target"`
	Ref               *workspacefs.FileRef `json:"ref,omitempty"`
	Revision          uint64               `json:"revision"`
	ModifiedRevision  string               `json:"modifiedRevision,omitempty"`
	Original          DiffSide             `json:"original"`
	Modified          DiffSide             `json:"modified"`
	Editable          bool                 `json:"editable"`
	Kind              string               `json:"kind"`
	UnavailableReason string               `json:"unavailableReason,omitempty"`
}

type Branch struct {
	Name    string `json:"name"`
	Current bool   `json:"current,omitempty"`
	Remote  bool   `json:"remote,omitempty"`
	Closed  bool   `json:"closed,omitempty"`
}

type Remote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl,omitempty"`
	PushURL  string `json:"pushUrl,omitempty"`
}

type Stash struct {
	Ref     string `json:"ref"`
	Hash    string `json:"hash,omitempty"`
	Message string `json:"message"`
}

type Metadata struct {
	Branches       []Branch `json:"branches"`
	RemoteBranches []Branch `json:"remoteBranches"`
	Remotes        []Remote `json:"remotes"`
	Tags           []string `json:"tags"`
	Stashes        []Stash  `json:"stashes"`
}

type Commit struct {
	Hash       string   `json:"hash"`
	Parents    []string `json:"parents"`
	Author     string   `json:"author"`
	AuthoredAt string   `json:"authoredAt"`
	Refs       []string `json:"refs"`
	Subject    string   `json:"subject"`
}

type History struct {
	Commits    []Commit `json:"commits"`
	NextOffset int      `json:"nextOffset,omitempty"`
	HasMore    bool     `json:"hasMore"`
}

type RevisionFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
}

type RevisionDetail struct {
	Ref   string         `json:"ref"`
	Files []RevisionFile `json:"files"`
}

type Annotation struct {
	RepositoryID string `json:"repositoryId"`
	ProviderID   string `json:"providerId"`
	Revision     string `json:"revision"`
	Path         string `json:"path"`
	StartLine    int    `json:"startLine,omitempty"`
	EndLine      int    `json:"endLine,omitempty"`
	Text         string `json:"text"`
	Truncated    bool   `json:"truncated,omitempty"`
}

// ActionRequest deliberately has a small common envelope. Each provider
// validates the fields used by its registered action IDs.
type ActionRequest struct {
	RequestID        string   `json:"requestId"`
	Action           string   `json:"action"`
	ExpectedRevision uint64   `json:"expectedRevision,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	Message          string   `json:"message,omitempty"`
	Ref              string   `json:"ref,omitempty"`
	StartPoint       string   `json:"startPoint,omitempty"`
	Name             string   `json:"name,omitempty"`
	Remote           string   `json:"remote,omitempty"`
	Branch           string   `json:"branch,omitempty"`
	URL              string   `json:"url,omitempty"`
	Confirmed        bool     `json:"confirmed,omitempty"`
}

type ActionResult struct {
	RequestID     string   `json:"requestId"`
	RepositoryID  string   `json:"repositoryId"`
	Revision      uint64   `json:"revision"`
	AffectedPaths []string `json:"affectedPaths,omitempty"`
	TrashIDs      []string `json:"trashIds,omitempty"`
}

type Operation struct {
	WorkspaceID  string `json:"workspaceId"`
	RepositoryID string `json:"repositoryId"`
	ProviderID   string `json:"providerId"`
	RequestID    string `json:"requestId"`
	Action       string `json:"action"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

type Event struct {
	Type         string          `json:"type"`
	WorkspaceID  string          `json:"workspaceId"`
	RepositoryID string          `json:"repositoryId,omitempty"`
	ProviderID   string          `json:"providerId,omitempty"`
	Status       *StatusSnapshot `json:"status,omitempty"`
	Operation    *Operation      `json:"operation,omitempty"`
}
