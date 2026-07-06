package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const fileChangesEventName = "echo:file-changes:event"

type WorkspaceChangeSource struct {
	Type       string `json:"type"`
	CardID     string `json:"cardId,omitempty"`
	CardTitle  string `json:"cardTitle,omitempty"`
	MessageID  string `json:"messageId,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
}

type WorkspaceFileSnapshot struct {
	Path          string `json:"path"`
	Exists        bool   `json:"exists"`
	Bytes         int64  `json:"bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	TextAvailable bool   `json:"textAvailable,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Large         bool   `json:"large,omitempty"`
}

type WorkspaceFileChange struct {
	ID          string                 `json:"id"`
	WorkspaceID string                 `json:"workspaceId"`
	Path        string                 `json:"path"`
	Operation   string                 `json:"operation"`
	Source      WorkspaceChangeSource  `json:"source"`
	Before      *WorkspaceFileSnapshot `json:"before,omitempty"`
	After       *WorkspaceFileSnapshot `json:"after,omitempty"`
	CreatedAt   string                 `json:"createdAt"`
}

type WorkspaceChangedFile struct {
	Path          string                  `json:"path"`
	Operation     string                  `json:"operation"`
	Diff          string                  `json:"diff,omitempty"`
	DiffAvailable bool                    `json:"diffAvailable"`
	Before        *WorkspaceFileSnapshot  `json:"before,omitempty"`
	After         *WorkspaceFileSnapshot  `json:"after,omitempty"`
	Sources       []WorkspaceChangeSource `json:"sources,omitempty"`
	ChangeCount   int                     `json:"changeCount"`
}

type WorkspaceChangeReview struct {
	WorkspaceID string                 `json:"workspaceId"`
	FileCount   int                    `json:"fileCount"`
	ChangeCount int                    `json:"changeCount"`
	Files       []WorkspaceChangedFile `json:"files"`
	Changes     []WorkspaceFileChange  `json:"changes,omitempty"`
}

type FileChangesEvent struct {
	WorkspaceID string `json:"workspaceId"`
	Type        string `json:"type"`
	FileCount   int    `json:"fileCount"`
	ChangeCount int    `json:"changeCount"`
}

type trackedFileChange struct {
	ID          string
	WorkspaceID string
	Path        string
	Operation   string
	Source      WorkspaceChangeSource
	Before      *tools.FileSnapshot
	After       *tools.FileSnapshot
	CreatedAt   time.Time
}

type toolExecution struct {
	Result  tools.ExecutionResult
	Changes []tools.FileChange
}

func workspaceToolRoots(workspace Workspace) []tools.WorkspaceRoot {
	roots := make([]tools.WorkspaceRoot, 0, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		roots = append(roots, tools.WorkspaceRoot{
			ID:    folder.ID,
			Label: folder.Label,
			Path:  folder.Path,
		})
	}
	return roots
}

func (s *SystemService) LoadWorkspaceChangeReview(workspaceID string) (WorkspaceChangeReview, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return WorkspaceChangeReview{}, err
	}
	return s.workspaceChangeReview(workspaceID), nil
}

func (s *SystemService) ClearWorkspaceChangeReview(workspaceID string) (WorkspaceChangeReview, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return WorkspaceChangeReview{}, err
	}
	s.fileChangeMu.Lock()
	delete(s.fileChanges, workspaceID)
	s.fileChangeMu.Unlock()
	review := s.workspaceChangeReview(workspaceID)
	s.emitFileChangesEvent(FileChangesEvent{
		WorkspaceID: workspaceID,
		Type:        "cleared",
		FileCount:   review.FileCount,
		ChangeCount: review.ChangeCount,
	})
	return review, nil
}

