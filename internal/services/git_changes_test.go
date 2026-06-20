package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestLoadWorkspaceGitChangesIncludesModifiedDeletedAndUntrackedFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "keep.txt", "before\n")
	writeGitTestFile(t, root, "gone.txt", "remove me\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "keep.txt", "after\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, root, "fresh.txt", "fresh\n")

	review := loadGitChangesForTestWorkspace(t, root)
	if review.FileCount != 3 {
		t.Fatalf("expected three git changed files, got %#v", review)
	}
	files := gitReviewFilesByPath(review)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))

	keep := files[label+"/keep.txt"]
	if keep.Operation != tools.FileChangeEdited || keep.WorktreeStatus != "M" || !strings.Contains(keep.Diff, "-before") || !strings.Contains(keep.Diff, "+after") {
		t.Fatalf("unexpected modified file review: %#v", keep)
	}
	gone := files[label+"/gone.txt"]
	if gone.Operation != tools.FileChangeDeleted || gone.WorktreeStatus != "D" || !strings.Contains(gone.Diff, "-remove me") {
		t.Fatalf("unexpected deleted file review: %#v", gone)
	}
	fresh := files[label+"/fresh.txt"]
	if fresh.Operation != tools.FileChangeCreated || fresh.Status != "??" || !strings.Contains(fresh.Diff, "+fresh") {
		t.Fatalf("unexpected untracked file review: %#v", fresh)
	}
}

func TestLoadWorkspaceGitChangesIncludesStagedAndUnstagedFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "staged.txt", "before staged\n")
	writeGitTestFile(t, root, "worktree.txt", "before worktree\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "staged.txt", "after staged\n")
	runGitTestCommand(t, root, "add", "staged.txt")
	writeGitTestFile(t, root, "worktree.txt", "after worktree\n")

	review := loadGitChangesForTestWorkspace(t, root)
	files := gitReviewFilesByPath(review)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))

	staged := files[label+"/staged.txt"]
	if staged.Operation != tools.FileChangeEdited || staged.IndexStatus != "M" || staged.WorktreeStatus != "" || !strings.Contains(staged.Diff, "+after staged") {
		t.Fatalf("unexpected staged file review: %#v", staged)
	}
	worktree := files[label+"/worktree.txt"]
	if worktree.Operation != tools.FileChangeEdited || worktree.IndexStatus != "" || worktree.WorktreeStatus != "M" || !strings.Contains(worktree.Diff, "+after worktree") {
		t.Fatalf("unexpected worktree file review: %#v", worktree)
	}
}

func TestLoadWorkspaceGitChangesReturnsClearErrorForNonGitWorkspace(t *testing.T) {
	requireGitTestExecutable(t)
	root := t.TempDir()
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	_, err = service.LoadWorkspaceGitChanges(state.ActiveWorkspaceID)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected non-git workspace error, got %v", err)
	}
}

