package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func invokeGitTool(t *testing.T, s *Server, bot domain.Bot, tool, directory string, args ...string) (broker.ProcessResult, error) {
	t.Helper()
	params := map[string]any{"directory": directory}
	if args != nil {
		params["args"] = args
	}
	if tool == "git_command" {
		params["command"], params["args"] = args[0], args[1:]
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	value, err := s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, tool, raw)
	if err != nil {
		return broker.ProcessResult{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result broker.ProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func TestNativeGitPluginParentFolders(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for contained CLI fixtures")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	repository := t.TempDir()
	prefixes := []string{"project/Source/one", "project/Content/two"}
	var roots []string
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range prefixes {
		folder := filepath.Join(repository, filepath.FromSlash(prefix))
		roots = append(roots, folder)
		write(filepath.Join(folder, "file with spaces.txt"), "original\n")
		write(filepath.Join(folder, "deleted.txt"), "delete me\n")
	}
	outside := filepath.Join(repository, "outside", "file.txt")
	write(outside, "outside original\n")
	write(filepath.Join(repository, ".gitignore"), "ignored.tmp\n")
	write(filepath.Join(repository, ".gitattributes"), "*.txt text eol=lf\n")
	fixtureSCM(t, repository, "git", "init", "--ref-format=files")
	fixtureSCM(t, repository, "git", "config", "core.autocrlf", "false")
	fixtureSCM(t, repository, "git", "add", ".")
	fixtureSCM(t, repository, "git", "commit", "-m", "initial")
	plugin, _ := s.Plugins.Get("git")
	plugin.Config = map[string]any{"userName": "Echo Test", "userEmail": "test@example.invalid"}
	if err := s.Plugins.Save(plugin); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "git-tools-parent", Roots: roots, Plugins: map[string]bool{"shell": false}})
	if err != nil {
		t.Fatal(err)
	}
	success := func(tool, directory string, args ...string) string {
		t.Helper()
		result, err := invokeGitTool(t, s, bot, tool, directory, args...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%s %v in %q: %+v; %v", tool, args, directory, result, err)
		}
		return result.Output
	}
	denied := func(tool, directory string, args ...string) {
		t.Helper()
		result, err := invokeGitTool(t, s, bot, tool, directory, args...)
		if err == nil && result.ExitCode == 0 {
			t.Fatalf("unscoped command succeeded: %s %v in %q: %+v", tool, args, directory, result)
		}
	}
	success("git_command", "", "version")
	denied("git_command", "", "rev-parse", "--show-toplevel")
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	// The plugin must work before the editor has ever discovered repositories.
	if err := os.Mkdir(filepath.Join(roots[0], ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(roots[0], "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"", ".", "nested", roots[1], repository} {
		actual := strings.TrimSpace(success("git_command", directory, "rev-parse", "--show-toplevel"))
		if !sameGitPath(actual, repository) {
			t.Fatalf("resolved %q instead of %q", actual, repository)
		}
	}
	for _, root := range roots {
		write(filepath.Join(root, "file with spaces.txt"), "modified\n")
		write(filepath.Join(root, "new.txt"), "new\n")
		write(filepath.Join(root, "ignored.tmp"), "ignored\n")
		if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
			t.Fatal(err)
		}
	}
	write(outside, "outside modified\n")
	status := success("git_status", "", "--short")
	if strings.Count(status, "file with spaces.txt") != 2 || strings.Contains(status, "outside") || strings.Contains(status, "ignored.tmp") {
		t.Fatalf("scoped status: %s", status)
	}
	diff := success("git_diff", repository)
	if strings.Count(diff, "+modified") != 2 || strings.Contains(diff, "outside") {
		t.Fatalf("scoped diff: %s", diff)
	}
	diff = success("git_diff", "", "--", "file with spaces.txt")
	if strings.Count(diff, "+modified") != 1 || strings.Contains(diff, prefixes[1]) {
		t.Fatalf("explicit diff widened selection: %s", diff)
	}
	denied("git_command", repository, "add", "--", "outside/file.txt")
	denied("git_command", repository, "hash-object", "--no-filters", outside)
	denied("git_command", repository, "restore", "--worktree", "--", "outside/file.txt")
	success("git_command", "", "add", "-A")
	staged := fixtureSCM(t, repository, "git", "diff", "--cached", "--name-only", "-z")
	if strings.Count(staged, "\x00") != 6 || strings.Contains(staged, "outside") || strings.Contains(staged, "ignored.tmp") {
		t.Fatalf("stage all crossed folder boundaries: %q", staged)
	}
	fixtureSCM(t, repository, "git", "add", "outside/file.txt")
	before := fixtureSCM(t, repository, "git", "rev-parse", "HEAD")
	for _, args := range [][]string{{"commit", "-m", "blocked"}, {"commit", "-am", "blocked"}} {
		_, err := invokeGitTool(t, s, bot, "git_command", "", args...)
		if err == nil || !strings.Contains(err.Error(), "staged changes outside") {
			t.Fatalf("missing shared commit guard: %v", err)
		}
	}
	if fixtureSCM(t, repository, "git", "rev-parse", "HEAD") != before {
		t.Fatal("blocked commit changed HEAD")
	}
	fixtureSCM(t, repository, "git", "restore", "--staged", "--", "outside/file.txt")
	success("git_command", repository, "commit", "-m", "scoped changes")
	committed := fixtureSCM(t, repository, "git", "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", "HEAD")
	if strings.Count(committed, "\x00") != 6 || strings.Contains(committed, "outside") {
		t.Fatalf("commit crossed folder boundaries: %q", committed)
	}
	for _, root := range roots {
		write(filepath.Join(root, "file with spaces.txt"), "tracked only\n")
		write(filepath.Join(root, "untracked.txt"), "leave untracked\n")
	}
	success("git_command", "", "commit", "-am", "tracked only")
	committed = fixtureSCM(t, repository, "git", "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", "HEAD")
	if strings.Count(committed, "\x00") != 2 || strings.Contains(committed, "untracked.txt") || strings.Contains(committed, "outside") {
		t.Fatalf("auto-stage included unintended files: %q", committed)
	}
	write(filepath.Join(roots[0], "file with spaces.txt"), "explicit selection\n")
	write(filepath.Join(roots[1], "file with spaces.txt"), "leave for next commit\n")
	write(filepath.Join(roots[0], "ignored.tmp"), "message from relative file\n")
	success("git_command", "", "commit", "-F", "ignored.tmp", "--", "file with spaces.txt")
	committed = fixtureSCM(t, repository, "git", "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", "HEAD")
	if committed != prefixes[0]+"/file with spaces.txt\x00" {
		t.Fatalf("explicit commit widened selection: %q", committed)
	}
	if message := strings.TrimSpace(fixtureSCM(t, repository, "git", "log", "-1", "--format=%s")); message != "message from relative file" {
		t.Fatalf("relative message file changed meaning: %q", message)
	}
	if err := os.Remove(filepath.Join(roots[0], "new.txt")); err != nil {
		t.Fatal(err)
	}
	success("git_command", "", "commit", "--all", "--message=tracked deletion")
	committed = fixtureSCM(t, repository, "git", "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", "HEAD")
	if strings.Count(committed, "\x00") != 2 || !strings.Contains(committed, prefixes[0]+"/new.txt") || strings.Contains(committed, "untracked.txt") {
		t.Fatalf("long auto-stage missed tracked deletions: %q", committed)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside modified\n" {
		t.Fatalf("outside working file changed: %q %v", data, err)
	}
	if s.Files.Allowed(bot, outside) || s.Files.Allowed(bot, filepath.Join(repository, ".git", "config")) {
		t.Fatal("Git metadata grants expanded bot file access")
	}
	// Exercise revocation through the owner API, including the shared epoch.
	ctx, cancel := s.Git.CommandContext(context.Background(), bot.ID)
	defer cancel()
	response := scmAPI(t, s, "PUT", "/plugins/monaco-editor/bots/"+bot.ID+"/source-control/settings", map[string]any{"searchParentRepositories": false})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("settings failed to cancel the bot Git execution context")
	}
	denied("git_command", repository, "rev-parse", "--show-toplevel")
	denied("git_command", "", "rev-parse", "--show-toplevel")
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	newCheckout := filepath.Join(roots[0], "new-checkout")
	if err := os.Mkdir(newCheckout, 0700); err != nil {
		t.Fatal(err)
	}
	success("git_command", newCheckout, "init")
	if _, err := os.Stat(filepath.Join(newCheckout, ".git", "HEAD")); err != nil {
		t.Fatalf("init redirected to the parent: %v", err)
	}
	if actual := strings.TrimSpace(success("git_command", newCheckout, "rev-parse", "--show-toplevel")); !sameGitPath(actual, newCheckout) {
		t.Fatalf("nested repository not selected: %q", actual)
	}
	linked := filepath.Join(roots[0], "linked")
	write(filepath.Join(linked, ".git"), "gitdir: "+filepath.ToSlash(t.TempDir())+"\n")
	_, err = invokeGitTool(t, s, bot, "git_command", linked, "status")
	if err == nil || !strings.Contains(err.Error(), "linked Git metadata") {
		t.Fatalf("linked metadata escaped bot folders: %v", err)
	}
	denied("git_command", filepath.Dir(roots[0]), "status")
	denied("git_command", t.TempDir(), "status")
}

func TestGitIndependentCommandsAndEnvironment(t *testing.T) {
	for _, command := range []string{"init", "clone", "version", "status", "commit", "rev-parse"} {
		args, _, err := scmArguments("git", command, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		want := command != "init" && command != "clone" && command != "version"
		if gitUsesRepository(args) != want {
			t.Fatalf("repository binding for %s: %v", command, args)
		}
	}
	root := t.TempDir()
	scope := gitProcessScope{workTree: root, gitDir: filepath.Join(root, ".git"), commonDir: filepath.Join(root, ".git")}
	for _, directory := range []string{root, filepath.Join(root, "one", "two")} {
		env := map[string]string{"GIT_AUTHOR_NAME": "preserved"}
		scope.environment(directory, env)
		if env["GIT_AUTHOR_NAME"] != "preserved" {
			t.Fatal("Git scope overwrote plugin settings")
		}
		if directory == root {
			if env["GIT_DIR"] != ".git" || env["GIT_COMMON_DIR"] != ".git" || env["GIT_WORK_TREE"] != "." {
				t.Fatalf("root-relative metadata: %v", env)
			}
		} else if env["GIT_DIR"] != "../../.git" || !sameGitPath(env["GIT_COMMON_DIR"], scope.commonDir) {
			t.Fatalf("subfolder metadata: %v", env)
		}
	}
	env := map[string]string{}
	(gitProcessScope{}).environment(root, env)
	if len(env) != 0 {
		t.Fatal("non-repository command received Git bindings", env)
	}
}
