// Package gitservice implements Echo's confined, process-backed Git model.
// Browser callers use opaque repository IDs and repository-relative paths;
// absolute filesystem paths never cross the public API boundary.
package gitservice

import (
	"errors"

	"github.com/brent/echo/internal/workspacefs"
)

const (
	StatusLimit     = 10_000
	HistoryPageSize = 100
)

var (
	ErrNotFound    = errors.New("Git repository not found")
	ErrInvalidPath = errors.New("invalid Git path")
)

// Error is a safe, stable failure returned to the HTTP layer.
type Error struct {
	Code    string
	Message string
	Cause   error
	Details any
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type Scope struct {
	RootID     string `json:"rootId"`
	RootLabel  string `json:"rootLabel"`
	RepoPrefix string `json:"repoPrefix"`
}

type Repository struct {
	ID       string               `json:"id"`
	Label    string               `json:"label"`
	RootRef  *workspacefs.FileRef `json:"rootRef,omitempty"`
	Parent   bool                 `json:"parent"`
	Scopes   []Scope              `json:"scopes"`
	Revision uint64               `json:"revision"`
}

type Change struct {
	Path           string               `json:"path"`
	OldPath        string               `json:"oldPath,omitempty"`
	Ref            *workspacefs.FileRef `json:"ref,omitempty"`
	Status         string               `json:"status"`
	StatusCode     string               `json:"statusCode"`
	IndexStatus    string               `json:"indexStatus,omitempty"`
	WorktreeStatus string               `json:"worktreeStatus,omitempty"`
	Scope          string               `json:"scope"`
	Submodule      bool                 `json:"submodule,omitempty"`
}

type RepositoryState struct {
	MergeInProgress      bool `json:"mergeInProgress,omitempty"`
	RebaseInProgress     bool `json:"rebaseInProgress,omitempty"`
	CherryPickInProgress bool `json:"cherryPickInProgress,omitempty"`
}

type StatusSnapshot struct {
	WorkspaceID       string          `json:"workspaceId"`
	RepositoryID      string          `json:"repositoryId"`
	Revision          uint64          `json:"revision"`
	Branch            string          `json:"branch"`
	Head              string          `json:"head,omitempty"`
	Detached          bool            `json:"detached"`
	Upstream          string          `json:"upstream,omitempty"`
	Ahead             int             `json:"ahead"`
	Behind            int             `json:"behind"`
	StashCount        int             `json:"stashCount,omitempty"`
	Conflicts         []Change        `json:"conflicts"`
	Staged            []Change        `json:"staged"`
	Unstaged          []Change        `json:"unstaged"`
	HiddenStagedCount int             `json:"hiddenStagedCount,omitempty"`
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

type DiffDocument struct {
	RepositoryID      string               `json:"repositoryId"`
	Scope             string               `json:"scope"`
	Path              string               `json:"path"`
	OldPath           string               `json:"oldPath,omitempty"`
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
}

type Remote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl,omitempty"`
	PushURL  string `json:"pushUrl,omitempty"`
}

type Stash struct {
	Ref     string `json:"ref"`
	Hash    string `json:"hash"`
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

type CommitFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
}

type CommitDetail struct {
	Ref   string       `json:"ref"`
	Files []CommitFile `json:"files"`
}

type Annotation struct {
	Revision  string
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Truncated bool
}

// ActionRequest is decoded once and then validated according to Action. The
// frontend exposes it as a discriminated union even though Go uses one wire
// struct for compact handler code.
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

type CloneRequest struct {
	URL         string `json:"url"`
	RootID      string `json:"rootId"`
	Destination string `json:"destination"`
}

type InitRequest struct {
	RootID string `json:"rootId"`
	Path   string `json:"path,omitempty"`
}

type Operation struct {
	WorkspaceID  string `json:"workspaceId"`
	RepositoryID string `json:"repositoryId"`
	RequestID    string `json:"requestId"`
	Action       string `json:"action"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

type Event struct {
	Type         string          `json:"type"`
	WorkspaceID  string          `json:"workspaceId"`
	RepositoryID string          `json:"repositoryId,omitempty"`
	Status       *StatusSnapshot `json:"status,omitempty"`
	Operation    *Operation      `json:"operation,omitempty"`
}
