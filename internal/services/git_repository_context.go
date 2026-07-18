package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type workspaceGitRepositoryContext struct {
	Folder        WorkspaceFolder
	WorktreePath  string
	FolderPath    string
	FolderGitPath string
}

func (s *SystemService) workspaceGitRepositoryContext(ctx context.Context, workspace Workspace, folder WorkspaceFolder) (workspaceGitRepositoryContext, error) {
	if folder.Missing {
		return workspaceGitRepositoryContext{}, fmt.Errorf("workspace folder is unavailable")
	}
	folderRoot, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return workspaceGitRepositoryContext{}, err
	}
	folderRoot, err = normalizedGitPath(folderRoot)
	if err != nil {
		return workspaceGitRepositoryContext{}, fmt.Errorf("resolve workspace folder: %w", err)
	}

	if workspace.SearchParentGitRepositories {
		if cached, ok := s.cachedWorkspaceGitRepositoryRoot(workspace, folder); ok {
			repository, err := workspaceGitRepositoryContextFromRoot(ctx, folder, folderRoot, cached)
			if err == nil {
				return repository, nil
			}
		}
		root, err := discoverWorkspaceGitRepositoryRoot(ctx, folderRoot)
		if err != nil {
			return workspaceGitRepositoryContext{}, err
		}
		repository, err := workspaceGitRepositoryContextFromValidatedRoot(folder, folderRoot, root)
		if err != nil {
			return workspaceGitRepositoryContext{}, err
		}
		if !samePath(repository.WorktreePath, repository.FolderPath) {
			_ = s.storeWorkspaceGitRepositoryRoot(workspace, folder, repository.WorktreePath)
		}
		return repository, nil
	}

	root, err := discoverWorkspaceGitRepositoryRoot(ctx, folderRoot)
	if err != nil {
		return workspaceGitRepositoryContext{}, err
	}
	repository, err := workspaceGitRepositoryContextFromValidatedRoot(folder, folderRoot, root)
	if err != nil {
		return workspaceGitRepositoryContext{}, err
	}
	if !samePath(repository.WorktreePath, repository.FolderPath) {
		return workspaceGitRepositoryContext{}, fmt.Errorf("workspace folder must be the Git repository root")
	}
	return repository, nil
}

