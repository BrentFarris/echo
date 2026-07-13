package services

import (
	"fmt"
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

func TestLoadWorkspaceGitChangesIncludesUnignoredEchoSkillFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "README.md", "hello\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, ".echo/skills/planner/SKILL.md", "Use the local planning conventions.\n")

	review := loadGitChangesForTestWorkspace(t, root)
	files := gitReviewFilesByPath(review)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))

	skill := files[label+"/.echo/skills/planner/SKILL.md"]
	if skill.Operation != tools.FileChangeCreated || skill.Status != "??" || !strings.Contains(skill.Diff, "+Use the local planning conventions.") {
		t.Fatalf("expected unignored .echo skill file in git changes, got %#v", review.Files)
	}
}

func TestLoadWorkspaceGitChangesExcludesGitignoredEchoFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, ".gitignore", ".echo/\n")
	writeGitTestFile(t, root, "README.md", "hello\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, ".echo/skills/ignored/SKILL.md", "Ignore this skill.\n")

	review := loadGitChangesForTestWorkspace(t, root)
	files := gitReviewFilesByPath(review)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))
	if _, ok := files[label+"/.echo/skills/ignored/SKILL.md"]; ok {
		t.Fatalf("expected gitignored .echo skill file to be hidden, got %#v", review.Files)
	}
	if review.FileCount != 0 {
		t.Fatalf("expected no visible git changes, got %#v", review)
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
	if !view.Repository.Dirty || view.Repository.FileCount != 2 || view.Repository.UnstagedFileCount != 2 || view.Repository.StagedFileCount != 0 {
		t.Fatalf("expected dirty repo with two files, got %#v", view.Repository)
	}
	if !gitBranchesContain(view.Repository.Branches, baseBranch, true) || !gitBranchesContain(view.Repository.Branches, "feature/view", false) {
		t.Fatalf("expected local branches, got %#v", view.Repository.Branches)
	}
	if len(view.Repository.Commits) < 2 || view.Repository.Commits[0].Subject != "base work" {
		t.Fatalf("unexpected history: %#v", view.Repository.Commits)
	}
}

func TestLoadWorkspaceGitRepositoryDefersWorkingDiffs(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, ".gitignore", "ignored.txt\n")
	writeGitTestFile(t, root, "modified.txt", "before\n")
	writeGitTestFile(t, root, "deleted.txt", "deleted\n")
	writeGitTestFile(t, root, "renamed-old.txt", "rename me\n")
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "modified.txt", "after\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "renamed-old.txt"), filepath.Join(root, "renamed-new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 3, 4}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, root, "add", "-A")
	writeGitTestFile(t, root, "untracked.txt", "new text\n")
	writeGitTestFile(t, root, "ignored.txt", "ignored\n")
	writeGitTestFile(t, root, "large.txt", strings.Repeat("x", maxWorkspaceGitSyntheticDiffSize+1))

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if view.Repository == nil || view.Repository.FileCount != 6 {
		t.Fatalf("expected six visible changes, got %#v", view.Repository)
	}
	for _, file := range view.Repository.Files {
		if file.Diff != "" || file.DiffAvailable {
			t.Fatalf("expected deferred diff for %q, got %#v", file.Path, file)
		}
	}

	label := normalizeWorkspaceFolderLabel(filepath.Base(root))
	for _, test := range []struct {
		path     string
		contains string
	}{
		{label + "/modified.txt", "+after"},
		{label + "/deleted.txt", "-deleted"},
		{label + "/renamed-new.txt", "rename"},
		{label + "/untracked.txt", "+new text"},
	} {
		file, err := service.LoadWorkspaceGitFileDiff(workspaceID, folderID, test.path)
		if err != nil {
			t.Fatalf("load diff for %s: %v", test.path, err)
		}
		if !file.DiffAvailable || !strings.Contains(file.Diff, test.contains) {
			t.Fatalf("unexpected diff for %s: %#v", test.path, file)
		}
	}
	for _, path := range []string{label + "/binary.bin", label + "/large.txt"} {
		file, err := service.LoadWorkspaceGitFileDiff(workspaceID, folderID, path)
		if err != nil {
			t.Fatalf("load unavailable diff for %s: %v", path, err)
		}
		if file.DiffAvailable || file.Diff != "" {
			t.Fatalf("expected unavailable diff for %s, got %#v", path, file)
		}
	}
	if _, err := service.LoadWorkspaceGitFileDiff(workspaceID, folderID, label+"/ignored.txt"); err == nil {
		t.Fatal("expected ignored file diff to be rejected")
	}
	if _, err := service.LoadWorkspaceGitFileDiff(workspaceID, folderID, "../outside.txt"); err == nil {
		t.Fatal("expected out-of-scope diff path to be rejected")
	}
}

