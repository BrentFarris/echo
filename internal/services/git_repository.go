package services

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brent/echo/internal/tools"
)

const workspaceGitHistoryLimit = 100

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
	Head          string `json:"head,omitempty"`
	ShortHead     string `json:"shortHead,omitempty"`
	Detached      bool   `json:"detached"`
	Dirty         bool   `json:"dirty"`
	Available     bool   `json:"available"`
	Error         string `json:"error,omitempty"`
}

type WorkspaceGitRepositoryStatus struct {
	FolderID      string                    `json:"folderId"`
	Label         string                    `json:"label"`
	Path          string                    `json:"path"`
	CurrentBranch string                    `json:"currentBranch,omitempty"`
	Head          string                    `json:"head,omitempty"`
	ShortHead     string                    `json:"shortHead,omitempty"`
	Detached      bool                      `json:"detached"`
	Dirty         bool                      `json:"dirty"`
	Branches      []WorkspaceGitBranch      `json:"branches"`
	FileCount     int                       `json:"fileCount"`
	Files         []WorkspaceGitChangedFile `json:"files"`
	Commits       []WorkspaceGitCommit      `json:"commits"`
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitCommitDetail{}, err
	}

	canonical, err := workspaceGitCommitHash(ctx, folder.Path, hash)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	commit, err := loadWorkspaceGitCommitMetadata(ctx, folder.Path, canonical)
	if err != nil {
		return WorkspaceGitCommitDetail{}, err
	}
	files, err := loadWorkspaceGitCommitFiles(ctx, folder, canonical)
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if dirty, err := workspaceGitRepositoryDirty(ctx, folder.Path); err != nil {
		return WorkspaceGitRepositoryView{}, err
	} else if !dirty {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("there are no Git changes to commit")
	}
	if err := requireWorkspaceGitCommitIdentity(ctx, folder.Path); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "add", "-A"); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommandWithInput(ctx, folder.Path, []byte(message), "commit", "-F", "-"); err != nil {
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateWorkspaceGitBranchName(ctx, folder.Path, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "checkout", "-b", branch); err != nil {
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateExistingWorkspaceGitBranch(ctx, folder.Path, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := requireCleanWorkspaceGitRepository(ctx, folder.Path); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "checkout", branch); err != nil {
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	branch, err := validateExistingWorkspaceGitBranch(ctx, folder.Path, name)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := requireCleanWorkspaceGitRepository(ctx, folder.Path); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	current := strings.TrimSpace(string(mustGitOutput(ctx, folder.Path, "branch", "--show-current")))
	if current != "" && current == branch {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("cannot merge the current branch into itself")
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "merge", "--no-edit", branch); err != nil {
		return WorkspaceGitRepositoryView{}, err
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
	var selectedFound bool

	for _, folder := range workspace.Folders {
		summary := workspaceGitRepositorySummary(ctx, folder)
		view.Repositories = append(view.Repositories, summary)
		if folder.ID == folderID {
			selectedFolder = folder
			selectedFound = true
		}
		if strings.TrimSpace(folderID) == "" && !selectedFound && summary.Available {
			selectedFolder = folder
			selectedFound = true
		}
	}
	if strings.TrimSpace(folderID) != "" && !selectedFound {
		return view, fmt.Errorf("workspace folder was not found")
	}
	if !selectedFound {
		return view, fmt.Errorf("workspace has no manageable Git repositories")
	}
	if err := ensureWorkspaceGitRepositoryRoot(ctx, selectedFolder); err != nil {
		return view, err
	}
	repository, err := loadWorkspaceGitRepositoryStatus(ctx, selectedFolder)
	if err != nil {
		return view, err
	}
	view.SelectedFolderID = selectedFolder.ID
	view.Repository = &repository
	return view, nil
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

func workspaceGitRepositorySummary(ctx context.Context, folder WorkspaceFolder) WorkspaceGitRepositorySummary {
	summary := WorkspaceGitRepositorySummary{
		FolderID: folder.ID,
		Label:    folder.Label,
		Path:     folder.Path,
	}
	if folder.Missing {
		summary.Error = "workspace folder is unavailable"
		return summary
	}
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		summary.Error = err.Error()
		return summary
	}
	summary.Available = true
	summary.CurrentBranch, summary.Detached = workspaceGitCurrentBranch(ctx, folder.Path)
	summary.Head, summary.ShortHead = workspaceGitHead(ctx, folder.Path)
	summary.Dirty, _ = workspaceGitRepositoryDirty(ctx, folder.Path)
	return summary
}

func loadWorkspaceGitRepositoryStatus(ctx context.Context, folder WorkspaceFolder) (WorkspaceGitRepositoryStatus, error) {
	currentBranch, detached := workspaceGitCurrentBranch(ctx, folder.Path)
	head, shortHead := workspaceGitHead(ctx, folder.Path)
	branches, err := loadWorkspaceGitBranches(ctx, folder.Path, currentBranch)
	if err != nil {
		return WorkspaceGitRepositoryStatus{}, err
	}
	files, err := loadGitChangedFilesForFolder(ctx, folder)
	if err != nil {
		return WorkspaceGitRepositoryStatus{}, err
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
	})
	commits, err := loadWorkspaceGitCommitHistory(ctx, folder.Path, workspaceGitHistoryLimit)
	if err != nil {
		return WorkspaceGitRepositoryStatus{}, err
	}
	return WorkspaceGitRepositoryStatus{
		FolderID:      folder.ID,
		Label:         folder.Label,
		Path:          folder.Path,
		CurrentBranch: currentBranch,
		Head:          head,
		ShortHead:     shortHead,
		Detached:      detached,
		Dirty:         len(files) > 0,
		Branches:      branches,
		FileCount:     len(files),
		Files:         files,
		Commits:       commits,
	}, nil
}

func ensureWorkspaceGitRepositoryRoot(ctx context.Context, folder WorkspaceFolder) error {
	output, err := runWorkspaceGitCommand(ctx, folder.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root, err := normalizedGitPath(strings.TrimSpace(string(output)))
	if err != nil {
		return fmt.Errorf("resolve Git repository root: %w", err)
	}
	folderRoot, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return err
	}
	folderRoot, err = normalizedGitPath(folderRoot)
	if err != nil {
		return fmt.Errorf("resolve workspace folder: %w", err)
	}
	if !samePath(root, folderRoot) {
		return fmt.Errorf("workspace folder must be the Git repository root")
	}
	return nil
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
	shortOutput, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return strings.TrimSpace(string(headOutput)), ""
	}
	return strings.TrimSpace(string(headOutput)), strings.TrimSpace(string(shortOutput))
}

func workspaceGitRepositoryDirty(ctx context.Context, workspacePath string) (bool, error) {
	status, err := runWorkspaceGitCommand(ctx, workspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return len(status) > 0, nil
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

func loadWorkspaceGitCommitFiles(ctx context.Context, folder WorkspaceFolder, hash string) ([]WorkspaceGitChangedFile, error) {
	parent, hasParent, err := workspaceGitFirstParent(ctx, folder.Path, hash)
	if err != nil {
		return nil, err
	}
	var output []byte
	if hasParent {
		output, err = runWorkspaceGitCommand(ctx, folder.Path, "diff", "--name-status", "-z", "--find-renames", parent, hash)
	} else {
		output, err = runWorkspaceGitCommand(ctx, folder.Path, "diff-tree", "--root", "--no-commit-id", "--name-status", "-z", "-r", "--find-renames", hash)
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
		file := WorkspaceGitChangedFile{
			Path:      labeledWorkspacePath(folder.Label, entry.path),
			OldPath:   labeledWorkspacePath(folder.Label, entry.oldPath),
			Operation: gitNameStatusOperation(entry.status),
			Status:    entry.status,
		}
		diffPath := entry.path
		if file.Operation == tools.FileChangeDeleted && entry.oldPath != "" {
			diffPath = entry.oldPath
		}
		var diff string
		if hasParent {
			diff, err = loadWorkspaceGitCommitDiffForPath(ctx, folder.Path, parent, hash, diffPath)
		} else {
			diff, err = loadWorkspaceGitRootCommitDiffForPath(ctx, folder.Path, hash, diffPath)
		}
		if err == nil && strings.TrimSpace(diff) != "" {
			file.Diff = prefixGitDiffPaths(diff, folder.Label)
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

func mustGitOutput(ctx context.Context, workspacePath string, args ...string) []byte {
	output, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
	if err != nil {
		return nil
	}
	return output
}