func TestLoadWorkspaceGitRepositoryListsStatusBranchesChangesAndHistory(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "notes.txt", "one\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	baseBranch := strings.TrimSpace(runGitTestCommand(t, root, "branch", "--show-current"))
	runGitTestCommand(t, root, "checkout", "-b", "feature/view")
	writeGitTestFile(t, root, "feature.txt", "feature\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "feature work")
	runGitTestCommand(t, root, "checkout", baseBranch)
	writeGitTestFile(t, root, "base.txt", "base\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "base work")
	writeGitTestFile(t, root, "notes.txt", "two\n")
	writeGitTestFile(t, root, "fresh.txt", "fresh\n")

	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	view, err := service.LoadWorkspaceGitRepository(state.ActiveWorkspaceID, "")
	if err != nil {
		t.Fatalf("load git repository: %v", err)
	}
	if view.Repository == nil {
		t.Fatalf("expected selected repository")
	}
	if view.Repository.CurrentBranch != baseBranch || view.Repository.Detached {
		t.Fatalf("unexpected current branch: %#v", view.Repository)
	}
	if !view.Repository.Dirty || view.Repository.FileCount != 2 {
		t.Fatalf("expected dirty repo with two files, got %#v", view.Repository)
	}
	if !gitBranchesContain(view.Repository.Branches, baseBranch, true) || !gitBranchesContain(view.Repository.Branches, "feature/view", false) {
		t.Fatalf("expected local branches, got %#v", view.Repository.Branches)
	}
	if len(view.Repository.Commits) < 2 || view.Repository.Commits[0].Subject != "base work" {
		t.Fatalf("unexpected history: %#v", view.Repository.Commits)
	}
}

func TestCommitWorkspaceGitChangesCommitsTrackedDeletedAndUntrackedFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "keep.txt", "before\n")
	writeGitTestFile(t, root, "gone.txt", "remove\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "keep.txt", "after\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, root, "fresh.txt", "fresh\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.CommitWorkspaceGitChanges(workspaceID, folderID, "commit all changes")
	if err != nil {
		t.Fatalf("commit changes: %v", err)
	}
	if view.Repository == nil || view.Repository.Dirty || view.Repository.FileCount != 0 {
		t.Fatalf("expected clean repo after commit, got %#v", view.Repository)
	}
	if len(view.Repository.Commits) == 0 || view.Repository.Commits[0].Subject != "commit all changes" {
		t.Fatalf("expected new commit at top of history, got %#v", view.Repository.Commits)
	}
	if status := runGitTestCommand(t, root, "status", "--porcelain=v1", "-z", "--untracked-files=all"); status != "" {
		t.Fatalf("expected clean git status, got %q", status)
	}
}

func TestCreateWorkspaceGitBranchChecksOutNewBranchAndPreservesDirtyFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "notes.txt", "before\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	writeGitTestFile(t, root, "notes.txt", "dirty\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	if _, err := service.CreateWorkspaceGitBranch(workspaceID, folderID, "bad name"); err == nil {
		t.Fatalf("expected invalid branch name to fail")
	}
	view, err := service.CreateWorkspaceGitBranch(workspaceID, folderID, "feature/new-work")
	if err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if view.Repository == nil || view.Repository.CurrentBranch != "feature/new-work" || !view.Repository.Dirty {
		t.Fatalf("expected new dirty branch, got %#v", view.Repository)
	}
	if content, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || string(content) != "dirty\n" {
		t.Fatalf("expected dirty file to remain, got %q err=%v", content, err)
	}
}

func TestSwitchWorkspaceGitBranchRequiresCleanRepository(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "notes.txt", "base\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	baseBranch := strings.TrimSpace(runGitTestCommand(t, root, "branch", "--show-current"))
	runGitTestCommand(t, root, "checkout", "-b", "feature/switch")
	writeGitTestFile(t, root, "feature.txt", "feature\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "feature")
	runGitTestCommand(t, root, "checkout", baseBranch)
	writeGitTestFile(t, root, "dirty.txt", "dirty\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	if _, err := service.SwitchWorkspaceGitBranch(workspaceID, folderID, "feature/switch"); err == nil || !strings.Contains(err.Error(), "commit or discard") {
		t.Fatalf("expected dirty switch failure, got %v", err)
	}

	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "clean before switch")
	view, err := service.SwitchWorkspaceGitBranch(workspaceID, folderID, "feature/switch")
	if err != nil {
		t.Fatalf("switch branch: %v", err)
	}
	if view.Repository == nil || view.Repository.CurrentBranch != "feature/switch" {
		t.Fatalf("expected switched branch, got %#v", view.Repository)
	}
}

func TestMergeWorkspaceGitBranchRequiresCleanRepositoryAndMerges(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "base.txt", "base\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	baseBranch := strings.TrimSpace(runGitTestCommand(t, root, "branch", "--show-current"))
	runGitTestCommand(t, root, "checkout", "-b", "feature/merge")
	writeGitTestFile(t, root, "feature.txt", "feature\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "feature")
	runGitTestCommand(t, root, "checkout", baseBranch)
	writeGitTestFile(t, root, "dirty.txt", "dirty\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	if _, err := service.MergeWorkspaceGitBranch(workspaceID, folderID, "feature/merge"); err == nil || !strings.Contains(err.Error(), "commit or discard") {
		t.Fatalf("expected dirty merge failure, got %v", err)
	}

	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "clean before merge")
	view, err := service.MergeWorkspaceGitBranch(workspaceID, folderID, "feature/merge")
	if err != nil {
		t.Fatalf("merge branch: %v", err)
	}
	if view.Repository == nil || view.Repository.CurrentBranch != baseBranch || view.Repository.Dirty {
		t.Fatalf("expected clean merged base branch, got %#v", view.Repository)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); err != nil {
		t.Fatalf("expected merged feature file: %v", err)
	}
}