func TestLoadWorkspaceGitFileDiffForScopeSeparatesStagedAndUnstagedChanges(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "both.txt", "original\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "both.txt", "staged\n")
	runGitTestCommand(t, root, "add", "both.txt")
	writeGitTestFile(t, root, "both.txt", "unstaged\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	path := normalizeWorkspaceFolderLabel(filepath.Base(root)) + "/both.txt"
	staged, err := service.LoadWorkspaceGitFileDiffForScope(workspaceID, folderID, path, "staged")
	if err != nil {
		t.Fatalf("load staged diff: %v", err)
	}
	if !staged.DiffAvailable || !strings.Contains(staged.Diff, "+staged") || strings.Contains(staged.Diff, "+unstaged") {
		t.Fatalf("unexpected staged diff: %#v", staged)
	}
	if !staged.Staged || staged.Unstaged || staged.WorktreeStatus != "" {
		t.Fatalf("unexpected staged scope status: %#v", staged)
	}

	unstaged, err := service.LoadWorkspaceGitFileDiffForScope(workspaceID, folderID, path, "unstaged")
	if err != nil {
		t.Fatalf("load unstaged diff: %v", err)
	}
	if !unstaged.DiffAvailable || !strings.Contains(unstaged.Diff, "-staged") || !strings.Contains(unstaged.Diff, "+unstaged") || strings.Contains(unstaged.Diff, "-original") {
		t.Fatalf("unexpected unstaged diff: %#v", unstaged)
	}
	if unstaged.Staged || !unstaged.Unstaged || unstaged.IndexStatus != "" {
		t.Fatalf("unexpected unstaged scope status: %#v", unstaged)
	}
}

func TestWorkspaceGitStatusRefreshPreservesCachedHistory(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "notes.txt", "before\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial history")
	writeGitTestFile(t, root, "notes.txt", "after\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	initial, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if initial.Repository == nil || len(initial.Repository.Commits) == 0 {
		t.Fatalf("expected initial history, got %#v", initial.Repository)
	}
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))
	staged, err := service.StageWorkspaceGitFile(workspaceID, folderID, label+"/notes.txt")
	if err != nil {
		t.Fatalf("stage file: %v", err)
	}
	if staged.Repository == nil || len(staged.Repository.Commits) == 0 || staged.Repository.Commits[0].Subject != "initial history" {
		t.Fatalf("expected cached history after staging, got %#v", staged.Repository)
	}
	unstaged, err := service.UnstageWorkspaceGitFile(workspaceID, folderID, label+"/notes.txt")
	if err != nil {
		t.Fatalf("unstage file: %v", err)
	}
	if unstaged.Repository == nil || len(unstaged.Repository.Commits) == 0 || unstaged.Repository.Commits[0].Subject != "initial history" {
		t.Fatalf("expected cached history after unstaging, got %#v", unstaged.Repository)
	}
}

