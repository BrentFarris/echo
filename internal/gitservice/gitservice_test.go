package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestParseStatusPorcelainV2(t *testing.T) {
	data := []byte("# branch.oid abc123\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +2 -1\n" +
		"1 .M N... 100644 100644 100644 a b file with spaces.txt\x00" +
		"2 R. N... 100644 100644 100644 a b R100 new.txt\x00old.txt\x00" +
		"u UU N... 100644 100644 100644 100644 a b c conflict.txt\x00" +
		"? untracked.txt\x00")
	status, err := parseStatusPorcelainV2(data)
	if err != nil {
		t.Fatalf("parse status: %v", err)
	}
	if status.branch != "main" || status.upstream != "origin/main" || status.ahead != 2 || status.behind != 1 {
		t.Fatalf("unexpected branch status: %#v", status)
	}
	if len(status.records) != 4 {
		t.Fatalf("expected four records, got %#v", status.records)
	}
	if status.records[0].path != "file with spaces.txt" || status.records[0].worktree != 'M' {
		t.Fatalf("unexpected ordinary record: %#v", status.records[0])
	}
	if status.records[1].path != "new.txt" || status.records[1].oldPath != "old.txt" {
		t.Fatalf("unexpected rename: %#v", status.records[1])
	}
	if !status.records[2].conflict || status.records[3].worktree != '?' {
		t.Fatalf("unexpected conflict/untracked records: %#v", status.records)
	}
}

