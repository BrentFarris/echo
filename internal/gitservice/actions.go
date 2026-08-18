package gitservice

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brent/echo/internal/workspacefs"
)

var simpleGitName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}+-]*$`)

func (s *Service) Action(ctx context.Context, workspaceID, repositoryID string, request ActionRequest) (result ActionResult, resultErr error) {
	state, err := s.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return ActionResult{}, err
	}
	request.Action = strings.TrimSpace(request.Action)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return ActionResult{}, &Error{Code: "request_id_required", Message: "requestId is required"}
	}
	operation := Operation{
		WorkspaceID: workspaceID, RepositoryID: repositoryID, RequestID: request.RequestID,
		Action: request.Action, State: "running",
	}
	s.emit(Event{Type: "git_operation", WorkspaceID: workspaceID, RepositoryID: repositoryID, Operation: &operation})
	state.mutationMu.Lock()
	defer state.mutationMu.Unlock()
	defer func() {
		operation.State = "completed"
		if resultErr != nil {
			operation.State = "failed"
			operation.Error = resultErr.Error()
		}
		revision := state.revision.Add(1)
		result.Revision = revision
		s.emit(Event{Type: "git_operation", WorkspaceID: workspaceID, RepositoryID: repositoryID, Operation: &operation})
		s.scheduleStatusRefresh(state)
	}()

	paths, trashIDs, err := s.executeAction(ctx, state, request)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		RequestID: request.RequestID, RepositoryID: repositoryID,
		AffectedPaths: paths, TrashIDs: trashIDs,
	}, nil
}

func (s *Service) executeAction(ctx context.Context, state *repositoryState, request ActionRequest) ([]string, []string, error) {
	action := request.Action
	switch action {
	case "stage":
		paths, err := state.validatePaths(request.Paths)
		if err != nil {
			return nil, nil, err
		}
		args := append([]string{"add", "--"}, paths...)
		_, err = runGitWithTimeout(ctx, state, false, nil, args...)
		return paths, nil, err
	case "stage_all":
		paths := state.scopePathspecs()
		args := append([]string{"add", "-A", "--"}, paths...)
		_, err := runGitWithTimeout(ctx, state, false, nil, args...)
		return paths, nil, err
	case "unstage":
		paths, err := state.validatePaths(request.Paths)
		if err != nil {
			return nil, nil, err
		}
		err = unstagePaths(ctx, state, paths)
		return paths, nil, err
	case "unstage_all":
		paths := state.scopePathspecs()
		err := unstagePaths(ctx, state, paths)
		return paths, nil, err
	case "discard", "discard_all":
		if !request.Confirmed {
			return nil, nil, &Error{Code: "confirmation_required", Message: "discarding changes requires confirmation"}
		}
		return s.discard(ctx, state, request)
	case "commit_staged", "commit_all", "commit_staged_amend", "commit_all_amend", "commit_staged_signoff", "commit_all_signoff":
		return nil, nil, s.commit(ctx, state, request)
	case "pull", "pull_rebase", "pull_from", "push", "push_to", "fetch", "fetch_prune", "fetch_all", "sync":
		return nil, nil, runNetworkAction(ctx, state, request)
	case "checkout", "merge", "rebase", "abort_rebase", "create_branch", "create_branch_from", "rename_branch", "delete_branch", "delete_remote_branch", "publish_branch":
		return nil, nil, runBranchAction(ctx, state, request)
	case "add_remote", "remove_remote":
		return nil, nil, runRemoteAction(ctx, state, request)
	case "stash", "stash_untracked", "stash_staged", "apply_latest_stash", "apply_stash", "pop_latest_stash", "pop_stash", "drop_stash", "drop_all_stashes":
		return nil, nil, runStashAction(ctx, state, request)
	case "create_tag", "delete_tag", "delete_remote_tag", "push_tags":
		return nil, nil, runTagAction(ctx, state, request)
	default:
		return nil, nil, &Error{Code: "unsupported_git_action", Message: "unsupported Git action"}
	}
}

func unstagePaths(ctx context.Context, state *repositoryState, paths []string) error {
	if repositoryHasHead(ctx, state) {
		args := append([]string{"restore", "--staged", "--"}, paths...)
		_, err := runGitWithTimeout(ctx, state, false, nil, args...)
		return err
	}
	args := append([]string{"rm", "-r", "--cached", "--ignore-unmatch", "--"}, paths...)
	_, err := runGitWithTimeout(ctx, state, false, nil, args...)
	return err
}

func (s *Service) discard(ctx context.Context, state *repositoryState, request ActionRequest) ([]string, []string, error) {
	status, err := s.readStatus(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	requested := request.Paths
	if request.Action == "discard_all" {
		requested = make([]string, 0, len(status.records))
		for _, record := range status.records {
			if !state.pathAllowed(record.path) {
				continue
			}
			if record.kind == '?' || record.conflict || record.worktree != '.' {
				requested = append(requested, record.path)
			}
		}
	}
	paths, err := state.validatePaths(requested)
	if err != nil {
		if request.Action == "discard_all" && len(requested) == 0 {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	untracked := make(map[string]bool)
	for _, record := range status.records {
		if record.kind == '?' && state.pathAllowed(record.path) {
			untracked[pathIdentity(record.path)] = true
		}
	}
	tracked := []string{}
	trashIDs := []string{}
	for _, path := range paths {
		if !untracked[pathIdentity(path)] {
			tracked = append(tracked, path)
			continue
		}
		ref, ok := state.refForPath(path)
		if !ok || ref == nil {
			return nil, trashIDs, &Error{Code: "path_outside_workspace", Message: "untracked file is outside this workspace"}
		}
		item, trashErr := s.fs.Trash(state.workspaceID, *ref)
		if trashErr != nil {
			return nil, trashIDs, trashErr
		}
		trashIDs = append(trashIDs, item.ID)
	}
	if len(tracked) > 0 {
		args := append([]string{"restore", "--worktree", "--"}, tracked...)
		if _, err := runGitWithTimeout(ctx, state, false, nil, args...); err != nil {
			return paths, trashIDs, err
		}
	}
	return paths, trashIDs, nil
}

func (s *Service) commit(ctx context.Context, state *repositoryState, request ActionRequest) error {
	status, err := s.loadStatus(ctx, state)
	if err != nil {
		return err
	}
	if status.HiddenStagedCount > 0 {
		return &Error{
			Code: "hidden_staged_changes", Message: "this parent repository has staged changes outside the workspace; open the repository root before committing",
			Details: map[string]any{"count": status.HiddenStagedCount},
		}
	}
	all := strings.Contains(request.Action, "commit_all")
	amend := strings.Contains(request.Action, "amend")
	signoff := strings.Contains(request.Action, "signoff")
	if all {
		paths := state.scopePathspecs()
		args := append([]string{"add", "-A", "--"}, paths...)
		if _, err := runGitWithTimeout(ctx, state, false, nil, args...); err != nil {
			return err
		}
		status, err = s.loadStatus(ctx, state)
		if err != nil {
			return err
		}
	}
	if len(status.Staged) == 0 && !amend {
		return &Error{Code: "no_staged_changes", Message: "there are no staged changes to commit"}
	}
	message := strings.TrimSpace(strings.ReplaceAll(request.Message, "\r\n", "\n"))
	if message == "" && !amend {
		return &Error{Code: "commit_message_required", Message: "commit message is required"}
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
		_, err = runGitWithTimeout(ctx, state, false, nil, args...)
		return err
	}
	message += "\n"
	args = append(args, "-F", "-")
	_, err = runGitWithTimeout(ctx, state, false, []byte(message), args...)
	return err
}

func runNetworkAction(ctx context.Context, state *repositoryState, request ActionRequest) error {
	remote := optionalGitName(request.Remote, "origin")
	ref := strings.TrimSpace(request.Ref)
	switch request.Action {
	case "pull":
		_, err := runGitWithTimeout(ctx, state, true, nil, "pull")
		return err
	case "pull_rebase":
		_, err := runGitWithTimeout(ctx, state, true, nil, "pull", "--rebase")
		return err
	case "pull_from":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		args := []string{"pull", remote}
		if ref != "" {
			if err := requireSafeRef(ref); err != nil {
				return err
			}
			args = append(args, ref)
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, args...)
		return err
	case "push":
		_, err := runGitWithTimeout(ctx, state, true, nil, "push")
		return err
	case "push_to":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		args := []string{"push", remote}
		if ref != "" {
			if err := requireSafeRef(ref); err != nil {
				return err
			}
			args = append(args, ref)
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, args...)
		return err
	case "fetch":
		_, err := runGitWithTimeout(ctx, state, true, nil, "fetch")
		return err
	case "fetch_prune":
		_, err := runGitWithTimeout(ctx, state, true, nil, "fetch", "--prune")
		return err
	case "fetch_all":
		_, err := runGitWithTimeout(ctx, state, true, nil, "fetch", "--all")
		return err
	case "sync":
		if _, err := runGitWithTimeout(ctx, state, true, nil, "pull"); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, "push")
		return err
	}
	return nil
}

func runBranchAction(ctx context.Context, state *repositoryState, request ActionRequest) error {
	ref := strings.TrimSpace(request.Ref)
	name := strings.TrimSpace(request.Name)
	remote := optionalGitName(request.Remote, "origin")
	switch request.Action {
	case "abort_rebase":
		_, err := runGitWithTimeout(ctx, state, false, nil, "rebase", "--abort")
		return err
	case "checkout", "merge", "rebase":
		if err := requireSafeRef(ref); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, request.Action, ref)
		return err
	case "create_branch", "create_branch_from":
		if err := validateBranchName(ctx, state, name); err != nil {
			return err
		}
		args := []string{"checkout", "-b", name}
		if request.Action == "create_branch_from" {
			if err := requireSafeRef(request.StartPoint); err != nil {
				return err
			}
			args = append(args, strings.TrimSpace(request.StartPoint))
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, args...)
		return err
	case "rename_branch":
		if err := validateBranchName(ctx, state, name); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, "branch", "-m", name)
		return err
	case "delete_branch":
		if err := requireSafeRef(ref); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, "branch", "-d", ref)
		return err
	case "delete_remote_branch":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		if err := requireSafeRef(ref); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, "push", remote, "--delete", ref)
		return err
	case "publish_branch":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		branch := strings.TrimSpace(request.Branch)
		if branch == "" {
			branch = currentBranch(ctx, state)
		}
		if err := requireSafeRef(branch); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, "push", "-u", remote, branch)
		return err
	}
	return nil
}

func runRemoteAction(ctx context.Context, state *repositoryState, request ActionRequest) error {
	name := strings.TrimSpace(request.Name)
	if err := requireSimpleName(name, "remote"); err != nil {
		return err
	}
	if request.Action == "remove_remote" {
		_, err := runGitWithTimeout(ctx, state, false, nil, "remote", "remove", name)
		return err
	}
	url := strings.TrimSpace(request.URL)
	if url == "" || strings.ContainsRune(url, 0) {
		return &Error{Code: "remote_url_required", Message: "remote URL is required"}
	}
	_, err := runGitWithTimeout(ctx, state, false, nil, "remote", "add", name, url)
	return err
}

func runStashAction(ctx context.Context, state *repositoryState, request ActionRequest) error {
	ref := strings.TrimSpace(request.Ref)
	if ref == "" {
		ref = "stash@{0}"
	}
	if strings.ContainsRune(ref, 0) || strings.HasPrefix(ref, "-") {
		return &Error{Code: "invalid_git_ref", Message: "stash reference is invalid"}
	}
	switch request.Action {
	case "stash", "stash_untracked", "stash_staged":
		args := []string{"stash", "push"}
		if request.Action == "stash_untracked" {
			args = append(args, "--include-untracked")
		}
		if request.Action == "stash_staged" {
			args = append(args, "--staged")
		}
		if message := strings.TrimSpace(request.Message); message != "" {
			args = append(args, "-m", message)
		}
		args = append(args, "--")
		args = append(args, state.scopePathspecs()...)
		_, err := runGitWithTimeout(ctx, state, false, nil, args...)
		return err
	case "apply_latest_stash", "apply_stash":
		_, err := runGitWithTimeout(ctx, state, false, nil, "stash", "apply", ref)
		return err
	case "pop_latest_stash", "pop_stash":
		_, err := runGitWithTimeout(ctx, state, false, nil, "stash", "pop", ref)
		return err
	case "drop_stash":
		_, err := runGitWithTimeout(ctx, state, false, nil, "stash", "drop", ref)
		return err
	case "drop_all_stashes":
		_, err := runGitWithTimeout(ctx, state, false, nil, "stash", "clear")
		return err
	}
	return nil
}

func runTagAction(ctx context.Context, state *repositoryState, request ActionRequest) error {
	name := strings.TrimSpace(request.Name)
	remote := optionalGitName(request.Remote, "origin")
	switch request.Action {
	case "push_tags":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, "push", remote, "--tags")
		return err
	case "create_tag":
		if err := requireSafeRef(name); err != nil {
			return err
		}
		args := []string{"tag", name}
		if strings.TrimSpace(request.Ref) != "" {
			if err := requireSafeRef(request.Ref); err != nil {
				return err
			}
			args = append(args, strings.TrimSpace(request.Ref))
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, args...)
		return err
	case "delete_tag":
		if err := requireSafeRef(name); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, false, nil, "tag", "-d", name)
		return err
	case "delete_remote_tag":
		if err := requireSimpleName(remote, "remote"); err != nil {
			return err
		}
		if err := requireSafeRef(name); err != nil {
			return err
		}
		_, err := runGitWithTimeout(ctx, state, true, nil, "push", remote, ":refs/tags/"+name)
		return err
	}
	return nil
}

func runGitWithTimeout(parent context.Context, state *repositoryState, network bool, input []byte, args ...string) ([]byte, error) {
	timeout := localCommandTimeout
	if network {
		timeout = networkCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return runGit(ctx, state.root, input, false, args...)
}

func repositoryHasHead(parent context.Context, state *repositoryState) bool {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	_, err := runGit(ctx, state.root, nil, true, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func validateBranchName(parent context.Context, state *repositoryState, name string) error {
	if name = strings.TrimSpace(name); name == "" || strings.HasPrefix(name, "-") || strings.ContainsRune(name, 0) {
		return &Error{Code: "invalid_branch", Message: "branch name is invalid"}
	}
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	if _, err := runGit(ctx, state.root, nil, true, "check-ref-format", "--branch", name); err != nil {
		return &Error{Code: "invalid_branch", Message: "branch name is invalid", Cause: err}
	}
	return nil
}

func requireSafeRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, 0) {
		return &Error{Code: "invalid_git_ref", Message: "Git reference is invalid"}
	}
	return nil
}

func requireSimpleName(name, kind string) error {
	if !simpleGitName.MatchString(strings.TrimSpace(name)) || strings.Contains(name, "..") {
		return &Error{Code: "invalid_" + kind, Message: kind + " name is invalid"}
	}
	return nil
}

func optionalGitName(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func currentBranch(parent context.Context, state *repositoryState) string {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, state.root, nil, true, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (s *Service) Clone(ctx context.Context, workspaceID string, request CloneRequest) ([]Repository, error) {
	url := strings.TrimSpace(request.URL)
	if url == "" || strings.ContainsRune(url, 0) {
		return nil, &Error{Code: "clone_url_required", Message: "repository URL is required"}
	}
	destinationPath, err := cleanGitPath(request.Destination)
	if err != nil {
		return nil, err
	}
	destination, err := s.fs.ResolveEntryHostPath(workspaceID, workspacefs.FileRef{RootID: request.RootID, Path: destinationPath})
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		return nil, &Error{Code: "clone_destination_exists", Message: "clone destination already exists"}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	ctx, cancel := context.WithTimeout(ctx, networkCommandTimeout)
	defer cancel()
	if _, err := runGit(ctx, filepath.Dir(destination), nil, false, "clone", "--", url, filepath.Base(destination)); err != nil {
		return nil, err
	}
	return s.Repositories(ctx, workspaceID)
}

func (s *Service) Initialize(ctx context.Context, workspaceID string, request InitRequest) ([]Repository, error) {
	ref := workspacefs.FileRef{RootID: request.RootID, Path: request.Path}
	directory, err := s.fs.ResolveExistingHostPath(workspaceID, ref, true)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, &Error{Code: "init_directory_required", Message: "Git can only be initialized in a workspace folder", Cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, localCommandTimeout)
	defer cancel()
	if _, err := runGit(ctx, directory, nil, false, "init"); err != nil {
		return nil, err
	}
	return s.Repositories(ctx, workspaceID)
}
