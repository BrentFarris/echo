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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	entry, err := workspaceGitStatusEntryForPath(ctx, folder, path)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := stageWorkspaceGitStatusEntry(ctx, folder.Path, entry); err != nil {
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	entry, err := workspaceGitStatusEntryForPath(ctx, folder, path)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := unstageWorkspaceGitStatusEntry(ctx, folder.Path, entry); err != nil {
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if _, err := runWorkspaceGitCommand(ctx, folder.Path, "add", "-A"); err != nil {
		return WorkspaceGitRepositoryView{}, err
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
	if err := ensureWorkspaceGitRepositoryRoot(ctx, folder); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := unstageWorkspaceGitPaths(ctx, folder.Path, []string{"."}); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func stageWorkspaceGitStatusEntry(ctx context.Context, workspacePath string, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if _, err := resolveWorkspaceGitPath(workspacePath, path); err != nil {
			return err
		}
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	_, err := runWorkspaceGitCommand(ctx, workspacePath, args...)
	return err
}

func unstageWorkspaceGitStatusEntry(ctx context.Context, workspacePath string, entry gitStatusEntry) error {
	paths := workspaceGitDiscardPaths(entry)
	if len(paths) == 0 {
		return fmt.Errorf("git file path is required")
	}
	for _, path := range paths {
		if _, err := resolveWorkspaceGitPath(workspacePath, path); err != nil {
			return err
		}
	}
	return unstageWorkspaceGitPaths(ctx, workspacePath, paths)
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
