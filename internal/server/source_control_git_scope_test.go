package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestGitSourceControlScope(t *testing.T) {
	s := testServer(t)
	repository := t.TempDir()
	marker := filepath.Join(repository, ".git")
	root := filepath.Join(repository, "one", "two")
	for _, path := range []string{marker, root} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeGitMarker := func(directory string) {
		t.Helper()
		for _, name := range []string{"objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(directory, name), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(directory, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeGitMarker(marker)
	ignore := filepath.Join(repository, ".gitignore")
	if err := os.WriteFile(ignore, []byte("ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "git-scope", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if scope, err := s.gitSourceControlScope(bot, root); err != nil || scope.workTree != "" || len(scope.roots) != 1 || len(scope.readPaths) != 0 {
		t.Fatalf("disabled parent search: %+v %v", scope, err)
	}
	if _, err := s.gitSourceControlScope(bot, repository); err == nil {
		t.Fatal("disabled parent search authorized its working directory")
	}
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, repository} {
		scope, err := s.gitSourceControlScope(bot, directory)
		if err != nil || !sameGitPath(scope.workTree, repository) || !slices.Equal(scope.roots, []string{root, marker}) {
			t.Fatalf("parent scope: %+v %v", scope, err)
		}
		if !slices.Contains(scope.readPaths, repository) || !slices.Contains(scope.readPaths, filepath.Dir(root)) || !slices.Contains(scope.readPaths, ignore) {
			t.Fatalf("missing ancestor read access: %+v", scope)
		}
	}
	if _, err := s.gitSourceControlScope(bot, filepath.Dir(root)); err == nil {
		t.Fatal("authorized an ancestor other than the repository root")
	}
	if _, err := s.gitSourceControlScope(bot, t.TempDir()); err == nil {
		t.Fatal("authorized an unrelated repository directory")
	}
	// Bot Git commands receive the entire checkout, including sibling working
	// directories, without changing the bot's ordinary file access.
	for _, directory := range []string{root, repository, filepath.Dir(root)} {
		scope, err := s.gitCommandScope(bot, directory)
		if err != nil || !sameGitPath(scope.workTree, repository) || !slices.Equal(scope.roots, []string{repository}) || len(scope.readPaths) != 0 {
			t.Fatalf("whole-checkout command scope: %+v %v", scope, err)
		}
	}
	if s.Files.Allowed(bot, ignore) || s.Files.Allowed(bot, marker) {
		t.Fatal("Git process grant changed file-tool permissions")
	}
	other := t.TempDir()
	if _, err := s.gitCommandScope(bot, other); err == nil {
		t.Fatal("Git command authorized an unrelated checkout")
	}
	multi := bot
	multi.Roots = append(append([]string{}, bot.Roots...), other)
	if scope, err := s.gitCommandScope(multi, root); err != nil || !slices.Equal(scope.roots, []string{repository, other}) {
		t.Fatalf("lost another explicitly configured folder: %+v %v", scope, err)
	}
	privateCheckout := &Server{Dir: filepath.Join(repository, "private"), Store: s.Store, Files: s.Files}
	if _, err := privateCheckout.gitCommandScope(bot, root); err == nil {
		t.Fatal("whole-checkout grant included Echo private state")
	}
	if scope, err := s.gitCommandScope(domain.Bot{}, root); err != nil || len(scope.roots) != 0 {
		t.Fatalf("unbound Git acquired folder restrictions: %+v %v", scope, err)
	}
	privateServer := &Server{Dir: filepath.Join(marker, "private"), Files: s.Files}
	if _, _, err := privateServer.gitMetadataDirectories(bot, marker); err == nil {
		t.Fatal("metadata grant included Echo private state")
	}
	// A .git/commondir reference cannot redirect this grant to arbitrary data.
	if err := os.WriteFile(filepath.Join(marker, "commondir"), []byte(t.TempDir()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.gitSourceControlScope(bot, root); err == nil {
		t.Fatal("authorized unrelated common metadata")
	}
	if _, err := s.gitCommandScope(bot, root); err == nil {
		t.Fatal("whole-checkout grant authorized unrelated common metadata")
	}
	if err := os.Remove(filepath.Join(marker, "commondir")); err != nil {
		t.Fatal(err)
	}
	// Git skips incomplete .git directories when looking for a repository.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if scope, err := s.gitSourceControlScope(bot, root); err != nil || !sameGitPath(scope.workTree, repository) {
		t.Fatalf("incomplete nested marker hid the parent: %+v %v", scope, err)
	}
	// Discover the nearest repository again; do not reuse a parent grant after
	// an independent nested checkout appears in the bot folder.
	writeGitMarker(filepath.Join(root, ".git"))
	if _, err := s.gitSourceControlScope(bot, repository); err == nil {
		t.Fatal("stale parent repository remained authorized")
	}
	if scope, err := s.gitSourceControlScope(bot, root); err != nil || !sameGitPath(scope.workTree, root) || len(scope.roots) != 1 {
		t.Fatalf("nested repository scope: %+v %v", scope, err)
	}
	for _, head := range []string{"invalid HEAD\n", strings.Repeat("1", 40) + "\n", strings.Repeat("2", 64) + "\n"} {
		if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte(head), 0600); err != nil {
			t.Fatal(err)
		}
		want := root
		if strings.HasPrefix(head, "invalid") {
			want = repository
		}
		if scope, err := s.gitSourceControlScope(bot, root); err != nil || !sameGitPath(scope.workTree, want) {
			t.Fatalf("repository with HEAD %q: %+v %v", head, scope, err)
		}
	}
	if err := os.Remove(filepath.Join(root, ".git", "refs")); err != nil {
		t.Fatal(err)
	}
	if scope, err := s.gitSourceControlScope(bot, root); err != nil || !sameGitPath(scope.workTree, repository) {
		t.Fatalf("incomplete nested metadata hid the parent: %+v %v", scope, err)
	}
}

func TestEditorSourceControlNativeGitParentFolders(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for contained CLI fixtures")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	repository := t.TempDir()
	prefixes := []string{"Nightfall/Source/NightfallModding", "Nightfall/Content/Maps/Modding/LuaEditorCode"}
	var roots []string
	for _, prefix := range prefixes {
		root := filepath.Join(repository, filepath.FromSlash(prefix))
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(repository, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, repository, "git", "init", "--ref-format=files")
	fixtureSCM(t, repository, "git", "config", "core.autocrlf", "false")
	fixtureSCM(t, repository, "git", "add", ".")
	fixtureSCM(t, repository, "git", "commit", "-m", "initial")
	// A leftover .git directory is not necessarily a repository. Terminal
	// Git ignores this marker and finds the shared parent checkout.
	if err := os.Mkdir(filepath.Join(roots[0], ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if actual := strings.TrimSpace(fixtureSCM(t, roots[0], "git", "rev-parse", "--show-toplevel")); !sameGitPath(actual, repository) {
		t.Fatalf("host Git selected %q instead of the parent", actual)
	}
	// Ignore and attribute files above the bot folders still affect their files.
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("ignored.tmp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte("*.txt text eol=lf\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("modified\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "ignored.tmp"), []byte("ignored\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := testServer(t)
	plugin, err := s.Plugins.Get("git")
	if err != nil {
		t.Fatal(err)
	}
	plugin.Config = map[string]any{"userName": "Echo Test", "userEmail": "test@example.invalid"}
	if err := s.Plugins.Save(plugin); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "git-parent-folders", Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos, err := s.SourceControl.Repositories(ctx, bot.ID)
	if err != nil || len(repos) != 0 {
		t.Fatalf("parent search disabled: %+v %v", repos, err)
	}
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	// Changing this setting stops the previous execution context and sessions.
	// Discovery after the transition must use a fresh, live context.
	s.Git.StopWorkspaceProcesses(bot.ID)
	if err := s.Process.CloseSessions(bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Git.Repositories(ctx, bot.ID); err != nil {
		t.Fatalf("Git discovery with an incomplete nested marker: %v", err)
	}
	repos, err = s.SourceControl.Repositories(ctx, bot.ID)
	if err != nil || len(repos) != 1 || !repos[0].Parent || len(repos[0].Scopes) != 2 {
		_, gitErr := s.Git.Repositories(ctx, bot.ID)
		t.Fatalf("shared parent repository: %+v %v; Git: %v", repos, err, gitErr)
	}
	repo := repos[0]
	if _, err := s.Git.Metadata(ctx, bot.ID, repo.ID); err != nil {
		t.Fatalf("parent repository metadata: %v", err)
	}
	if history, err := s.Git.History(ctx, bot.ID, repo.ID, 0, 10); err != nil || len(history.Commits) != 1 {
		t.Fatalf("parent repository history: %+v %v", history, err)
	}
	status, err := s.SourceControl.Status(ctx, bot.ID, repo.ID)
	if err != nil || status.TotalChangeCount != 2 || status.HiddenChangeCount != 0 {
		t.Fatalf("scoped status: %+v %v", status, err)
	}
	for _, prefix := range prefixes {
		diff, err := s.SourceControl.Diff(ctx, bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "unstaged", Path: prefix + "/file.txt"})
		if err != nil || diff.Original.Content != "original\n" || diff.Modified.Content != "modified\n" {
			t.Fatalf("scoped diff: %+v %v", diff, err)
		}
	}
	if s.Files.Allowed(bot, outside) || s.Files.Allowed(bot, filepath.Join(repository, ".git", "config")) {
		t.Fatal("source control enlarged bot file access")
	}
	// The CLI itself must not be able to read unrelated working-tree files.
	result, err := s.executeSCM(ctx, bot.ID, scmexec.Request{Command: []string{"git", "hash-object", "--no-filters", outside}, WorkingDirectory: repository})
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("outside file access: %+v %v", result, err)
	}
	if err := os.WriteFile(outside, []byte("outside staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, repository, "git", "add", "outside.txt")
	status, err = s.SourceControl.Status(ctx, bot.ID, repo.ID)
	if err != nil || status.TotalChangeCount != 2 || status.HiddenChangeCount != 1 {
		t.Fatalf("hidden staged changes: %+v %v", status, err)
	}
	_, err = s.SourceControl.Action(ctx, bot.ID, repo.ID, sourcecontrol.ActionRequest{RequestID: "blocked-commit", Action: "commit_all", Message: "must not commit outside files"})
	if err == nil || !strings.Contains(err.Error(), "staged changes outside") {
		t.Fatalf("hidden staged commit guard: %v", err)
	}
	fixtureSCM(t, repository, "git", "restore", "--staged", "--", "outside.txt")
	result, err = s.executeSCM(ctx, bot.ID, scmexec.Request{Command: []string{"git", "restore", "--worktree", "--", "outside.txt"}, WorkingDirectory: repository})
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("outside file write access: %+v %v", result, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside staged\n" {
		t.Fatalf("outside file changed: %q %v", data, err)
	}
	_, err = s.SourceControl.Action(ctx, bot.ID, repo.ID, sourcecontrol.ActionRequest{RequestID: "stage-folders", Action: "stage_all"})
	if err != nil {
		t.Fatal(err)
	}
	staged := fixtureSCM(t, repository, "git", "diff", "--cached", "--name-only")
	if strings.Contains(staged, "outside.txt") || len(strings.Fields(staged)) != 2 {
		t.Fatalf("stage all crossed bot folders: %s", staged)
	}
	_, err = s.SourceControl.Action(ctx, bot.ID, repo.ID, sourcecontrol.ActionRequest{RequestID: "commit-folders", Action: "commit_staged", Message: "scoped changes"})
	if err != nil {
		t.Fatal(err)
	}
	committed := fixtureSCM(t, repository, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if strings.Contains(committed, "outside.txt") || len(strings.Fields(committed)) != 2 {
		t.Fatalf("commit crossed bot folders: %s", committed)
	}
	// Revoking parent search must also reject commands using a cached repository.
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SourceControl.Status(ctx, bot.ID, repo.ID); err == nil {
		t.Fatal("cached parent repository remained executable after disabling parent search")
	}
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	// Initializing inside a parent repository must create the requested nested
	// checkout, not bind Git's init command to the parent's metadata.
	editorRoots, err := s.EditorFS.Roots(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	newCheckout := filepath.Join(editorRoots[0].HostPath, "new-checkout")
	if err := os.Mkdir(newCheckout, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Git.Initialize(ctx, bot.ID, gitservice.InitRequest{RootID: editorRoots[0].ID, Path: "new-checkout"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(newCheckout, ".git", "HEAD")); err != nil {
		t.Fatalf("nested initialization redirected to parent: %v", err)
	}
}
