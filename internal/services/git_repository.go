package services

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brent/echo/internal/tools"
)

const (
	workspaceGitHistoryLimit = 100
	workspaceGitSyncTimeout  = 2 * time.Minute
)

type WorkspaceGitRepositoryView struct {
	WorkspaceID      string                          `json:"workspaceId"`
	SelectedFolderID string                          `json:"selectedFolderId,omitempty"`
	Repositories     []WorkspaceGitRepositorySummary `json:"repositories"`
	Repository       *WorkspaceGitRepositoryStatus   `json:"repository,omitempty"`
}

type WorkspaceGitRepositorySummary struct {
	FolderID      string `json:"folderId"`
	Label         string `json:"label"`
	Path          string `json:"path"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	Upstream      string `json:"upstream,omitempty"`
	AheadCount    int    `json:"aheadCount"`
	BehindCount   int    `json:"behindCount"`
	Head          string `json:"head,omitempty"`
	ShortHead     string `json:"shortHead,omitempty"`
	Detached      bool   `json:"detached"`
	Dirty         bool   `json:"dirty"`
	Available     bool   `json:"available"`
	Error         string `json:"error,omitempty"`
}

type WorkspaceGitRepositoryStatus struct {
	FolderID          string                    `json:"folderId"`
	Label             string                    `json:"label"`
	Path              string                    `json:"path"`
	CurrentBranch     string                    `json:"currentBranch,omitempty"`
	Upstream          string                    `json:"upstream,omitempty"`
	AheadCount        int                       `json:"aheadCount"`
	BehindCount       int                       `json:"behindCount"`
	Head              string                    `json:"head,omitempty"`
	ShortHead         string                    `json:"shortHead,omitempty"`
	Detached          bool                      `json:"detached"`
	Dirty             bool                      `json:"dirty"`
	Branches          []WorkspaceGitBranch      `json:"branches"`
	FileCount         int                       `json:"fileCount"`
	StagedFileCount   int                       `json:"stagedFileCount"`
	UnstagedFileCount int                       `json:"unstagedFileCount"`
	Files             []WorkspaceGitChangedFile `json:"files"`
	Commits           []WorkspaceGitCommit      `json:"commits"`
}

type WorkspaceGitBranch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

type WorkspaceGitCommit struct {
	Hash        string `json:"hash"`
	ShortHash   string `json:"shortHash"`
	Subject     string `json:"subject"`
	AuthorName  string `json:"authorName"`
	AuthorEmail string `json:"authorEmail,omitempty"`
	AuthoredAt  string `json:"authoredAt"`
}

type WorkspaceGitCommitDetail struct {
	WorkspaceID string                    `json:"workspaceId"`
	FolderID    string                    `json:"folderId"`
	Commit      WorkspaceGitCommit        `json:"commit"`
	FileCount   int                       `json:"fileCount"`
	Files       []WorkspaceGitChangedFile `json:"files"`
}

type gitNameStatusEntry struct {
	status  string
	path    string
	oldPath string
}

type workspaceGitRepositorySnapshot struct {
	context workspaceGitRepositoryContext
	summary WorkspaceGitRepositorySummary
	files   []WorkspaceGitChangedFile
}

func (s *SystemService) LoadWorkspaceGitRepository(workspaceID string, folderID string) (WorkspaceGitRepositoryView, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folderID)
}

func (s *SystemService) LoadWorkspaceGitCommit(workspaceID string, folderID string, hash string) (WorkspaceGitCommitDetail, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return WorkspaceGitCommitDetail{}, fmt.Errorf("commit hash is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}

	canonical, err := workspaceGitCommitHash(ctx, repository.WorktreePath, hash)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	commit, err := loadWorkspaceGitCommitMetadata(ctx, repository.WorktreePath, canonical)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	files, err := loadWorkspaceGitCommitFiles(ctx, repository, canonical)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	return WorkspaceGitCommitDetail{
		WorkspaceID: workspace.ID,
		FolderID:    folder.ID,
		Commit:      commit,
		FileCount:   len(files),
		Files:       files,
	}, nil
}

func (s *SystemService) CommitWorkspaceGitChanges(workspaceID string, folderID string, message string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if message == "" {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("commit message is required")
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if staged, err := workspaceGitRepositoryHasStagedChangesForContext(ctx, repository); err != nil {
		return WorkspaceGitRepositoryView{}, err
	} else if !staged {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("stage Git changes before committing")
	}
	if err := requireWorkspaceGitCommitIdentity(ctx, repository.WorktreePath); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommandWithInput(ctx, repository.WorktreePath, []byte(message), "commit", "-F", "-"); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) CreateWorkspaceGitBranch(workspaceID string, folderID string, name string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateWorkspaceGitBranchName(ctx, repository.WorktreePath, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "checkout", "-b", branch); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) SwitchWorkspaceGitBranch(workspaceID string, folderID string, name string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateExistingWorkspaceGitBranch(ctx, repository.WorktreePath, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := requireCleanWorkspaceGitRepository(ctx, repository.WorktreePath); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "checkout", branch); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) MergeWorkspaceGitBranch(workspaceID string, folderID string, name string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateExistingWorkspaceGitBranch(ctx, repository.WorktreePath, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := requireCleanWorkspaceGitRepository(ctx, repository.WorktreePath); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	current := strings.TrimSpace(string(mustGitOutput(ctx, repository.WorktreePath, "branch", "--show-current")))
	if current != "" && current == branch {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("cannot merge the current branch into itself")
	}
	if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "merge", "--no-edit", branch); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) SyncWorkspaceGitBranch(workspaceID string, folderID string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitSyncTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	currentBranch, detached := workspaceGitCurrentBranch(ctx, repository.WorktreePath)
	if detached || currentBranch == "" {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("cannot sync a detached Git HEAD")
	}
	upstream, ahead, behind, err := workspaceGitRemoteStatus(ctx, repository.WorktreePath)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if upstream == "" {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("current branch has no upstream configured")
	}
	if ahead == 0 && behind == 0 {
		return s.loadWorkspaceGitRepository(workspace, folder.ID)
	}
	if behind > 0 {
		if err := requireCleanWorkspaceGitRepository(ctx, repository.WorktreePath); err != nil {
			return WorkspaceGitRepositoryView{}, fmt.Errorf("commit or discard Git changes before syncing incoming commits")
		}
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "-c", "pull.rebase=false", "-c", "core.editor=true", "pull", "--no-edit"); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	}
	if ahead > 0 {
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "push"); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) loadWorkspaceGitRepository(workspace Workspace, folderID string) (WorkspaceGitRepositoryView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()

	view := WorkspaceGitRepositoryView{
		WorkspaceID:  workspace.ID,
		Repositories: make([]WorkspaceGitRepositorySummary, 0, len(workspace.Folders)),
	}
	var selectedFolder WorkspaceFolder
	var selectedSnapshot workspaceGitRepositorySnapshot
	var selectedErr error
	var selectedFound bool

	for _, folder := range workspace.Folders {
		snapshot, err := s.workspaceGitRepositorySnapshot(ctx, workspace, folder)
		summary := snapshot.summary
		if err != nil {
			summary = WorkspaceGitRepositorySummary{FolderID: folder.ID, Label: folder.Label, Path: folder.Path, Error: err.Error()}
		}
		view.Repositories = append(view.Repositories, summary)
		if folder.ID == folderID {
			selectedFolder = folder
			selectedSnapshot = snapshot
			selectedErr = err
			selectedFound = true
		}
		if strings.TrimSpace(folderID) == "" && !selectedFound && summary.Available {
			selectedFolder = folder
			selectedSnapshot = snapshot
			selectedErr = err
			selectedFound = true
		}
	}
	if strings.TrimSpace(folderID) != "" && !selectedFound {
		return view, fmt.Errorf("workspace folder was not found")
	}
	if !selectedFound {
		return view, fmt.Errorf("workspace has no manageable Git repositories")
	}
	if selectedErr != nil {
		return view, selectedErr
	}
	if !selectedSnapshot.summary.Available {
		return view, fmt.Errorf("workspace folder has no manageable Git repository")
	}
	repository, err := loadWorkspaceGitRepositoryStatus(ctx, selectedSnapshot)
	if err != nil {
		return view, err
	}
	view.SelectedFolderID = selectedFolder.ID
	view.Repository = &repository
	s.storeWorkspaceGitRepositoryView(view)
	return view, nil
}

func (s *SystemService) refreshCachedWorkspaceGitRepositoryStatus(workspace Workspace, folder WorkspaceFolder) (WorkspaceGitRepositoryView, error) {
	cached, ok := s.cachedWorkspaceGitRepositoryView(workspace.ID, folder.ID)
	if !ok || cached.Repository == nil {
		return s.loadWorkspaceGitRepository(workspace, folder.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	files, err := loadGitStatusFilesForRepository(ctx, repository)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	staged, unstaged := workspaceGitChangeStageCounts(files)
	cached.Repository.Dirty = len(files) > 0
	cached.Repository.FileCount = len(files)
	cached.Repository.StagedFileCount = staged
	cached.Repository.UnstagedFileCount = unstaged
	cached.Repository.Files = files
	for i := range cached.Repositories {
		if cached.Repositories[i].FolderID == folder.ID {
			cached.Repositories[i].Dirty = len(files) > 0
		}
	}
	s.storeWorkspaceGitRepositoryView(cached)
	return cloneWorkspaceGitRepositoryView(cached), nil
}

func workspaceGitRepositoryViewCacheKey(workspaceID string, folderID string) string {
	return workspaceID + "\x00" + folderID
}

func (s *SystemService) cachedWorkspaceGitRepositoryView(workspaceID string, folderID string) (WorkspaceGitRepositoryView, bool) {
	s.gitViewMu.Lock()
	defer s.gitViewMu.Unlock()
	view, ok := s.gitRepositoryViews[workspaceGitRepositoryViewCacheKey(workspaceID, folderID)]
	if !ok {
		return WorkspaceGitRepositoryView{}, false
	}
	return cloneWorkspaceGitRepositoryView(view), true
}

func (s *SystemService) storeWorkspaceGitRepositoryView(view WorkspaceGitRepositoryView) {
	if view.WorkspaceID == "" || view.SelectedFolderID == "" || view.Repository == nil {
		return
	}
	s.gitViewMu.Lock()
	s.gitRepositoryViews[workspaceGitRepositoryViewCacheKey(view.WorkspaceID, view.SelectedFolderID)] = cloneWorkspaceGitRepositoryView(view)
	s.gitViewMu.Unlock()
}

func cloneWorkspaceGitRepositoryView(view WorkspaceGitRepositoryView) WorkspaceGitRepositoryView {
	clone := view
	clone.Repositories = append([]WorkspaceGitRepositorySummary(nil), view.Repositories...)
	if view.Repository != nil {
		repository := *view.Repository
		repository.Branches = append([]WorkspaceGitBranch(nil), view.Repository.Branches...)
		repository.Files = append([]WorkspaceGitChangedFile(nil), view.Repository.Files...)
		repository.Commits = append([]WorkspaceGitCommit(nil), view.Repository.Commits...)
		clone.Repository = &repository
	}
	return clone
}

func (s *SystemService) workspaceGitRepositoryFolder(workspaceID string, folderID string) (Workspace, WorkspaceFolder, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return Workspace{}, WorkspaceFolder{}, err
	}
	folderID = strings.TrimSpace(folderID)
	if folderID == "" {
		return Workspace{}, WorkspaceFolder{}, fmt.Errorf("workspace folder id is required")
	}
	for _, folder := range workspace.Folders {
		if folder.ID == folderID {
			if folder.Missing {
				return Workspace{}, WorkspaceFolder{}, fmt.Errorf("workspace folder is unavailable")
			}
			return workspace, folder, nil
		}
	}
	return Workspace{}, WorkspaceFolder{}, fmt.Errorf("workspace folder was not found")
}

func (s *SystemService) workspaceGitRepositorySnapshot(ctx context.Context, workspace Workspace, folder WorkspaceFolder) (workspaceGitRepositorySnapshot, error) {
	summary := WorkspaceGitRepositorySummary{
		FolderID: folder.ID,
		Label:    folder.Label,
		Path:     folder.Path,
	}
	if folder.Missing {
		return workspaceGitRepositorySnapshot{summary: summary}, fmt.Errorf("workspace folder is unavailable")
	}
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return workspaceGitRepositorySnapshot{summary: summary}, err
	}
	summary.Available = true
	summary.CurrentBranch, summary.Detached = workspaceGitCurrentBranch(ctx, repository.WorktreePath)
	summary.Upstream, summary.AheadCount, summary.BehindCount, _ = workspaceGitRemoteStatus(ctx, repository.WorktreePath)
	summary.Head, summary.ShortHead = workspaceGitHead(ctx, repository.WorktreePath)
	files, err := loadGitStatusFilesForRepository(ctx, repository)
	if err != nil {
		return workspaceGitRepositorySnapshot{context: repository, summary: summary}, err
	}
	summary.Dirty = len(files) > 0
	return workspaceGitRepositorySnapshot{context: repository, summary: summary, files: files}, nil
}

func loadWorkspaceGitRepositoryStatus(ctx context.Context, snapshot workspaceGitRepositorySnapshot) (WorkspaceGitRepositoryStatus, error) {
	repository := snapshot.context
	summary := snapshot.summary
	branches, err := loadWorkspaceGitBranches(ctx, repository.WorktreePath, summary.CurrentBranch)
	if err != nil {
		return WorkspaceGitRepositoryStatus{}, err
	}
	files := snapshot.files
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	stagedFileCount, unstagedFileCount := workspaceGitChangeStageCounts(files)
	commits, err := loadWorkspaceGitCommitHistory(ctx, repository.WorktreePath, workspaceGitHistoryLimit)
	if err != nil {
		return WorkspaceGitRepositoryStatus{}, err
	}
	return WorkspaceGitRepositoryStatus{
		FolderID:          repository.Folder.ID,
		Label:             repository.Folder.Label,
		Path:              repository.Folder.Path,
		CurrentBranch:     summary.CurrentBranch,
		Upstream:          summary.Upstream,
		AheadCount:        summary.AheadCount,
		BehindCount:       summary.BehindCount,
		Head:              summary.Head,
		ShortHead:         summary.ShortHead,
		Detached:          summary.Detached,
		Dirty:             len(files) > 0,
		Branches:          branches,
		FileCount:         len(files),
		StagedFileCount:   stagedFileCount,
		UnstagedFileCount: unstagedFileCount,
		Files:             files,
		Commits:           commits,
	}, nil
}

func workspaceGitChangeStageCounts(files []WorkspaceGitChangedFile) (int, int) {
	staged := 0
	unstaged := 0
	for _, file := range files {
		if file.Staged {
			staged++
		}
		if file.Unstaged {
			unstaged++
		}
	}
	return staged, unstaged
}

func normalizedGitPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is empty")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = real
	}
	return filepath.Clean(absolute), nil
}

func workspaceGitCurrentBranch(ctx context.Context, workspacePath string) (string, bool) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "branch", "--show-current")
	if err != nil {
		return "", false
	}
	branch := strings.TrimSpace(string(output))
	return branch, branch == ""
}

func workspaceGitHead(ctx context.Context, workspacePath string) (string, string) {
	headOutput, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-parse", "HEAD")
	if err != nil {
		return "", ""
	}
	head := strings.TrimSpace(string(headOutput))
	shortHead := head
	if len(shortHead) > 7 {
		shortHead = shortHead[:7]
	}
	return head, shortHead
}

func workspaceGitRemoteStatus(ctx context.Context, workspacePath string) (string, int, int, error) {
	upstreamOutput, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		if workspaceGitHasNoUpstream(err) {
			return "", 0, 0, nil
		}
		return "", 0, 0, err
	}
	upstream := strings.TrimSpace(string(upstreamOutput))
	if upstream == "" {
		return "", 0, 0, nil
	}
	countOutput, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		if workspaceGitHasNoUpstream(err) {
			return "", 0, 0, nil
		}
		return "", 0, 0, err
	}
	fields := strings.Fields(string(countOutput))
	if len(fields) != 2 {
		return upstream, 0, 0, fmt.Errorf("parse Git upstream status: expected ahead and behind counts")
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return upstream, 0, 0, fmt.Errorf("parse Git ahead count: %w", err)
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return upstream, 0, 0, fmt.Errorf("parse Git behind count: %w", err)
	}
	return upstream, ahead, behind, nil
}

func workspaceGitRepositoryDirty(ctx context.Context, workspacePath string) (bool, error) {
	status, err := runWorkspaceGitCommand(ctx, workspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return len(status) > 0, nil
}

func workspaceGitRepositoryHasStagedChangesForContext(ctx context.Context, repository workspaceGitRepositoryContext) (bool, error) {
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if gitStatusEntryHasStagedChanges(entry) {
			return true, nil
		}
	}
	return false, nil
}

func requireCleanWorkspaceGitRepository(ctx context.Context, workspacePath string) error {
	dirty, err := workspaceGitRepositoryDirty(ctx, workspacePath)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("commit or discard Git changes before switching or merging branches")
	}
	return nil
}

func requireWorkspaceGitCommitIdentity(ctx context.Context, workspacePath string) error {
	name, nameErr := runWorkspaceGitCommand(ctx, workspacePath, "config", "--get", "user.name")
	email, emailErr := runWorkspaceGitCommand(ctx, workspacePath, "config", "--get", "user.email")
	if nameErr != nil || emailErr != nil || strings.TrimSpace(string(name)) == "" || strings.TrimSpace(string(email)) == "" {
		return fmt.Errorf("configure Git user.name and user.email before committing")
	}
	return nil
}

func loadWorkspaceGitBranches(ctx context.Context, workspacePath string, currentBranch string) ([]WorkspaceGitBranch, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	branches := make([]WorkspaceGitBranch, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		branches = append(branches, WorkspaceGitBranch{
			Name:    name,
			Current: name == currentBranch,
		})
	}
	sort.Slice(branches, func(i, j int) bool {
		if branches[i].Current != branches[j].Current {
			return branches[i].Current
		}
		return strings.ToLower(branches[i].Name) < strings.ToLower(branches[j].Name)
	})
	return branches, nil
}

func loadWorkspaceGitCommitHistory(ctx context.Context, workspacePath string, limit int) ([]WorkspaceGitCommit, error) {
	if limit <= 0 {
		limit = workspaceGitHistoryLimit
	}
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "log", fmt.Sprintf("-n%d", limit), "--date=iso-strict", "--format=%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x1e")
	if err != nil {
		if gitRepositoryHasNoCommits(err) {
			return []WorkspaceGitCommit{}, nil
		}
		return nil, err
	}
	return parseWorkspaceGitCommitLog(output), nil
}

func parseWorkspaceGitCommitLog(output []byte) []WorkspaceGitCommit {
	records := strings.Split(string(output), "\x1e")
	commits := make([]WorkspaceGitCommit, 0, len(records))
	for _, record := range records {
		record = strings.Trim(record, "\r\n")
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x00", 6)
		if len(parts) != 6 {
			continue
		}
		commits = append(commits, WorkspaceGitCommit{
			Hash:        parts[0],
			ShortHash:   parts[1],
			AuthorName:  parts[2],
			AuthorEmail: parts[3],
			AuthoredAt:  parts[4],
			Subject:     parts[5],
		})
	}
	return commits
}

func workspaceGitCommitHash(ctx context.Context, workspacePath string, hash string) (string, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-parse", "--verify", hash+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func loadWorkspaceGitCommitMetadata(ctx context.Context, workspacePath string, hash string) (WorkspaceGitCommit, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "log", "-1", "--date=iso-strict", "--format=%H%x00%h%x00%an%x00%ae%x00%aI%x00%s", hash)
	if err != nil {
		return WorkspaceGitCommit{}, err
	}
	commits := parseWorkspaceGitCommitLog(append(output, 0x1e))
	if len(commits) == 0 {
		return WorkspaceGitCommit{}, fmt.Errorf("commit was not found")
	}
	return commits[0], nil
}

func loadWorkspaceGitCommitFiles(ctx context.Context, repository workspaceGitRepositoryContext, hash string) ([]WorkspaceGitChangedFile, error) {
	parent, hasParent, err := workspaceGitFirstParent(ctx, repository.WorktreePath, hash)
	if err != nil {
		return nil, err
	}
	var output []byte
	if hasParent {
		output, err = runWorkspaceGitCommand(ctx, repository.WorktreePath, "diff", "--name-status", "-z", "--find-renames", parent, hash)
	} else {
		output, err = runWorkspaceGitCommand(ctx, repository.WorktreePath, "diff-tree", "--root", "--no-commit-id", "--name-status", "-z", "-r", "--find-renames", hash)
	}
	if err != nil {
		return nil, err
	}
	entries, err := parseGitNameStatus(output)
	if err != nil {
		return nil, err
	}
	files := make([]WorkspaceGitChangedFile, 0, len(entries))
	for _, entry := range entries {
		if _, ok := repository.gitPathInFolder(entry.path); !ok {
			continue
		}
		file := WorkspaceGitChangedFile{
			Path:      repository.labeledGitPath(entry.path),
			OldPath:   repository.labeledGitPath(entry.oldPath),
			Operation: gitNameStatusOperation(entry.status),
			Status:    entry.status,
		}
		diffPath := entry.path
		if file.Operation == tools.FileChangeDeleted && entry.oldPath != "" {
			diffPath = entry.oldPath
		}
		var diff string
		if hasParent {
			diff, err = loadWorkspaceGitCommitDiffForPath(ctx, repository.WorktreePath, parent, hash, diffPath)
		} else {
			diff, err = loadWorkspaceGitRootCommitDiffForPath(ctx, repository.WorktreePath, hash, diffPath)
		}
		if err == nil && strings.TrimSpace(diff) != "" {
			file.Diff = prefixGitDiffPathsForRepository(diff, repository)
			file.DiffAvailable = true
		}
		files = append(files, file)
	}
	return files, nil
}

func workspaceGitFirstParent(ctx context.Context, workspacePath string, hash string) (string, bool, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-list", "--parents", "-n", "1", hash)
	if err != nil {
		return "", false, err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return "", false, nil
	}
	return fields[1], true, nil
}

func loadWorkspaceGitCommitDiffForPath(ctx context.Context, workspacePath string, parent string, hash string, path string) (string, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "diff", "--no-ext-diff", "--no-color", "--find-renames", parent, hash, "--", path)
	if err != nil {
		return "", err
	}
	return normalizeGitDiff(output), nil
}

func loadWorkspaceGitRootCommitDiffForPath(ctx context.Context, workspacePath string, hash string, path string) (string, error) {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "diff-tree", "--root", "-p", "--no-commit-id", "--no-ext-diff", "--no-color", "--find-renames", hash, "--", path)
	if err != nil {
		return "", err
	}
	return normalizeGitDiff(output), nil
}

func parseGitNameStatus(output []byte) ([]gitNameStatusEntry, error) {
	if len(output) == 0 {
		return nil, nil
	}
	records := strings.Split(string(output), "\x00")
	entries := make([]gitNameStatusEntry, 0, len(records))
	for i := 0; i < len(records); i++ {
		status := strings.TrimSpace(records[i])
		if status == "" {
			continue
		}
		if i+1 >= len(records) || records[i+1] == "" {
			return nil, fmt.Errorf("parse git name-status: missing path")
		}
		entry := gitNameStatusEntry{status: status}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			entry.oldPath = records[i+1]
			i++
			if i+1 >= len(records) || records[i+1] == "" {
				return nil, fmt.Errorf("parse git name-status: missing target path")
			}
			entry.path = records[i+1]
			i++
		} else {
			entry.path = records[i+1]
			i++
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func gitNameStatusOperation(status string) string {
	if status == "" {
		return tools.FileChangeEdited
	}
	switch status[0] {
	case 'A':
		return tools.FileChangeCreated
	case 'D':
		return tools.FileChangeDeleted
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return tools.FileChangeEdited
	}
}

func validateWorkspaceGitBranchName(ctx context.Context, workspacePath string, name string) (string, error) {
	branch := strings.TrimSpace(name)
	if branch == "" {
		return "", fmt.Errorf("branch name is required")
	}
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, "\n") || strings.Contains(branch, "\r") || strings.Contains(branch, "\x00") || strings.Contains(branch, "@{") {
		return "", fmt.Errorf("branch name is invalid")
	}
	output, err := runWorkspaceGitCommand(ctx, workspacePath, "check-ref-format", "--branch", branch)
	if err != nil {
		return "", fmt.Errorf("branch name is invalid")
	}
	normalized := strings.TrimSpace(string(output))
	if normalized == "" {
		normalized = branch
	}
	return normalized, nil
}

func validateExistingWorkspaceGitBranch(ctx context.Context, workspacePath string, name string) (string, error) {
	branch := strings.TrimSpace(name)
	if branch == "" {
		return "", fmt.Errorf("branch name is required")
	}
	branches, err := loadWorkspaceGitBranches(ctx, workspacePath, "")
	if err != nil {
		return "", err
	}
	for _, candidate := range branches {
		if candidate.Name == branch {
			return branch, nil
		}
	}
	return "", fmt.Errorf("branch was not found")
}

func gitRepositoryHasNoCommits(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not have any commits") ||
		strings.Contains(message, "your current branch") ||
		strings.Contains(message, "bad default revision")
}

func workspaceGitHasNoUpstream(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no upstream configured") ||
		strings.Contains(message, "no upstream branch") ||
		strings.Contains(message, "no such ref") ||
		strings.Contains(message, "unknown revision") && strings.Contains(message, "@{upstream}")
}

func mustGitOutput(ctx context.Context, workspacePath string, args ...string) []byte {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
	if err != nil {
		return nil
	}
	return output
}
