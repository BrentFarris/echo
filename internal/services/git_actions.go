package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const workspaceGitNetworkTimeout = 5 * time.Minute

type WorkspaceGitActionRequest struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	Name    string `json:"name,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Branch  string `json:"branch,omitempty"`
	URL     string `json:"url,omitempty"`
}

func (s *SystemService) RunWorkspaceGitAction(workspaceID string, folderID string, request WorkspaceGitActionRequest) (WorkspaceGitRepositoryView, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	action := strings.TrimSpace(request.Action)
	if !workspaceGitActionAllowed(action) {
		return WorkspaceGitRepositoryView{}, fmt.Errorf("unsupported Git action %q", action)
	}
	if err := validateWorkspaceGitActionInput(request); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}

	lock := s.workspaceToolLock(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	timeout := workspaceGitCommandTimeout
	if workspaceGitNetworkAction(action) {
		timeout = workspaceGitNetworkTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	if err := runWorkspaceGitAction(ctx, repository, request); err != nil {
		return WorkspaceGitRepositoryView{}, err
	}
	return s.loadWorkspaceGitRepository(workspace, folder.ID)
}

func workspaceGitActionAllowed(action string) bool {
	switch action {
	case "commit_staged", "commit_all", "commit_staged_amend", "commit_all_amend", "commit_staged_signoff", "commit_all_signoff",
		"abort_rebase", "stage_all", "unstage_all", "discard_all", "sync", "pull", "pull_rebase", "pull_from", "push", "push_to",
		"fetch", "fetch_prune", "fetch_all", "checkout", "merge", "rebase", "create_branch", "create_branch_from", "rename_branch",
		"delete_branch", "delete_remote_branch", "publish_branch", "add_remote", "remove_remote", "stash", "stash_untracked", "stash_staged",
		"apply_latest_stash", "apply_stash", "pop_latest_stash", "pop_stash", "drop_stash", "drop_all_stashes", "create_tag", "delete_tag",
		"delete_remote_tag", "push_tags":
		return true
	default:
		return false
	}
}

func workspaceGitNetworkAction(action string) bool {
	switch action {
	case "sync", "pull", "pull_rebase", "pull_from", "push", "push_to", "fetch", "fetch_prune", "fetch_all", "delete_remote_branch", "publish_branch", "delete_remote_tag", "push_tags":
		return true
	default:
		return false
	}
}

func validateWorkspaceGitActionInput(request WorkspaceGitActionRequest) error {
	for label, value := range map[string]string{"name": request.Name, "ref": request.Ref, "remote": request.Remote, "branch": request.Branch} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("Git %s contains an invalid character", label)
		}
		if strings.HasPrefix(strings.TrimSpace(value), "-") {
			return fmt.Errorf("Git %s cannot begin with a dash", label)
		}
	}
	return nil
}

func runWorkspaceGitAction(ctx context.Context, repository workspaceGitRepositoryContext, request WorkspaceGitActionRequest) error {
	action := strings.TrimSpace(request.Action)
	root := repository.WorktreePath
	switch action {
	case "commit_staged", "commit_all", "commit_staged_amend", "commit_all_amend", "commit_staged_signoff", "commit_all_signoff":
		return commitWorkspaceGitAction(ctx, repository, request)
	case "abort_rebase":
		if !workspaceGitRebaseInProgress(ctx, root) {
			return fmt.Errorf("no Git rebase is in progress")
		}
		_, err := runWorkspaceGitCommand(ctx, root, "rebase", "--abort")
		return err
	case "stage_all":
		_, err := runWorkspaceGitCommand(ctx, root, "add", "-A", "--", workspaceGitFolderPathspec(repository))
		return err
	case "unstage_all":
		return unstageWorkspaceGitAction(ctx, repository)
	case "discard_all":
		entries, err := workspaceGitStatusEntriesForRepository(ctx, repository)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := discardWorkspaceGitStatusEntry(ctx, repository, entry); err != nil {
				return err
			}
		}
		return nil
	case "sync":
		return syncWorkspaceGitAction(ctx, root)
	case "pull":
		_, err := runWorkspaceGitCommand(ctx, root, "-c", "pull.rebase=false", "-c", "core.editor=true", "pull", "--no-edit")
		return err
	case "pull_rebase":
		_, err := runWorkspaceGitCommand(ctx, root, "-c", "core.editor=true", "pull", "--rebase")
		return err
	case "pull_from":
		remote, branch, err := requireWorkspaceGitRemoteAndBranch(ctx, root, request.Remote, request.Branch)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "-c", "pull.rebase=false", "-c", "core.editor=true", "pull", "--no-edit", remote, branch)
		return err
	case "push":
		_, err := runWorkspaceGitCommand(ctx, root, "push")
		return err
	case "push_to":
		remote, branch, err := requireWorkspaceGitRemoteAndBranch(ctx, root, request.Remote, request.Branch)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "push", remote, "HEAD:refs/heads/"+branch)
		return err
	case "fetch":
		_, err := runWorkspaceGitCommand(ctx, root, "fetch")
		return err
	case "fetch_prune":
		_, err := runWorkspaceGitCommand(ctx, root, "fetch", "--prune")
		return err
	case "fetch_all":
		_, err := runWorkspaceGitCommand(ctx, root, "fetch", "--all")
		return err
	case "checkout":
		return checkoutWorkspaceGitRef(ctx, root, strings.TrimSpace(request.Ref))
	case "merge":
		ref, err := requireWorkspaceGitCommitRef(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "merge", "--no-edit", ref)
		return err
	case "rebase":
		ref, err := requireWorkspaceGitCommitRef(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "-c", "core.editor=true", "rebase", ref)
		return err
	case "create_branch", "create_branch_from":
		name, err := validateWorkspaceGitBranchName(ctx, root, request.Name)
		if err != nil {
			return err
		}
		args := []string{"checkout", "-b", name}
		if action == "create_branch_from" {
			ref, refErr := requireWorkspaceGitCommitRef(ctx, root, request.Ref)
			if refErr != nil {
				return refErr
			}
			args = append(args, ref)
		}
		_, err = runWorkspaceGitCommand(ctx, root, args...)
		return err
	case "rename_branch":
		oldName, err := validateExistingWorkspaceGitBranch(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		newName, err := validateWorkspaceGitBranchName(ctx, root, request.Name)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "branch", "-m", oldName, newName)
		return err
	case "delete_branch":
		name, err := validateExistingWorkspaceGitBranch(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "branch", "-d", name)
		return err
	case "delete_remote_branch":
		remote, branch, err := requireWorkspaceGitRemoteAndBranch(ctx, root, request.Remote, request.Branch)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "push", remote, "--delete", branch)
		return err
	case "publish_branch":
		remote, err := requireWorkspaceGitRemote(ctx, root, request.Remote)
		if err != nil {
			return err
		}
		branch, detached := workspaceGitCurrentBranch(ctx, root)
		if detached || branch == "" {
			return fmt.Errorf("cannot publish a detached Git HEAD")
		}
		_, err = runWorkspaceGitCommand(ctx, root, "push", "--set-upstream", remote, branch)
		return err
	case "add_remote":
		name := strings.TrimSpace(request.Name)
		url := strings.TrimSpace(request.URL)
		if name == "" || url == "" {
			return fmt.Errorf("remote name and URL are required")
		}
		_, err := runWorkspaceGitCommand(ctx, root, "remote", "add", name, url)
		return err
	case "remove_remote":
		remote, err := requireWorkspaceGitRemote(ctx, root, request.Remote)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "remote", "remove", remote)
		return err
	case "stash", "stash_untracked", "stash_staged":
		args := []string{"stash", "push"}
		if action == "stash_untracked" {
			args = append(args, "--include-untracked")
		}
		if action == "stash_staged" {
			if !workspaceGitSupportsStashStaged(ctx, root) {
				return fmt.Errorf("this Git version does not support stashing staged changes")
			}
			args = append(args, "--staged")
		}
		if message := strings.TrimSpace(request.Message); message != "" {
			args = append(args, "-m", message)
		}
		args = append(args, "--", workspaceGitFolderPathspec(repository))
		_, err := runWorkspaceGitCommand(ctx, root, args...)
		return err
	case "apply_latest_stash", "apply_stash", "pop_latest_stash", "pop_stash":
		verb := "apply"
		if action == "pop_latest_stash" || action == "pop_stash" {
			verb = "pop"
		}
		args := []string{"stash", verb}
		if action == "apply_stash" || action == "pop_stash" {
			ref, err := requireWorkspaceGitStash(ctx, root, request.Ref)
			if err != nil {
				return err
			}
			args = append(args, ref)
		}
		_, err := runWorkspaceGitCommand(ctx, root, args...)
		return err
	case "drop_stash":
		ref, err := requireWorkspaceGitStash(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "stash", "drop", ref)
		return err
	case "drop_all_stashes":
		_, err := runWorkspaceGitCommand(ctx, root, "stash", "clear")
		return err
	case "create_tag":
		name, err := validateWorkspaceGitTagName(ctx, root, request.Name)
		if err != nil {
			return err
		}
		args := []string{"tag"}
		if message := strings.TrimSpace(request.Message); message != "" {
			args = append(args, "-a", name, "-m", message)
		} else {
			args = append(args, name)
		}
		_, err = runWorkspaceGitCommand(ctx, root, args...)
		return err
	case "delete_tag":
		name, err := requireWorkspaceGitTag(ctx, root, request.Ref)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "tag", "-d", name)
		return err
	case "delete_remote_tag":
		remote, err := requireWorkspaceGitRemote(ctx, root, request.Remote)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(request.Ref)
		if _, err := validateWorkspaceGitTagName(ctx, root, name); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "push", remote, ":refs/tags/"+name)
		return err
	case "push_tags":
		remote, err := requireWorkspaceGitRemote(ctx, root, request.Remote)
		if err != nil {
			return err
		}
		_, err = runWorkspaceGitCommand(ctx, root, "push", remote, "--tags")
		return err
	}
	return fmt.Errorf("unsupported Git action %q", action)
}

func commitWorkspaceGitAction(ctx context.Context, repository workspaceGitRepositoryContext, request WorkspaceGitActionRequest) error {
	action := strings.TrimSpace(request.Action)
	all := strings.Contains(action, "commit_all")
	amend := strings.Contains(action, "amend")
	signoff := strings.Contains(action, "signoff")
	if all {
		if _, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "add", "-A", "--", workspaceGitFolderPathspec(repository)); err != nil {
			return err
		}
	}
	if !amend {
		staged, err := workspaceGitRepositoryHasStagedChangesForContext(ctx, repository)
		if err != nil {
			return err
		}
		if !staged {
			return fmt.Errorf("no staged Git changes to commit")
		}
	}
	if err := requireWorkspaceGitCommitIdentity(ctx, repository.WorktreePath); err != nil {
		return err
	}
	message := strings.TrimSpace(strings.ReplaceAll(request.Message, "\r\n", "\n"))
	if message == "" && !amend {
		return fmt.Errorf("commit message is required")
	}
	args := []string{"commit"}
	if amend {
		args = append(args, "--amend")
	}
	if signoff {
		args = append(args, "--signoff")
	}
	if message == "" {
		args = append(args, "--no-edit")
		_, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, args...)
		return err
	}
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	args = append(args, "-F", "-")
	_, err := runWorkspaceGitCommandWithInput(ctx, repository.WorktreePath, []byte(message), args...)
	return err
}

func unstageWorkspaceGitAction(ctx context.Context, repository workspaceGitRepositoryContext) error {
	pathspec := workspaceGitFolderPathspec(repository)
	if workspaceGitHasHead(ctx, repository.WorktreePath) {
		_, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "restore", "--staged", "--", pathspec)
		return err
	}
	_, err := runWorkspaceGitCommand(ctx, repository.WorktreePath, "rm", "-r", "--cached", "--ignore-unmatch", "--", pathspec)
	return err
}

func syncWorkspaceGitAction(ctx context.Context, root string) error {
	branch, detached := workspaceGitCurrentBranch(ctx, root)
	if detached || branch == "" {
		return fmt.Errorf("cannot sync a detached Git HEAD")
	}
	upstream, ahead, behind, err := workspaceGitRemoteStatus(ctx, root)
	if err != nil {
		return err
	}
	if upstream == "" {
		return fmt.Errorf("current branch has no upstream configured")
	}
	if behind > 0 {
		if _, err := runWorkspaceGitCommand(ctx, root, "-c", "pull.rebase=false", "-c", "core.editor=true", "pull", "--no-edit"); err != nil {
			return err
		}
	}
	if ahead > 0 {
		if _, err := runWorkspaceGitCommand(ctx, root, "push"); err != nil {
			return err
		}
	}
	return nil
}

func checkoutWorkspaceGitRef(ctx context.Context, root string, requested string) error {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return fmt.Errorf("Git ref is required")
	}
	if branch, err := validateExistingWorkspaceGitBranch(ctx, root, requested); err == nil {
		_, err = runWorkspaceGitCommand(ctx, root, "checkout", branch)
		return err
	}
	remoteBranches, err := loadWorkspaceGitRemoteBranches(ctx, root)
	if err != nil {
		return err
	}
	for _, branch := range remoteBranches {
		if branch.Name == requested {
			_, err := runWorkspaceGitCommand(ctx, root, "checkout", "--track", branch.Name)
			return err
		}
	}
	ref, err := requireWorkspaceGitCommitRef(ctx, root, requested)
	if err != nil {
		return err
	}
	_, err = runWorkspaceGitCommand(ctx, root, "checkout", "--detach", ref)
	return err
}

func requireWorkspaceGitCommitRef(ctx context.Context, root string, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("Git ref is required")
	}
	if strings.HasPrefix(requested, "-") {
		return "", fmt.Errorf("Git ref cannot begin with a dash")
	}
	if _, err := runWorkspaceGitCommand(ctx, root, "rev-parse", "--verify", requested+"^{commit}"); err != nil {
		return "", fmt.Errorf("Git ref %q was not found", requested)
	}
	return requested, nil
}

func requireWorkspaceGitRemote(ctx context.Context, root string, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	remotes, err := loadWorkspaceGitRemotes(ctx, root)
	if err != nil {
		return "", err
	}
	if requested == "" && len(remotes) == 1 {
		return remotes[0].Name, nil
	}
	for _, remote := range remotes {
		if remote.Name == requested {
			return remote.Name, nil
		}
	}
	if requested == "" {
		return "", fmt.Errorf("select a Git remote")
	}
	return "", fmt.Errorf("Git remote %q was not found", requested)
}

func requireWorkspaceGitRemoteAndBranch(ctx context.Context, root string, remote string, branch string) (string, string, error) {
	validatedRemote, err := requireWorkspaceGitRemote(ctx, root, remote)
	if err != nil {
		return "", "", err
	}
	branch, err = validateWorkspaceGitBranchName(ctx, root, branch)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists") {
		output, checkErr := runWorkspaceGitCommand(ctx, root, "check-ref-format", "--branch", strings.TrimSpace(branch))
		if checkErr == nil {
			branch = strings.TrimSpace(string(output))
			err = nil
		}
	}
	if err != nil {
		return "", "", err
	}
	return validatedRemote, branch, nil
}

func requireWorkspaceGitStash(ctx context.Context, root string, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	stashes, err := loadWorkspaceGitStashes(ctx, root)
	if err != nil {
		return "", err
	}
	for _, stash := range stashes {
		if stash.Ref == requested {
			return stash.Ref, nil
		}
	}
	return "", fmt.Errorf("Git stash %q was not found", requested)
}

func validateWorkspaceGitTagName(ctx context.Context, root string, requested string) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		return "", fmt.Errorf("tag name is required")
	}
	if _, err := runWorkspaceGitCommand(ctx, root, "check-ref-format", "refs/tags/"+name); err != nil {
		return "", fmt.Errorf("invalid Git tag name %q", name)
	}
	if _, err := runWorkspaceGitCommand(ctx, root, "show-ref", "--verify", "--quiet", "refs/tags/"+name); err == nil {
		return "", fmt.Errorf("Git tag %q already exists", name)
	}
	return name, nil
}

func requireWorkspaceGitTag(ctx context.Context, root string, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	tags, err := loadWorkspaceGitTags(ctx, root)
	if err != nil {
		return "", err
	}
	for _, tag := range tags {
		if tag.Name == requested {
			return tag.Name, nil
		}
	}
	return "", fmt.Errorf("Git tag %q was not found", requested)
}

func (s *SystemService) LoadWorkspaceGitStash(workspaceID string, folderID string, ref string) (WorkspaceGitStashDetail, error) {
	workspace, folder, err := s.workspaceGitRepositoryFolder(workspaceID, folderID)
	if err != nil {
		return WorkspaceGitStashDetail{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitCommandTimeout)
	defer cancel()
	repository, err := s.workspaceGitRepositoryContext(ctx, workspace, folder)
	if err != nil {
		return WorkspaceGitStashDetail{}, err
	}
	ref, err = requireWorkspaceGitStash(ctx, repository.WorktreePath, ref)
	if err != nil {
		return WorkspaceGitStashDetail{}, err
	}
	stashes, err := loadWorkspaceGitStashes(ctx, repository.WorktreePath)
	if err != nil {
		return WorkspaceGitStashDetail{}, err
	}
	var selected WorkspaceGitStash
	for _, stash := range stashes {
		if stash.Ref == ref {
			selected = stash
			break
		}
	}
	files, err := loadWorkspaceGitCommitFiles(ctx, repository, ref)
	if err != nil {
		return WorkspaceGitStashDetail{}, err
	}
	if _, thirdParentErr := runWorkspaceGitCommand(ctx, repository.WorktreePath, "rev-parse", "--verify", ref+"^3"); thirdParentErr == nil {
		untracked, loadErr := loadWorkspaceGitCommitFiles(ctx, repository, ref+"^3")
		if loadErr != nil {
			return WorkspaceGitStashDetail{}, loadErr
		}
		seen := make(map[string]bool, len(files))
		for _, file := range files {
			seen[file.Path] = true
		}
		for _, file := range untracked {
			if !seen[file.Path] {
				files = append(files, file)
			}
		}
	}
	return WorkspaceGitStashDetail{WorkspaceID: workspace.ID, FolderID: folder.ID, Stash: selected, FileCount: len(files), Files: files}, nil
}

func (s *SystemService) ChooseWorkspaceGitCloneParent() (string, error) {
	if s.ctx == nil {
		return "", fmt.Errorf("application is not ready to open a folder picker")
	}
	return runtime.OpenDirectoryDialog(s.ctx, runtime.OpenDialogOptions{Title: "Choose Git clone destination"})
}

func (s *SystemService) CloneWorkspaceGitRepository(workspaceID string, url string, parentPath string, directoryName string) (AppState, error) {
	url = strings.TrimSpace(url)
	parentPath = strings.TrimSpace(parentPath)
	directoryName = strings.TrimSpace(directoryName)
	if url == "" || parentPath == "" {
		return AppState{}, fmt.Errorf("repository URL and destination are required")
	}
	parent, err := normalizedWorkspacePath(parentPath)
	if err != nil {
		return AppState{}, fmt.Errorf("resolve clone destination: %w", err)
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return AppState{}, fmt.Errorf("clone destination parent does not exist")
	}
	if directoryName == "" {
		directoryName = workspaceGitCloneDirectoryName(url)
	}
	if directoryName == "" || directoryName == "." || directoryName == ".." || filepath.Base(directoryName) != directoryName || strings.ContainsAny(directoryName, `/\\`) {
		return AppState{}, fmt.Errorf("invalid clone directory name")
	}
	destination := filepath.Join(parent, directoryName)
	relative, err := filepath.Rel(parent, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return AppState{}, fmt.Errorf("clone destination escapes its parent folder")
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitNetworkTimeout)
	defer cancel()
	if _, err := runWorkspaceGitCommand(ctx, parent, "clone", "--", url, destination); err != nil {
		return AppState{}, err
	}
	return s.AddWorkspaceFolder(workspaceID, destination)
}

func workspaceGitCloneDirectoryName(url string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(url), "/\\")
	if index := strings.LastIndexAny(trimmed, "/:"); index >= 0 {
		trimmed = trimmed[index+1:]
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	return strings.TrimSpace(trimmed)
}