func (s *SystemService) executeTrackedToolCall(ctx context.Context, workspace Workspace, settings llm.Settings, call llm.ToolCall, source WorkspaceChangeSource, emit tools.EventEmitter, toolScopes *tools.ToolScopeChecker) toolExecution {
	source.ToolCallID = call.ID
	source.ToolName = call.Function.Name

	var captured []tools.FileChange
	sink := func(changes []tools.FileChange) {
		captured = append(captured, changes...)
	}

	unlock := func() {}
	if tools.IsMutatingToolName(call.Function.Name) {
		lock := s.workspaceToolLock(workspace.ID)
		lock.Lock()
		unlock = lock.Unlock
	}
	result := tools.Execute(tools.ExecutionContext{
		Context:          ctx,
		WorkspaceRoots:   workspaceToolRoots(workspace),
		SearxngURL:       settings.SearxngURL,
		CodeNavigator:    s.codeNavigator(workspace),
		WorkspaceContext: s.workspaceContextProvider(workspace),
		WorkspaceSkills:  s.workspaceSkillsProvider(workspace),
		Emit:             emit,
		FileChanges:      sink,
		ToolScopes:       toolScopes,
		AgentModes:       s,
	}, call.Function.Name, json.RawMessage(call.Function.Arguments))

	if len(captured) > 0 {
		s.recordToolFileChanges(workspace.ID, source, captured)
	}
	unlock()
	return toolExecution{Result: result, Changes: captured}
}

func (s *SystemService) workspaceToolLock(workspaceID string) *sync.Mutex {
	s.fileChangeMu.Lock()
	defer s.fileChangeMu.Unlock()
	lock := s.workspaceToolLocks[workspaceID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.workspaceToolLocks[workspaceID] = lock
	}
	return lock
}

func (s *SystemService) recordToolFileChanges(workspaceID string, source WorkspaceChangeSource, changes []tools.FileChange) {
	if len(changes) == 0 {
		return
	}
	ignoredPaths := ignoredWorkspaceChangePaths(s.workspaceSnapshot(workspaceID), changes)
	s.fileChangeMu.Lock()
	now := time.Now().UTC()
	accepted := false
	for _, change := range changes {
		path := cleanChangePath(change.Path)
		if path == "" || tools.IsIgnoredChangePath(path) || ignoredPaths[path] {
			continue
		}
		accepted = true
		s.fileChangeSeq++
		id := fmt.Sprintf("change-%d", s.fileChangeSeq)
		s.fileChanges[workspaceID] = append(s.fileChanges[workspaceID], trackedFileChange{
			ID:          id,
			WorkspaceID: workspaceID,
			Path:        path,
			Operation:   change.Operation,
			Source:      source,
			Before:      cloneToolSnapshot(change.Before),
			After:       cloneToolSnapshot(change.After),
			CreatedAt:   now,
		})
	}
	review := s.workspaceChangeReviewLocked(workspaceID)
	s.fileChangeMu.Unlock()
	if accepted {
		s.removeWorkspaceFileDatabases(workspaceID)
	}

	s.emitFileChangesEvent(FileChangesEvent{
		WorkspaceID: workspaceID,
		Type:        "updated",
		FileCount:   review.FileCount,
		ChangeCount: review.ChangeCount,
	})
}

func (s *SystemService) workspaceSnapshot(workspaceID string) Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == workspaceID {
			return workspace
		}
	}
	return Workspace{}
}

func (s *SystemService) workspaceChangeReview(workspaceID string) WorkspaceChangeReview {
	s.fileChangeMu.Lock()
	defer s.fileChangeMu.Unlock()
	return s.workspaceChangeReviewLocked(workspaceID)
}