func TestLoadWorkspaceGitRepositoryManyFilesRemainStatusOnly(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "README.md", "initial\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	for i := 0; i < 100; i++ {
		writeGitTestFile(t, root, fmt.Sprintf("changes/file-%03d.txt", i), "changed\n")
	}

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if view.Repository == nil || view.Repository.FileCount != 100 {
		t.Fatalf("expected 100 status entries, got %#v", view.Repository)
	}
	for _, file := range view.Repository.Files {
		if file.DiffAvailable || file.Diff != "" {
			t.Fatalf("expected status-only file, got %#v", file)
		}
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
	if _, err := service.CommitWorkspaceGitChanges(workspaceID, folderID, "commit without staged changes"); err == nil || !strings.Contains(err.Error(), "stage Git changes") {
		t.Fatalf("expected commit without staged changes to fail, got %v", err)
	}
	if _, err := service.StageWorkspaceGitChanges(workspaceID, folderID); err != nil {
		t.Fatalf("stage all changes: %v", err)
	}
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

func TestStageAndUnstageWorkspaceGitFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "one.txt", "one\n")
	writeGitTestFile(t, root, "two.txt", "two\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "one.txt", "one staged\n")
	writeGitTestFile(t, root, "two.txt", "two unstaged\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))
	view, err := service.StageWorkspaceGitFile(workspaceID, folderID, label+"/one.txt")
	if err != nil {
		t.Fatalf("stage file: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 1 || view.Repository.UnstagedFileCount != 1 {
		t.Fatalf("expected one staged and one unstaged file, got %#v", view.Repository)
	}

	view, err = service.UnstageWorkspaceGitFile(workspaceID, folderID, label+"/one.txt")
	if err != nil {
		t.Fatalf("unstage file: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 0 || view.Repository.UnstagedFileCount != 2 {
		t.Fatalf("expected two unstaged files, got %#v", view.Repository)
	}

	view, err = service.StageWorkspaceGitChanges(workspaceID, folderID)
	if err != nil {
		t.Fatalf("stage all changes: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 2 || view.Repository.UnstagedFileCount != 0 {
		t.Fatalf("expected both files staged, got %#v", view.Repository)
	}

	writeGitTestFile(t, root, "two.txt", "two unstaged after staging\n")
	view, err = service.CommitWorkspaceGitChanges(workspaceID, folderID, "commit staged files")
	if err != nil {
		t.Fatalf("commit staged files: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 0 || view.Repository.UnstagedFileCount != 1 || view.Repository.FileCount != 1 {
		t.Fatalf("expected one unstaged file after commit, got %#v", view.Repository)
	}
	if status := runGitTestCommand(t, root, "status", "--porcelain=v1", "--untracked-files=all"); !strings.Contains(status, " M two.txt") {
		t.Fatalf("expected only two.txt to remain unstaged, got %q", status)
	}

	view, err = service.UnstageWorkspaceGitChanges(workspaceID, folderID)
	if err != nil {
		t.Fatalf("unstage all changes: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 0 || view.Repository.UnstagedFileCount != 1 {
		t.Fatalf("expected unstage all to leave worktree change unstaged, got %#v", view.Repository)
	}
}

func TestDiscardWorkspaceGitFileRevertsOneChangedFile(t *testing.T) {
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
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))
	view, err := service.DiscardWorkspaceGitFile(workspaceID, folderID, label+"/keep.txt")
	if err != nil {
		t.Fatalf("discard modified file: %v", err)
	}
	if view.Repository == nil || view.Repository.FileCount != 2 {
		t.Fatalf("expected two remaining changes, got %#v", view.Repository)
	}
	if content, err := os.ReadFile(filepath.Join(root, "keep.txt")); err != nil || string(content) != "before\n" {
		t.Fatalf("expected modified file restored, got %q err=%v", content, err)
	}
	if status := runGitTestCommand(t, root, "status", "--porcelain=v1", "-z", "--untracked-files=all"); strings.Contains(status, "keep.txt") {
		t.Fatalf("expected keep.txt to be clean, got %q", status)
	}

	view, err = service.DiscardWorkspaceGitFile(workspaceID, folderID, label+"/gone.txt")
	if err != nil {
		t.Fatalf("discard deleted file: %v", err)
	}
	if view.Repository == nil || view.Repository.FileCount != 1 {
		t.Fatalf("expected one remaining change, got %#v", view.Repository)
	}
	if content, err := os.ReadFile(filepath.Join(root, "gone.txt")); err != nil || string(content) != "remove\n" {
		t.Fatalf("expected deleted file restored, got %q err=%v", content, err)
	}

	view, err = service.DiscardWorkspaceGitFile(workspaceID, folderID, label+"/fresh.txt")
	if err != nil {
		t.Fatalf("discard untracked file: %v", err)
	}
	if view.Repository == nil || view.Repository.Dirty || view.Repository.FileCount != 0 {
		t.Fatalf("expected clean repo after discarding all files, got %#v", view.Repository)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected untracked file removed, got %v", err)
	}

	writeGitTestFile(t, root, "staged-add.txt", "new\n")
	runGitTestCommand(t, root, "add", "staged-add.txt")
	view, err = service.DiscardWorkspaceGitFile(workspaceID, folderID, label+"/staged-add.txt")
	if err != nil {
		t.Fatalf("discard staged added file: %v", err)
	}
	if view.Repository == nil || view.Repository.Dirty || view.Repository.FileCount != 0 {
		t.Fatalf("expected clean repo after discarding staged add, got %#v", view.Repository)
	}
	if _, err := os.Stat(filepath.Join(root, "staged-add.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected staged added file removed, got %v", err)
	}
}

func TestDiscardWorkspaceGitChangesRevertsTrackedStagedAndUntrackedFiles(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "keep.txt", "before\n")
	writeGitTestFile(t, root, "gone.txt", "remove\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "keep.txt", "after\n")
	runGitTestCommand(t, root, "add", "keep.txt")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, root, "fresh.txt", "fresh\n")

	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	view, err := service.DiscardWorkspaceGitChanges(workspaceID, folderID)
	if err != nil {
		t.Fatalf("discard all changes: %v", err)
	}
	if view.Repository == nil || view.Repository.Dirty || view.Repository.FileCount != 0 {
		t.Fatalf("expected clean repo, got %#v", view.Repository)
	}
	if content, err := os.ReadFile(filepath.Join(root, "keep.txt")); err != nil || string(content) != "before\n" {
		t.Fatalf("expected staged file restored, got %q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "gone.txt")); err != nil || string(content) != "remove\n" {
		t.Fatalf("expected deleted file restored, got %q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected untracked file removed, got %v", err)
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

func TestSwitchWorkspaceGitBranchLetsGitPreserveSafeDirtyFiles(t *testing.T) {
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
	view, err := service.SwitchWorkspaceGitBranch(workspaceID, folderID, "feature/switch")
	if err != nil {
		t.Fatalf("switch branch: %v", err)
	}
	if view.Repository == nil || view.Repository.CurrentBranch != "feature/switch" {
		t.Fatalf("expected switched branch, got %#v", view.Repository)
	}
	if content, err := os.ReadFile(filepath.Join(root, "dirty.txt")); err != nil || string(content) != "dirty\n" {
		t.Fatalf("expected dirty file to survive switch, got %q err=%v", content, err)
	}
}

func TestMergeWorkspaceGitBranchLetsGitPreserveSafeDirtyFiles(t *testing.T) {
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
	view, err := service.MergeWorkspaceGitBranch(workspaceID, folderID, "feature/merge")
	if err != nil {
		t.Fatalf("merge branch: %v", err)
	}
	if view.Repository == nil || view.Repository.CurrentBranch != baseBranch || !view.Repository.Dirty {
		t.Fatalf("expected dirty merged base branch, got %#v", view.Repository)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); err != nil {
		t.Fatalf("expected merged feature file: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "dirty.txt")); err != nil || string(content) != "dirty\n" {
		t.Fatalf("expected dirty file to survive merge, got %q err=%v", content, err)
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
	view, err = service.SyncWorkspaceGitBranch(workspaceID, folderID)
	if err != nil {
		t.Fatalf("sync branch with safe dirty file: %v", err)
	}
	if view.Repository == nil || view.Repository.AheadCount != 0 || view.Repository.BehindCount != 0 || !view.Repository.Dirty {
		t.Fatalf("expected synced repository with preserved dirty file, got %#v", view.Repository)
	}
	if err := os.Remove(filepath.Join(root, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	view, err = service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("reload synced branch: %v", err)
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

func TestWorkspaceGitParentRepositoryModeCachesRootAndScopesChanges(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, ".gitignore", ".echo/\n")
	writeGitTestFile(t, root, "app/main.txt", "app before\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs before\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "app/main.txt", "app after\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs after\n")

	appRoot := filepath.Join(root, "app")
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(appRoot)
	if err != nil {
		t.Fatalf("add child workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	folderID := state.Workspaces[0].Folders[0].ID

	if _, err := service.LoadWorkspaceGitRepository(workspaceID, folderID); err == nil || !strings.Contains(err.Error(), "workspace folder must be the Git repository root") {
		t.Fatalf("expected parent repository to be disabled by default, got %v", err)
	}

	state, err = service.SetWorkspaceSearchParentGitRepositories(workspaceID, true)
	if err != nil {
		t.Fatalf("enable parent repository search: %v", err)
	}
	view, err := service.LoadWorkspaceGitRepository(workspaceID, folderID)
	if err != nil {
		t.Fatalf("load parent repository: %v", err)
	}
	if view.Repository == nil || view.Repository.FileCount != 1 {
		t.Fatalf("expected one child-folder change, got %#v", view.Repository)
	}
	label := normalizeWorkspaceFolderLabel(filepath.Base(appRoot))
	if got := view.Repository.Files[0].Path; got != label+"/main.txt" {
		t.Fatalf("expected child-relative file path, got %q", got)
	}
	if strings.Contains(view.Repository.Files[0].Diff, "docs/readme.txt") {
		t.Fatalf("expected diff to exclude sibling folder changes, got %q", view.Repository.Files[0].Diff)
	}

	workspaceState, err := readWorkspaceStateFileAt(filepath.Join(appRoot, ".echo", workspaceStateFile))
	if err != nil {
		t.Fatalf("read workspace state: %v", err)
	}
	if len(workspaceState.Git.ParentRepositories) != 1 || !sameCleanPath(workspaceState.Git.ParentRepositories[0].RepositoryRoot, root) {
		t.Fatalf("expected cached parent repository root, got %#v", workspaceState.Git.ParentRepositories)
	}

	if _, err := service.StageWorkspaceGitChanges(workspaceID, folderID); err != nil {
		t.Fatalf("stage child changes: %v", err)
	}
	status := runGitTestCommand(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "M  app/main.txt") || !strings.Contains(status, " M docs/readme.txt") {
		t.Fatalf("expected only child change staged, got %q", status)
	}
	if _, err := service.UnstageWorkspaceGitChanges(workspaceID, folderID); err != nil {
		t.Fatalf("unstage child changes: %v", err)
	}
	status = runGitTestCommand(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, " M app/main.txt") || !strings.Contains(status, " M docs/readme.txt") {
		t.Fatalf("expected child and sibling changes unstaged, got %q", status)
	}
	if _, err := service.StageWorkspaceGitChanges(workspaceID, folderID); err != nil {
		t.Fatalf("restage child changes: %v", err)
	}

	if _, err := service.DiscardWorkspaceGitChanges(workspaceID, folderID); err != nil {
		t.Fatalf("discard child changes: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "app", "main.txt")); err != nil || string(content) != "app before\n" {
		t.Fatalf("expected child change restored, got %q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "docs", "readme.txt")); err != nil || string(content) != "docs after\n" {
		t.Fatalf("expected sibling change preserved, got %q err=%v", content, err)
	}
}

func TestWorkspaceGitParentRepositoryModeSupportsMultipleFoldersSharingRepo(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, ".gitignore", ".echo/\n")
	writeGitTestFile(t, root, "app/main.txt", "app before\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs before\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "app/main.txt", "app after\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs after\n")

	appRoot := filepath.Join(root, "app")
	docsRoot := filepath.Join(root, "docs")
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(appRoot)
	if err != nil {
		t.Fatalf("add app workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	state, err = service.AddWorkspaceFolder(workspaceID, docsRoot)
	if err != nil {
		t.Fatalf("add docs folder: %v", err)
	}
	if _, err := service.SetWorkspaceSearchParentGitRepositories(workspaceID, true); err != nil {
		t.Fatalf("enable parent repository search: %v", err)
	}

	review, err := service.LoadWorkspaceGitChanges(workspaceID)
	if err != nil {
		t.Fatalf("load shared parent repository changes: %v", err)
	}
	files := gitReviewFilesByPath(review)
	if len(files) != 2 {
		t.Fatalf("expected one change per workspace folder, got %#v", review.Files)
	}
	if _, ok := files["app/main.txt"]; !ok {
		t.Fatalf("expected app change, got %#v", files)
	}
	if _, ok := files["docs/readme.txt"]; !ok {
		t.Fatalf("expected docs change, got %#v", files)
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