func TestStatusParserPreservesUnicodeNewlinesAndLeadingSpaces(t *testing.T) {
	path := " leading 日本語\nname.txt"
	status, err := parseStatusPorcelainV2([]byte("? " + path + "\x00"))
	if err != nil || len(status.records) != 1 || status.records[0].path != path {
		t.Fatalf("newline/unicode path was not preserved: %#v err=%v", status.records, err)
	}
	clean, err := cleanGitPath(path)
	if err != nil || clean != path {
		t.Fatalf("clean path changed a valid name: %q err=%v", clean, err)
	}
	for _, invalid := range []string{"../outside.txt", "folder/../../outside.txt", "\x00bad"} {
		if _, err := cleanGitPath(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestDiscoveryStatusStageUnstageAndTrash(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()

	writeTestFile(t, root, "tracked.txt", "before\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "initial")
	writeTestFile(t, root, "tracked.txt", "after\n")
	writeTestFile(t, root, "new file.txt", "new\n")

	repositories, err := service.Repositories(context.Background(), workspaceID)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("discover repositories: %#v err=%v", repositories, err)
	}
	repositoryID := repositories[0].ID
	status, err := service.Status(context.Background(), workspaceID, repositoryID)
	if err != nil {
		t.Fatalf("load status: %v", err)
	}
	if status.Branch != "main" || len(status.Unstaged) != 2 || len(status.Staged) != 0 {
		t.Fatalf("unexpected initial status: %#v", status)
	}

	result, err := service.Action(context.Background(), workspaceID, repositoryID, ActionRequest{
		RequestID: "stage-1", Action: "stage", Paths: []string{"tracked.txt", "new file.txt"},
	})
	if err != nil || result.Revision <= status.Revision {
		t.Fatalf("stage: %#v err=%v", result, err)
	}
	status, err = service.Status(context.Background(), workspaceID, repositoryID)
	if err != nil || len(status.Staged) != 2 || len(status.Unstaged) != 0 {
		t.Fatalf("status after stage: %#v err=%v", status, err)
	}

	if _, err := service.Action(context.Background(), workspaceID, repositoryID, ActionRequest{
		RequestID: "unstage-1", Action: "unstage", Paths: []string{"new file.txt"},
	}); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	status, _ = service.Status(context.Background(), workspaceID, repositoryID)
	if len(status.Staged) != 1 || len(status.Unstaged) != 1 || status.Unstaged[0].StatusCode != "?" {
		t.Fatalf("status after unstage: %#v", status)
	}

	discard, err := service.Action(context.Background(), workspaceID, repositoryID, ActionRequest{
		RequestID: "discard-1", Action: "discard", Paths: []string{"new file.txt"}, Confirmed: true,
	})
	if err != nil || len(discard.TrashIDs) != 1 {
		t.Fatalf("trash untracked: %#v err=%v", discard, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new file.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected untracked file in trash, stat err=%v", err)
	}
	if _, err := fs.Restore(workspaceID, discard.TrashIDs[0]); err != nil {
		t.Fatalf("restore trash: %v", err)
	}
}

func TestStagedAndUnstagedDiffs(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()
	writeTestFile(t, root, "file.go", "package sample\n\nvar Value = 1\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "initial")
	writeTestFile(t, root, "file.go", "package sample\n\nvar Value = 2\n")
	gitTestCommand(t, root, "add", "file.go")
	writeTestFile(t, root, "file.go", "package sample\n\nvar Value = 3\n")
	repositories, _ := service.Repositories(context.Background(), workspaceID)

	staged, err := service.Diff(context.Background(), workspaceID, repositories[0].ID, "staged", "file.go", "", "")
	if err != nil || !strings.Contains(staged.Original.Content, "Value = 1") || !strings.Contains(staged.Modified.Content, "Value = 2") || staged.Editable {
		t.Fatalf("staged diff: %#v err=%v", staged, err)
	}
	unstaged, err := service.Diff(context.Background(), workspaceID, repositories[0].ID, "unstaged", "file.go", "", "")
	if err != nil || !strings.Contains(unstaged.Original.Content, "Value = 2") || !strings.Contains(unstaged.Modified.Content, "Value = 3") || !unstaged.Editable {
		t.Fatalf("unstaged diff: %#v err=%v", unstaged, err)
	}
}

func TestTrackedDiscardRestoresIndexWithoutLosingStagedContent(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()
	writeTestFile(t, root, "tracked.txt", "head\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "initial")
	writeTestFile(t, root, "tracked.txt", "index\n")
	gitTestCommand(t, root, "add", "tracked.txt")
	writeTestFile(t, root, "tracked.txt", "worktree\n")
	repositories, _ := service.Repositories(context.Background(), workspaceID)

	_, err := service.Action(context.Background(), workspaceID, repositories[0].ID, ActionRequest{
		RequestID: "discard-tracked", Action: "discard", Paths: []string{"tracked.txt"}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("discard tracked worktree side: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || strings.ReplaceAll(string(content), "\r\n", "\n") != "index\n" {
		t.Fatalf("worktree was not restored from index: %q err=%v", content, err)
	}
	status, err := service.Status(context.Background(), workspaceID, repositories[0].ID)
	if err != nil || len(status.Staged) != 1 || len(status.Unstaged) != 0 {
		t.Fatalf("staged content was not preserved: %#v err=%v", status, err)
	}
}

func TestTrackedDiscardDoesNotResurfaceFilterMismatch(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()
	writeTestFile(t, root, "asset.bin", "committed binary")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "add binary")

	// Model an LFS-style clean filter being added without renormalizing the
	// already-committed binary. The filter output can never match its raw blob.
	gitTestCommand(t, root, "config", "filter.echo-test.clean", "git hash-object --stdin")
	writeTestFile(t, root, ".gitattributes", "*.bin filter=echo-test -text\n")
	gitTestCommand(t, root, "add", ".gitattributes")
	gitTestCommand(t, root, "commit", "-m", "add binary filter")
	writeTestFile(t, root, "asset.bin", "changed binary")

	repositories, err := service.Repositories(context.Background(), workspaceID)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("discover repository: %#v err=%v", repositories, err)
	}
	repositoryID := repositories[0].ID
	status, err := service.Status(context.Background(), workspaceID, repositoryID)
	if err != nil || len(status.Unstaged) != 1 || status.Unstaged[0].Path != "asset.bin" {
		t.Fatalf("expected changed binary before discard: %#v err=%v", status, err)
	}

	_, err = service.Action(context.Background(), workspaceID, repositoryID, ActionRequest{
		RequestID: "discard-filtered", Action: "discard", Paths: []string{"asset.bin"}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("discard filtered binary: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "asset.bin"))
	if err != nil || string(content) != "committed binary" {
		t.Fatalf("binary was not restored: %q err=%v", content, err)
	}
	if raw := gitTestCommand(t, root, "status", "--porcelain", "--", "asset.bin"); !strings.Contains(raw, "asset.bin") {
		t.Fatalf("test did not reproduce Git's filter mismatch: %q", raw)
	}
	status, err = service.Status(context.Background(), workspaceID, repositoryID)
	if err != nil || len(status.Unstaged) != 0 {
		t.Fatalf("restored binary resurfaced in Echo status: %#v err=%v", status, err)
	}
}

func TestUnbornDetachedAndRenamedDiffs(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()
	repositories, _ := service.Repositories(context.Background(), workspaceID)
	status, err := service.Status(context.Background(), workspaceID, repositories[0].ID)
	if err != nil || status.Head != "" || status.Branch != "main" || status.Detached {
		t.Fatalf("unexpected unborn status: %#v err=%v", status, err)
	}
	writeTestFile(t, root, "old name.txt", "shared one\nshared two\nshared three\nbefore\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "initial")
	if err := os.Rename(filepath.Join(root, "old name.txt"), filepath.Join(root, "new 日本語.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "new 日本語.txt", "shared one\nshared two\nshared three\nafter\n")
	gitTestCommand(t, root, "add", "-A")
	status, err = service.Status(context.Background(), workspaceID, repositories[0].ID)
	if err != nil || len(status.Staged) != 1 || status.Staged[0].OldPath != "old name.txt" {
		t.Fatalf("unexpected rename status: %#v err=%v", status, err)
	}
	diff, err := service.Diff(context.Background(), workspaceID, repositories[0].ID, "staged", "new 日本語.txt", "old name.txt", "")
	if err != nil || !strings.Contains(diff.Original.Content, "before") || !strings.Contains(diff.Modified.Content, "after") {
		t.Fatalf("renamed file diff: %#v err=%v", diff, err)
	}
	gitTestCommand(t, root, "commit", "-m", "rename")
	gitTestCommand(t, root, "checkout", "--detach", "HEAD")
	status, err = service.Status(context.Background(), workspaceID, repositories[0].ID)
	if err != nil || !status.Detached || status.Branch != "detached" || status.Head == "" {
		t.Fatalf("unexpected detached status: %#v err=%v", status, err)
	}
	if _, err := service.Diff(context.Background(), workspaceID, repositories[0].ID, "staged", "../outside.txt", "", ""); err == nil {
		t.Fatal("expected diff traversal to be rejected")
	}
}

func TestBranchRemoteTagStashAndCommitActions(t *testing.T) {
	service, fs, workspaceID, root := newGitServiceTestWorkspace(t)
	defer service.Close()
	defer fs.Close()
	gitTestCommand(t, root, "config", "user.name", "Echo Test")
	gitTestCommand(t, root, "config", "user.email", "echo@example.com")
	writeTestFile(t, root, "tracked.txt", "base\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "initial")
	repositories, _ := service.Repositories(context.Background(), workspaceID)
	repositoryID := repositories[0].ID
	run := func(id, action string, configure func(*ActionRequest)) {
		t.Helper()
		request := ActionRequest{RequestID: id, Action: action}
		if configure != nil {
			configure(&request)
		}
		if _, err := service.Action(context.Background(), workspaceID, repositoryID, request); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	run("branch", "create_branch", func(request *ActionRequest) { request.Name = "feature" })
	run("rename", "rename_branch", func(request *ActionRequest) { request.Name = "topic" })
	writeTestFile(t, root, "tracked.txt", "committed\n")
	run("stage", "stage", func(request *ActionRequest) { request.Paths = []string{"tracked.txt"} })
	run("commit", "commit_staged_signoff", func(request *ActionRequest) { request.Message = "action commit" })
	run("tag", "create_tag", func(request *ActionRequest) { request.Name = "v1.0.0" })

	bare := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, bare, "init", "--bare")
	run("remote", "add_remote", func(request *ActionRequest) { request.Name, request.URL = "origin", bare })
	run("publish", "publish_branch", func(request *ActionRequest) { request.Remote = "origin" })
	run("push-tags", "push_tags", func(request *ActionRequest) { request.Remote = "origin" })

	writeTestFile(t, root, "tracked.txt", "stashed\n")
	run("stash", "stash", func(request *ActionRequest) { request.Message = "saved work" })
	metadata, err := service.Metadata(context.Background(), workspaceID, repositoryID)
	if err != nil || len(metadata.Stashes) != 1 || metadata.Stashes[0].Message == "" || len(metadata.Tags) != 1 || len(metadata.Remotes) != 1 {
		t.Fatalf("lazy metadata after actions: %#v err=%v", metadata, err)
	}
	run("apply", "apply_latest_stash", nil)
	run("discard", "discard", func(request *ActionRequest) { request.Paths, request.Confirmed = []string{"tracked.txt"}, true })
	run("drop", "drop_stash", func(request *ActionRequest) { request.Ref = metadata.Stashes[0].Ref })
	run("delete-remote", "delete_remote_branch", func(request *ActionRequest) { request.Remote, request.Ref = "origin", "topic" })
	run("remove-remote", "remove_remote", func(request *ActionRequest) { request.Name = "origin" })
	run("delete-tag", "delete_tag", func(request *ActionRequest) { request.Name = "v1.0.0" })
}

func TestParentRepositoryScopeBlocksHiddenStagedCommit(t *testing.T) {
	requireGit(t)
	repositoryRoot := t.TempDir()
	gitTestCommand(t, repositoryRoot, "init", "-b", "main")
	writeTestFile(t, repositoryRoot, ".gitignore", ".echo/\n")
	writeTestFile(t, repositoryRoot, "app/main.txt", "before\n")
	writeTestFile(t, repositoryRoot, "docs/readme.txt", "before\n")
	gitTestCommand(t, repositoryRoot, "add", ".")
	gitTestCommand(t, repositoryRoot, "commit", "-m", "initial")

	workspaceRoot := filepath.Join(repositoryRoot, "app")
	settingsPath := filepath.Join(t.TempDir(), "echo.json")
	manager := workspaces.NewManager(settingsPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "parent", MainPath: workspaceRoot})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := manager.SetSearchParentGitRepositories(workspace.ID, true); err != nil {
		t.Fatalf("enable parent repositories: %v", err)
	}
	fs := workspacefs.New(manager, settingsPath)
	defer fs.Close()
	service := New(manager, fs)
	defer service.Close()
	writeTestFile(t, repositoryRoot, "app/main.txt", "after\n")
	writeTestFile(t, repositoryRoot, "docs/readme.txt", "after\n")
	gitTestCommand(t, repositoryRoot, "add", "docs/readme.txt")

	repositories, err := service.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repositories) != 1 || !repositories[0].Parent {
		t.Fatalf("discover parent: %#v err=%v", repositories, err)
	}
	status, err := service.Status(context.Background(), workspace.ID, repositories[0].ID)
	if err != nil || status.HiddenStagedCount != 1 || len(status.Unstaged) != 1 {
		t.Fatalf("scoped parent status: %#v err=%v", status, err)
	}
	_, err = service.Action(context.Background(), workspace.ID, repositories[0].ID, ActionRequest{
		RequestID: "commit-hidden", Action: "commit_staged", Message: "unsafe",
	})
	var gitError *Error
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") || !errors.As(err, &gitError) || gitError.Code != "hidden_staged_changes" {
		t.Fatalf("expected hidden staged rejection, got %v", err)
	}
}

func newGitServiceTestWorkspace(t *testing.T) (*Service, *workspacefs.Service, string, string) {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	settingsPath := filepath.Join(t.TempDir(), "echo.json")
	manager := workspaces.NewManager(settingsPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "test", MainPath: root})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	gitTestCommand(t, root, "init", "-b", "main")
	writeTestFile(t, root, ".gitignore", ".echo/\n")
	fs := workspacefs.New(manager, settingsPath)
	return New(manager, fs), fs, workspace.ID, root
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not installed")
	}
}

func gitTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Echo Test", "GIT_AUTHOR_EMAIL=echo@example.com", "GIT_COMMITTER_NAME=Echo Test", "GIT_COMMITTER_EMAIL=echo@example.com", "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