func (s *SystemService) workspaceChangeReviewLocked(workspaceID string) WorkspaceChangeReview {
	changes := s.fileChanges[workspaceID]
	publicChanges := make([]WorkspaceFileChange, 0, len(changes))
	for _, change := range changes {
		publicChanges = append(publicChanges, publicTrackedFileChange(change))
	}

	filesByPath := map[string]*consolidatedWorkspaceFile{}
	for _, change := range changes {
		file := filesByPath[change.Path]
		if file == nil {
			file = &consolidatedWorkspaceFile{path: change.Path, before: cloneToolSnapshot(change.Before)}
			filesByPath[change.Path] = file
		}
		file.after = cloneToolSnapshot(change.After)
		file.sources = append(file.sources, change.Source)
		file.changeCount++
	}

	files := make([]WorkspaceChangedFile, 0, len(filesByPath))
	for _, file := range filesByPath {
		changed := consolidatedChangedFile(*file)
		if changed.Operation == "" {
			continue
		}
		files = append(files, changed)
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	return WorkspaceChangeReview{
		WorkspaceID: workspaceID,
		FileCount:   len(files),
		ChangeCount: len(changes),
		Files:       files,
		Changes:     publicChanges,
	}
}

type consolidatedWorkspaceFile struct {
	path        string
	before      *tools.FileSnapshot
	after       *tools.FileSnapshot
	sources     []WorkspaceChangeSource
	changeCount int
}

func consolidatedChangedFile(file consolidatedWorkspaceFile) WorkspaceChangedFile {
	operation := consolidatedOperation(file.before, file.after)
	if operation == "" {
		return WorkspaceChangedFile{}
	}
	diffChange := tools.FileChange{
		Path:      file.path,
		Operation: operation,
		Before:    file.before,
		After:     file.after,
	}
	diff := tools.UnifiedDiff(diffChange)
	return WorkspaceChangedFile{
		Path:          file.path,
		Operation:     operation,
		Diff:          diff,
		DiffAvailable: diff != "",
		Before:        publicSnapshot(file.before),
		After:         publicSnapshot(file.after),
		Sources:       uniqueChangeSources(file.sources),
		ChangeCount:   file.changeCount,
	}
}

func consolidatedOperation(before *tools.FileSnapshot, after *tools.FileSnapshot) string {
	switch {
	case before == nil && after != nil:
		return tools.FileChangeCreated
	case before != nil && after == nil:
		return tools.FileChangeDeleted
	case before != nil && after != nil && before.SHA256 != after.SHA256:
		return tools.FileChangeEdited
	default:
		return ""
	}
}

func publicTrackedFileChange(change trackedFileChange) WorkspaceFileChange {
	return WorkspaceFileChange{
		ID:          change.ID,
		WorkspaceID: change.WorkspaceID,
		Path:        change.Path,
		Operation:   change.Operation,
		Source:      change.Source,
		Before:      publicSnapshot(change.Before),
		After:       publicSnapshot(change.After),
		CreatedAt:   change.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func publicSnapshot(snapshot *tools.FileSnapshot) *WorkspaceFileSnapshot {
	if snapshot == nil {
		return nil
	}
	return &WorkspaceFileSnapshot{
		Path:          snapshot.Path,
		Exists:        snapshot.Exists,
		Bytes:         snapshot.Bytes,
		SHA256:        snapshot.SHA256,
		TextAvailable: snapshot.TextAvailable,
		Binary:        snapshot.Binary,
		Large:         snapshot.Large,
	}
}

func cloneToolSnapshot(snapshot *tools.FileSnapshot) *tools.FileSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	return &clone
}

func uniqueChangeSources(sources []WorkspaceChangeSource) []WorkspaceChangeSource {
	if len(sources) == 0 {
		return nil
	}
	output := make([]WorkspaceChangeSource, 0, len(sources))
	seen := map[string]bool{}
	for _, source := range sources {
		key := strings.Join([]string{
			source.Type,
			source.CardID,
			source.MessageID,
			source.RequestID,
			source.ToolCallID,
			source.ToolName,
		}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		output = append(output, source)
	}
	return output
}

func affectedPathsFromChanges(changes []tools.FileChange) []string {
	if len(changes) == 0 {
		return nil
	}
	paths := map[string]bool{}
	for _, change := range changes {
		if strings.TrimSpace(change.Path) != "" {
			paths[change.Path] = true
		}
	}
	output := make([]string, 0, len(paths))
	for path := range paths {
		output = append(output, path)
	}
	sort.Strings(output)
	return output
}

func (s *SystemService) dropWorkspaceChangeReview(workspaceID string) {
	s.fileChangeMu.Lock()
	delete(s.fileChanges, workspaceID)
	delete(s.workspaceToolLocks, workspaceID)
	s.fileChangeMu.Unlock()
}

func (s *SystemService) emitFileChangesEvent(event FileChangesEvent) {
	s.emitRuntimeEvent(fileChangesEventName, event)
	if s.fileChangesEventSink != nil {
		s.fileChangesEventSink(event)
	}
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, fileChangesEventName, event)
	}
}
