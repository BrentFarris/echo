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

	keep := files["keep.txt"]
	if keep.Operation != tools.FileChangeEdited || keep.WorktreeStatus != "M" || !strings.Contains(keep.Diff, "-before") || !strings.Contains(keep.Diff, "+after") {
		t.Fatalf("unexpected modified file review: %#v", keep)
	}
	gone := files["gone.txt"]
	if gone.Operation != tools.FileChangeDeleted || gone.WorktreeStatus != "D" || !strings.Contains(gone.Diff, "-remove me") {
		t.Fatalf("unexpected deleted file review: %#v", gone)
	}
	fresh := files["fresh.txt"]
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

	staged := files["staged.txt"]
	if staged.Operation != tools.FileChangeEdited || staged.IndexStatus != "M" || staged.WorktreeStatus != "" || !strings.Contains(staged.Diff, "+after staged") {
		t.Fatalf("unexpected staged file review: %#v", staged)
	}
	worktree := files["worktree.txt"]
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
