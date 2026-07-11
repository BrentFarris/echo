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
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	entry, err := workspaceGitStatusEntryForPath(ctx, repository, path)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := discardWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
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
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if len(entries) == 0 {
		return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
	}
	for _, entry := range entries {
		if err := discardWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
}

func workspaceGitStatusEntryForPath(ctx context.Context, repository workspaceGitRepositoryContext, requestedPath string) (gitStatusEntry, error) {
	path, err := normalizeWorkspaceGitRequestedPath(repository, requestedPath)
	if err != nil {
		return gitStatusEntry{}, err
	}
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
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

func normalizeWorkspaceGitRequestedPath(repository workspaceGitRepositoryContext, requestedPath string) (string, error) {
	return repository.requestedPathToGitPath(requestedPath)
}

func discardWorkspaceGitStatusEntry(ctx context.Context, repository workspaceGitRepositoryContext, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if err := repository.requireGitPathInFolder(path); err != nil {
			return err
		}
	}
	if entry.index == '?' && entry.worktree == '?' {
		return cleanWorkspaceGitPaths(ctx, repository.WorktreePath, paths)
	}
	if workspaceGitHasHead(ctx, repository.WorktreePath) {
		args := append([]string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}, paths...)
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, args...); err != nil {
			return err
		}
	} else {
		args := append([]string{"rm", "-r", "--cached", "--ignore-unmatch", "--"}, paths...)
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, args...); err != nil {
			return err
		}
	}
	return cleanWorkspaceGitPaths(ctx, repository.WorktreePath, paths)
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