func TestSyncWorkspaceGitBranchReportsCountsAndSyncsUpstream(t *testing.T) {
	parent := t.TempDir()
	bare := filepath.Join(parent, "origin.git")
	runGitTestCommand(t, parent, "init", "--bare", bare)

	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "base.txt", "base\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	baseBranch := strings.TrimSpace(runGitTestCommand(t, root, "branch", "--show-current"))
	runGitTestCommand(t, root, "remote", "add", "origin", bare)
	runGitTestCommand(t, root, "push", "-u", "origin", baseBranch)
	runGitTestCommand(t, bare, "symbolic-ref", "HEAD", "refs/heads/"+baseBranch)

	other := filepath.Join(parent, "other")
	runGitTestCommand(t, parent, "clone", bare, other)
	runGitTestCommand(t, other, "config", "user.name", "Echo Test")
	runGitTestCommand(t, other, "config", "user.email", "echo@example.test")
	runGitTestCommand(t, other, "config", "core.autocrlf", "false")

	writeGitTestFile(t, root, "outgoing.txt", "outgoing\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "outgoing")
	writeGitTestFile(t, other, "incoming.txt", "incoming\n")
	runGitTestCommand(t, other, "add", ".")
	runGitTestCommand(t, other, "commit", "-m", "incoming")
	runGitTestCommand(t, other, "push")
	runGitTestCommand(t, root, "fetch", "origin")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if view.Repository == nil || view.Repository.Upstream != "origin/"+baseBranch || view.Repository.AheadCount != 1 || view.Repository.BehindCount != 1 {
		t.Fatalf("expected one commit up and one down, got %#v", view.Repository)
	}

	writeGitTestFile(t, root, "dirty.txt", "dirty\n")
	if _, err := service.SyncWorkspaceGitBranch(workspaceID, folderID); err == nil || !strings.Contains(err.Error(), "incoming commits") {
		t.Fatalf("expected dirty incoming sync failure, got %v", err)
	}
	if err := os.Remove(filepath.Join(root, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	view, err = service.SyncWorkspaceGitBranch(workspaceID, folderID)
	if err != nil {
		t.Fatalf("sync branch: %v", err)
	}
	if view.Repository == nil || view.Repository.AheadCount != 0 || view.Repository.BehindCount != 0 || view.Repository.Dirty {
		t.Fatalf("expected synced clean repository, got %#v", view.Repository)
	}
	runGitTestCommand(t, other, "pull", "--ff-only")
	if _, err := os.Stat(filepath.Join(other, "outgoing.txt")); err != nil {
		t.Fatalf("expected outgoing commit pushed to remote: %v", err)
	}
}

func TestLoadWorkspaceGitCommitReturnsCommitDiff(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "notes.txt", "one\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	writeGitTestFile(t, root, "notes.txt", "two\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "edit notes")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if view.Repository == nil || len(view.Repository.Commits) == 0 {
		t.Fatalf("expected history, got %#v", view.Repository)
	}
	detail, err := service.LoadWorkspaceGitCommit(workspaceID, folderID, view.Repository.Commits[0].Hash)
	if err != nil {
		t.Fatalf("load commit detail: %v", err)
	}
	if detail.Commit.Subject != "edit notes" || detail.FileCount != 1 {
		t.Fatalf("unexpected commit detail: %#v", detail)
	}
	file := detail.Files[0]
	if !strings.Contains(file.Path, "notes.txt") || !strings.Contains(file.Diff, "-one") || !strings.Contains(file.Diff, "+two") {
		t.Fatalf("unexpected commit file diff: %#v", file)
	}
}

func TestLoadWorkspaceGitRepositorySelectsRepositoryByFolderID(t *testing.T) {
	first := newGitTestRepo(t)
	writeGitTestFile(t, first, "first.txt", "first\n")
	runGitTestCommand(t, first, "add", ".")
	runGitTestCommand(t, first, "commit", "-m", "first")
	second := newGitTestRepo(t)
	writeGitTestFile(t, second, "second.txt", "second\n")
	runGitTestCommand(t, second, "add", ".")
	runGitTestCommand(t, second, "commit", "-m", "second")

	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(first)
	if err != nil {
		t.Fatalf("add first workspace: %v", err)
	}
	state, err = service.AddWorkspaceFolder(state.ActiveWorkspaceID, second)
	if err != nil {
		t.Fatalf("add second folder: %v", err)
	}
	workspace := state.Workspaces[0]
	if len(workspace.Folders) != 2 {
		t.Fatalf("expected two folders, got %#v", workspace.Folders)
	}
	secondID := workspace.Folders[1].ID
	view, err := service.LoadWorkspaceGitRepository(workspace.ID, secondID)
	if err != nil {
		t.Fatalf("load selected repository: %v", err)
	}
	if view.SelectedFolderID != secondID || view.Repository == nil || view.Repository.FolderID != secondID {
		t.Fatalf("expected second repo selected, got %#v", view)
	}
	if len(view.Repositories) != 2 {
		t.Fatalf("expected two repo summaries, got %#v", view.Repositories)
	}
}

func TestParseGitStatusPorcelainHandlesRenamesCopiesAndConflicts(t *testing.T) {
	entries, err := parseGitStatusPorcelain([]byte("R  new.txt\x00old.txt\x00C  copy.txt\x00base.txt\x00UU conflict.txt\x00"))
	if err != nil {
		t.Fatalf("parse status: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three entries, got %#v", entries)
	}
	if entries[0].path != "new.txt" || entries[0].oldPath != "old.txt" || gitStatusOperation(entries[0].index, entries[0].worktree) != "renamed" {
		t.Fatalf("unexpected rename entry: %#v", entries[0])
	}
	if entries[1].path != "copy.txt" || entries[1].oldPath != "base.txt" || gitStatusOperation(entries[1].index, entries[1].worktree) != "copied" {
		t.Fatalf("unexpected copy entry: %#v", entries[1])
	}
	if entries[2].path != "conflict.txt" || gitStatusOperation(entries[2].index, entries[2].worktree) != "conflicted" {
		t.Fatalf("unexpected conflict entry: %#v", entries[2])
	}
}

func TestParseGitNameStatusHandlesRenamesCopiesAndRegularFiles(t *testing.T) {
	entries, err := parseGitNameStatus([]byte("M\x00edit.txt\x00R100\x00old.txt\x00new.txt\x00C100\x00base.txt\x00copy.txt\x00D\x00gone.txt\x00"))
	if err != nil {
		t.Fatalf("parse name-status: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected four entries, got %#v", entries)
	}
	if entries[0].status != "M" || entries[0].path != "edit.txt" || gitNameStatusOperation(entries[0].status) != tools.FileChangeEdited {
		t.Fatalf("unexpected edit entry: %#v", entries[0])
	}
	if entries[1].status != "R100" || entries[1].oldPath != "old.txt" || entries[1].path != "new.txt" || gitNameStatusOperation(entries[1].status) != "renamed" {
		t.Fatalf("unexpected rename entry: %#v", entries[1])
	}
	if entries[2].status != "C100" || entries[2].oldPath != "base.txt" || entries[2].path != "copy.txt" || gitNameStatusOperation(entries[2].status) != "copied" {
		t.Fatalf("unexpected copy entry: %#v", entries[2])
	}
	if entries[3].status != "D" || entries[3].path != "gone.txt" || gitNameStatusOperation(entries[3].status) != tools.FileChangeDeleted {
		t.Fatalf("unexpected delete entry: %#v", entries[3])
	}
}

func newGitTestRepo(t *testing.T) string {
	t.Helper()
	requireGitTestExecutable(t)
	root := t.TempDir()
	runGitTestCommand(t, root, "init")
	runGitTestCommand(t, root, "config", "user.name", "Echo Test")
	runGitTestCommand(t, root, "config", "user.email", "echo@example.test")
	runGitTestCommand(t, root, "config", "core.autocrlf", "false")
	return root
}

func requireGitTestExecutable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
}

func runGitTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-c", "safe.directory=*", "-C", root}, args...)
	cmd := exec.Command("git", commandArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
	return string(output)
}

func writeGitTestFile(t *testing.T, root string, path string, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadGitChangesForTestWorkspace(t *testing.T, root string) WorkspaceGitChangeReview {
	t.Helper()
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	review, err := service.LoadWorkspaceGitChanges(state.ActiveWorkspaceID)
	if err != nil {
		t.Fatalf("load git changes: %v", err)
	}
	return review
}

func gitReviewFilesByPath(review WorkspaceGitChangeReview) map[string]WorkspaceGitChangedFile {
	files := map[string]WorkspaceGitChangedFile{}
	for _, file := range review.Files {
		files[file.Path] = file
	}
	return files
}

func newGitRepositoryTestService(t *testing.T, root string) (*SystemService, string, string) {
	t.Helper()
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if len(state.Workspaces) == 0 || len(state.Workspaces[0].Folders) == 0 {
		t.Fatalf("expected workspace folder, got %#v", state.Workspaces)
	}
	return service, state.ActiveWorkspaceID, state.Workspaces[0].Folders[0].ID
}

func gitBranchesContain(branches []WorkspaceGitBranch, name string, current bool) bool {
	for _, branch := range branches {
		if branch.Name == name && branch.Current == current {
			return true
		}
	}
	return false
}
