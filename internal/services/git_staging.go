package services

import (
	"context"
	"fmt"
	"strings"
)

func (s *SystemService) StageWorkspaceGitFile(workspaceID string, folderID string, path string) (WorkspaceGitRepositoryView, error) {
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
	if err := stageWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
}

func (s *SystemService) UnstageWorkspaceGitFile(workspaceID string, folderID string, path string) (WorkspaceGitRepositoryView, error) {
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
	if err := unstageWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
}

func (s *SystemService) StageWorkspaceGitChanges(workspaceID string, folderID string) (WorkspaceGitRepositoryView, error) {
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
	if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "add", "-A", "--", workspaceGitFolderPathspec(repository)); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
}

func (s *SystemService) UnstageWorkspaceGitChanges(workspaceID string, folderID string) (WorkspaceGitRepositoryView, error) {
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
	pathspec := workspaceGitFolderPathspec(repository)
	if workspaceGitHasHead(ctx, repository.WorktreePath) {
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "restore", "--staged", "--", pathspec); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	} else if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "rm", "-r", "--cached", "--ignore-unmatch", "--", pathspec); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.refreshCachedWorkspaceGitRepositoryStatus(workspace, folder)
}

func workspaceGitFolderPathspec(repository workspaceGitRepositoryContext) string {
	if repository.FolderGitPath == "" {
		return "."
	}
	return repository.FolderGitPath
}

func stageWorkspaceGitStatusEntry(ctx context.Context, repository workspaceGitRepositoryContext, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if err := repository.requireGitPathInFolder(path); err != nil {
			return err
		}
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	_, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, args...)
	return err
}

func unstageWorkspaceGitStatusEntry(ctx context.Context, repository workspaceGitRepositoryContext, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if err := repository.requireGitPathInFolder(path); err != nil {
			return err
		}
	}
	return unstageWorkspaceGitPaths(ctx, repository.WorktreePath, paths)
}

func stageWorkspaceGitFolder(ctx context.Context, repository workspaceGitRepositoryContext, requestedFolder string) error {
	entries, err := workspaceGitStatusEntriesInFolder(ctx, repository, requestedFolder, gitStatusEntryHasUnstagedChanges)
	if err != nil {
		return err
	}
	paths, err := workspaceGitPathsForStatusEntries(repository, entries)
	if err != nil {
		return err
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	_, err = runWorkspaceGitCommand(ctx, repository.WorktreePath, args...)
	return err
}

func unstageWorkspaceGitFolder(ctx context.Context, repository workspaceGitRepositoryContext, requestedFolder string) error {
	entries, err := workspaceGitStatusEntriesInFolder(ctx, repository, requestedFolder, gitStatusEntryHasStagedChanges)
	if err != nil {
		return err
	}
	paths, err := workspaceGitPathsForStatusEntries(repository, entries)
	if err != nil {
		return err
	}
	return unstageWorkspaceGitPaths(ctx, repository.WorktreePath, paths)
}

func workspaceGitStatusEntriesInFolder(
	ctx context.Context,
	repository workspaceGitRepositoryContext,
	requestedFolder string,
	include func(gitStatusEntry) bool,
) ([]gitStatusEntry, error) {
	folder := cleanWorkspaceGitRelativePath(requestedFolder)
	if folder == "" {
		return nil, fmt.Errorf("git folder path is required")
	}
	if _, err := repository.requestedPathToGitPath(folder); err != nil {
		return nil, err
	}
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
	if err != nil {
		return nil, err
	}
	selected := make([]gitStatusEntry, 0, len(entries))
	for _, entry := range entries {
		if !include(entry) || !workspaceGitPathIsInFolder(repository.labeledGitPath(entry.path), folder) {
			continue
		}
		selected = append(selected, entry)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("changed Git folder was not found")
	}
	return selected, nil
}

func workspaceGitPathIsInFolder(path string, folder string) bool {
	path = cleanWorkspaceGitRelativePath(path)
	folder = cleanWorkspaceGitRelativePath(folder)
	return path == folder || strings.HasPrefix(path, folder+"/")
}

func workspaceGitPathsForStatusEntries(repository workspaceGitRepositoryContext, entries []gitStatusEntry) ([]string, error) {
	paths := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		for _, path := range workspaceGitDiscardPaths(entry) {
			if err := repository.requireGitPathInFolder(path); err != nil {
				return nil, err
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("git folder contains no changed files")
	}
	return paths, nil
}

func unstageWorkspaceGitPaths(ctx context.Context, workspacePath string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	if workspaceGitHasHead(ctx, workspacePath) {
		args := append([]string{"restore", "--staged", "--"}, paths...)
		_, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
		return err
	}
	args := append([]string{"rm", "-r", "--cached", "--ignore-unmatch", "--"}, paths...)
	_, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
	return err
}
