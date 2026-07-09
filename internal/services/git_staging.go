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
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
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
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
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
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	for _, entry := range entries {
		if err := stageWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
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
	entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	for _, entry := range entries {
		if !gitStatusEntryHasStagedChanges(entry) {
			continue
		}
		if err := unstageWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
			return WorkspaceGitRepositoryView{}, err
		}
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
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