func discoverWorkspaceGitRepositoryRoot(ctx context.Context, path string) (string, error) {
	output, err := runWorkspaceGitCommand(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root, err := normalizedGitPath(strings.TrimSpace(string(output)))
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	return root, nil
}

func workspaceGitRepositoryContextFromRoot(ctx context.Context, folder WorkspaceFolder, folderRoot string, root string) (workspaceGitRepositoryContext, error) {
	root, err := normalizedGitPath(root)
	if err != nil {
		return workspaceGitRepositoryContext{}, fmt.Errorf("resolve Git repository root: %w", err)
	}
	output, err := runWorkspaceGitCommand(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return workspaceGitRepositoryContext{}, err
	}
	actualRoot, err := normalizedGitPath(strings.TrimSpace(string(output)))
	if err != nil {
		return workspaceGitRepositoryContext{}, fmt.Errorf("resolve Git repository root: %w", err)
	}
	if !samePath(root, actualRoot) {
		return workspaceGitRepositoryContext{}, fmt.Errorf("cached Git repository root is stale")
	}
	return workspaceGitRepositoryContextFromValidatedRoot(folder, folderRoot, actualRoot)
}

func workspaceGitRepositoryContextFromValidatedRoot(folder WorkspaceFolder, folderRoot string, root string) (workspaceGitRepositoryContext, error) {
	relative, err := filepath.Rel(root, folderRoot)
	if err != nil {
		return workspaceGitRepositoryContext{}, fmt.Errorf("resolve workspace folder inside Git repository: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return workspaceGitRepositoryContext{}, fmt.Errorf("workspace folder is outside the Git repository")
	}
	if relative == "." {
		relative = ""
	}
	return workspaceGitRepositoryContext{
		Folder:        folder,
		WorktreePath:  root,
		FolderPath:    folderRoot,
		FolderGitPath: cleanWorkspaceGitRelativePath(filepath.ToSlash(relative)),
	}, nil
}

func (r workspaceGitRepositoryContext) gitPathInFolder(path string) (string, bool) {
	clean := cleanWorkspaceGitRelativePath(path)
	prefix := cleanWorkspaceGitRelativePath(r.FolderGitPath)
	if clean == "" {
		return "", prefix == ""
	}
	if prefix == "" {
		return clean, true
	}
	if strings.EqualFold(clean, prefix) {
		return "", true
	}
	lowerClean := strings.ToLower(clean)
	lowerPrefix := strings.ToLower(prefix)
	if strings.HasPrefix(lowerClean, lowerPrefix+"/") {
		return clean[len(prefix)+1:], true
	}
	return "", false
}

func (r workspaceGitRepositoryContext) labeledGitPath(path string) string {
	relative, ok := r.gitPathInFolder(path)
	if !ok {
		return ""
	}
	return labeledWorkspacePath(r.Folder.Label, relative)
}

func (r workspaceGitRepositoryContext) requestedPathToGitPath(requestedPath string) (string, error) {
	path := cleanWorkspaceGitRelativePath(requestedPath)
	labelPrefix := cleanWorkspaceGitRelativePath(r.Folder.Label) + "/"
	if len(path) > len(labelPrefix) && strings.EqualFold(path[:len(labelPrefix)], labelPrefix) {
		path = path[len(labelPrefix):]
	}
	if path == "" {
		return "", fmt.Errorf("git file path is required")
	}
	gitPath := path
	if r.FolderGitPath != "" {
		gitPath = cleanWorkspaceGitRelativePath(r.FolderGitPath + "/" + path)
	}
	if err := r.requireGitPathInFolder(gitPath); err != nil {
		return "", err
	}
	return gitPath, nil
}

func (r workspaceGitRepositoryContext) requireGitPathInFolder(path string) error {
	if _, ok := r.gitPathInFolder(path); !ok {
		return fmt.Errorf("git path escapes workspace folder")
	}
	resolved, err := resolveWorkspaceGitPath(r.WorktreePath, path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(r.FolderPath, resolved)
	if err != nil {
		return fmt.Errorf("resolve workspace git relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("git path escapes workspace folder")
	}
	return nil
}

func (s *SystemService) cachedWorkspaceGitRepositoryRoot(workspace Workspace, folder WorkspaceFolder) (string, bool) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()

	location, err := resolveWorkspaceTaskLocation(workspace)
	if err != nil {
		return "", false
	}
	state, err := readWorkspaceStateFileAt(location.statePath)
	if err != nil {
		return "", false
	}
	for _, entry := range state.Git.ParentRepositories {
		if entry.FolderID != folder.ID {
			continue
		}
		if !sameCleanPath(entry.FolderPath, folder.Path) {
			continue
		}
		root := strings.TrimSpace(entry.RepositoryRoot)
		if root != "" {
			return root, true
		}
	}
	return "", false
}

func (s *SystemService) storeWorkspaceGitRepositoryRoot(workspace Workspace, folder WorkspaceFolder, root string) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()

	location, err := resolveWorkspaceTaskLocation(workspace)
	if err != nil {
		return err
	}
	state, err := readWorkspaceStateFileAt(location.statePath)
	if err != nil {
		return err
	}
	entry := workspaceStateGitParentRepository{
		FolderID:       folder.ID,
		FolderPath:     filepath.Clean(folder.Path),
		RepositoryRoot: filepath.Clean(root),
	}
	repositories := state.Git.ParentRepositories[:0]
	updated := false
	for _, existing := range state.Git.ParentRepositories {
		if existing.FolderID == folder.ID && sameCleanPath(existing.FolderPath, folder.Path) {
			repositories = append(repositories, entry)
			updated = true
			continue
		}
		repositories = append(repositories, existing)
	}
	if !updated {
		repositories = append(repositories, entry)
	}
	state.Git.ParentRepositories = repositories
	return writeWorkspaceStateFileAt(location.root, location.statePath, state)
}

func sameCleanPath(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	return samePath(filepath.Clean(left), filepath.Clean(right))
}
