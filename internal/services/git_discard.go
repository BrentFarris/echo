package services

import (
	"context"
	"fmt"
	"strings"
)

func (s *SystemService) DiscardWorkspaceGitFile(workspaceID string, folderID string, path string) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("git file path is required")
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	entry, err := workspaceGitStatusEntryForPath(ctx, folder, path)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := discardWorkspaceGitStatusEntry(ctx, folder.Path, entry); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func (s *SystemService) DiscardWorkspaceGitChanges(workspaceID string, folderID string) (WorkspaceGitRepositoryView, error) {
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
	if dirty, err := workspaceGitRepositoryDirty(ctx, folder.Path); err != nil {
		return WorkspaceGitRepositoryView{}, err
	} else if !dirty {
		return s.loadWorkspaceGitRepository(workspace, folder.ID)
	}
	if workspaceGitHasHead(ctx, folder.Path) {
		if _, err := runWorkspaceGitCommand(ctx, folder.Path, "reset", "--hard", "HEAD"); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	} else if _, err := runWorkspaceGitCommand(ctx, folder.Path, "rm", "-r", "--cached", "--ignore-unmatch", "--", "."); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "clean", "-fd"); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func workspaceGitStatusEntryForPath(ctx context.Context, folder WorkspaceFolder, requestedPath string) (gitStatusEntry, error) {
	path, err := normalizeWorkspaceGitRequestedPath(folder, requestedPath)
	if err != nil {
		return gitStatusEntry{}, err
	}
	status, err := runWorkspaceGitCommand(ctx, folder.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gitStatusEntry{}, err
	}
	entries, err := parseGitStatusPorcelain(status)
	if err != nil {
		return gitStatusEntry{}, err
	}
	for _, entry := range entries {
		if sameWorkspaceGitRelativePath(entry.path, path) || sameWorkspaceGitRelativePath(entry.oldPath, path) {
			return entry, nil
		}
	}
	return gitStatusEntry{}, fmt.Errorf("changed Git file was not found")
}

func normalizeWorkspaceGitRequestedPath(folder WorkspaceFolder, requestedPath string) (string, error) {
	path := cleanWorkspaceGitRelativePath(requestedPath)
	labelPrefix := cleanWorkspaceGitRelativePath(folder.Label) + "/"
	if len(path) > len(labelPrefix) && strings.EqualFold(path[:len(labelPrefix)], labelPrefix) {
		path = path[len(labelPrefix):]
	}
	if path == "" {
		return "", fmt.Errorf("git file path is required")
	}
	if _, err := resolveWorkspaceGitPath(folder.Path, path); err != nil {
		return "", err
	}
	return path, nil
}

func discardWorkspaceGitStatusEntry(ctx context.Context, workspacePath string, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if _, err := resolveWorkspaceGitPath(workspacePath, path); err != nil {
			return err
		}
	}
	if entry.index == '?' && entry.worktree == '?' {
		return cleanWorkspaceGitPaths(ctx, workspacePath, paths)
	}
	if workspaceGitHasHead(ctx, workspacePath) {
		args := append([]string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}, paths...)
		if _, err := runWorkspaceGitCommand(ctx, workspacePath, args...); err != nil {
			return err
		}
	} else {
		args := append([]string{"rm", "-r", "--cached", "--ignore-unmatch", "--"}, paths...)
		if _, err := runWorkspaceGitCommand(ctx, workspacePath, args...); err != nil {
			return err
		}
	}
	return cleanWorkspaceGitPaths(ctx, workspacePath, paths)
}

func workspaceGitDiscardPaths(entry gitStatusEntry) []string {
	paths := make([]string, 0, 2)
	if entry.path != "" {
		paths = append(paths, entry.path)
	}
	if entry.oldPath != "" && (entry.index == 'R' || entry.worktree == 'R') {
		paths = append(paths, entry.oldPath)
	}
	return paths
}

func cleanWorkspaceGitPaths(ctx context.Context, workspacePath string, paths []string) error {
	args := append([]string{"clean", "-fd", "--"}, paths...)
	_, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
	return err
}

func workspaceGitHasHead(ctx context.Context, workspacePath string) bool {
	_, err := runWorkspaceGitCommand(ctx, workspacePath, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func cleanWorkspaceGitRelativePath(path string) string {
	return strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
}

func sameWorkspaceGitRelativePath(left string, right string) bool {
	return cleanWorkspaceGitRelativePath(left) == cleanWorkspaceGitRelativePath(right)
}
