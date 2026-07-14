package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWorkspaceGitActionCommitTagStashAndRemoteMetadata(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "base.txt", "base\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")
	service, workspaceID, folderID := newGitRepositoryTestService(t, root)

	writeGitTestFile(t, root, "signed.txt", "signed\n")
	view, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{
		Action:  "commit_all_signoff",
		Message: "signed commit",
	})
	if err != nil {
		t.Fatalf("signed commit: %v", err)
	}
	if view.Repository == nil || view.Repository.Dirty {
		t.Fatalf("expected clean repository after commit, got %#v", view.Repository)
	}
	if body := runGitTestCommand(t, root, "log", "-1", "--format=%B"); !strings.Contains(body, "Signed-off-by: Echo Test <echo@example.test>") {
		t.Fatalf("expected sign-off trailer, got %q", body)
	}

	if _, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "create_tag", Name: "v1.0.0", Message: "release"}); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	bare := filepath.Join(t.TempDir(), "origin.git")
	runGitTestCommand(t, filepath.Dir(bare), "init", "--bare", bare)
	if _, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "add_remote", Name: "origin", URL: bare}); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	writeGitTestFile(t, root, "stashed.txt", "stash\n")
	view, err = service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "stash_untracked", Message: "saved work"})
	if err != nil {
		t.Fatalf("stash: %v", err)
	}
	if view.Repository == nil || len(view.Repository.Tags) != 1 || len(view.Repository.Remotes) != 1 || len(view.Repository.Stashes) != 1 {
		t.Fatalf("expected tag, remote, and stash metadata, got %#v", view.Repository)
	}
	detail, err := service.LoadWorkspaceGitStash(workspaceID, folderID, view.Repository.Stashes[0].Ref)
	if err != nil {
		t.Fatalf("load stash: %v", err)
	}
	if detail.Stash.Message != "saved work" {
		t.Fatalf("unexpected stash detail: %#v", detail)
	}
	if detail.FileCount != 1 || len(detail.Files) != 1 || !strings.HasSuffix(detail.Files[0].Path, "/stashed.txt") {
		t.Fatalf("expected untracked stash file and diff, got %#v", detail.Files)
	}
}

func TestRunWorkspaceGitActionRejectsUnknownActionsAndUnsafeRefs(t *testing.T) {
	root := newGitTestRepo(t)
	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	if _, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "force_push"}); err == nil {
		t.Fatal("expected unknown action to fail")
	}
	if _, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "checkout", Ref: "--help"}); err == nil {
		t.Fatal("expected option-shaped ref to fail")
	}
	if _, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{Action: "stage_folder", Ref: "../outside"}); err == nil {
		t.Fatal("expected an escaping folder path to fail")
	}
}

func TestRunWorkspaceGitActionStagesAndUnstagesFolder(t *testing.T) {
	root := newGitTestRepo(t)
	writeGitTestFile(t, root, "src/one.txt", "one\n")
	writeGitTestFile(t, root, "src/nested/two.txt", "two\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs\n")
	runGitTestCommand(t, root, "add", ".")
	runGitTestCommand(t, root, "commit", "-m", "initial")

	writeGitTestFile(t, root, "src/one.txt", "one changed\n")
	writeGitTestFile(t, root, "src/nested/two.txt", "two changed\n")
	writeGitTestFile(t, root, "docs/readme.txt", "docs changed\n")
	service, workspaceID, folderID := newGitRepositoryTestService(t, root)
	label := normalizeWorkspaceFolderLabel(filepath.Base(root))

	view, err := service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{
		Action: "stage_folder",
		Ref:    label + "/src",
	})
	if err != nil {
		t.Fatalf("stage folder: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 2 || view.Repository.UnstagedFileCount != 1 {
		t.Fatalf("expected only source files staged, got %#v", view.Repository)
	}
	status := runGitTestCommand(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "M  src/one.txt") || !strings.Contains(status, "M  src/nested/two.txt") || !strings.Contains(status, " M docs/readme.txt") {
		t.Fatalf("unexpected status after staging folder: %q", status)
	}

	view, err = service.RunWorkspaceGitAction(workspaceID, folderID, WorkspaceGitActionRequest{
		Action: "unstage_folder",
		Ref:    label + "/src",
	})
	if err != nil {
		t.Fatalf("unstage folder: %v", err)
	}
	if view.Repository == nil || view.Repository.StagedFileCount != 0 || view.Repository.UnstagedFileCount != 3 {
		t.Fatalf("expected all files unstaged, got %#v", view.Repository)
	}
}

func TestCloneWorkspaceGitRepositoryAddsFolderToCurrentWorkspace(t *testing.T) {
	source := newGitTestRepo(t)
	writeGitTestFile(t, source, "README.md", "hello\n")
	runGitTestCommand(t, source, "add", ".")
	runGitTestCommand(t, source, "commit", "-m", "initial")
	workspaceRoot := t.TempDir()
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(workspaceRoot)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	parent := t.TempDir()
	state, err = service.CloneWorkspaceGitRepository(state.ActiveWorkspaceID, source, parent, "clone")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(state.Workspaces) != 1 || len(state.Workspaces[0].Folders) != 2 {
		t.Fatalf("expected cloned folder in current workspace, got %#v", state.Workspaces)
	}
	if _, err := os.Stat(filepath.Join(parent, "clone", "README.md")); err != nil {
		t.Fatalf("expected cloned file: %v", err)
	}
}
