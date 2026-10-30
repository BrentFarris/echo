package server

import (
	"context"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/hunk"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureSCM(t *testing.T, root, program string, args ...string) string {
	t.Helper()
	cmd := exec.Command(program, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Echo Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Echo Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	data, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture %s %v: %v: %s", program, args, e, data)
	}
	return string(data)
}
func TestEditorSourceControlNativeGit(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for contained CLI fixtures")
	}
	if _, e := exec.LookPath("git"); e != nil {
		t.Skip(e)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "one", "two")
	if e := os.MkdirAll(nested, 0700); e != nil {
		t.Fatal(e)
	}
	fixtureSCM(t, nested, "git", "init")
	fixtureSCM(t, nested, "git", "config", "core.autocrlf", "false")
	original := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"
	path := filepath.Join(nested, "file with spaces.txt")
	_ = os.WriteFile(path, []byte(original), 0600)
	fixtureSCM(t, nested, "git", "add", ".")
	fixtureSCM(t, nested, "git", "commit", "-m", "initial")
	current := strings.ReplaceAll(strings.ReplaceAll(original, "two\n", "TWO\n"), "eight\n", "EIGHT\n")
	_ = os.WriteFile(path, []byte(current), 0600)
	s := testServer(t)
	bot, e := s.saveBot(domain.Bot{Handle: "scm-fixture", Name: "SCM fixture", Roots: []string{root}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	repos, e := s.SourceControl.Repositories(ctx, bot.ID)
	if e != nil || len(repos) != 1 {
		t.Fatalf("discover: %+v %v", repos, e)
	}
	repo := repos[0]
	target := sourcecontrol.DiffTarget{Kind: "change", GroupID: "unstaged", Path: "file with spaces.txt"}
	diff, e := s.SourceControl.Diff(ctx, bot.ID, repo.ID, target)
	if e != nil {
		t.Fatal(e)
	}
	if diff.Original.Content != original || diff.Modified.Content != current || diff.HunkToken == "" {
		t.Fatalf("diff: %+v", diff)
	}
	request := sourcecontrol.ActionRequest{RequestID: "hunk1", Action: "stage_hunk", Hunk: &hunk.Request{Target: hunk.Target(target), Token: diff.HunkToken, Original: hunk.Range{Start: 2, End: 3}, Modified: hunk.Range{Start: 2, End: 3}}}
	if _, e = s.SourceControl.Action(ctx, bot.ID, repo.ID, request); e != nil {
		t.Fatal(e)
	}
	staged := fixtureSCM(t, nested, "git", "show", ":file with spaces.txt")
	if !strings.Contains(staged, "TWO") || strings.Contains(staged, "EIGHT") {
		t.Fatal("wrong hunk staged", staged)
	}
	captured, e := s.executeSCM(ctx, bot.ID, scmexec.Request{Command: []string{"git", "diff", "--cached", "--output", scmexec.OutputFileToken}, WorkingDirectory: nested, CaptureOutputFile: true, OutputLimit: 1 << 20})
	if e != nil || !strings.Contains(string(captured.Stdout), "+TWO") || strings.Contains(string(captured.Stdout), "+EIGHT") {
		t.Fatalf("confined output capture: %q %v", captured.Stdout, e)
	}
	request.RequestID = "stale"
	if _, e = s.SourceControl.Action(ctx, bot.ID, repo.ID, request); e == nil {
		t.Fatal("stale hunk applied")
	}
	action := func(name string, confirmed bool) {
		t.Helper()
		_, e = s.SourceControl.Action(ctx, bot.ID, repo.ID, sourcecontrol.ActionRequest{RequestID: name, Action: name, Paths: []string{target.Path}, Confirmed: confirmed})
		if e != nil {
			t.Fatal(name, e)
		}
	}
	action("unstage", false)
	if got := fixtureSCM(t, nested, "git", "show", ":file with spaces.txt"); got != original {
		t.Fatal("unstage changed base", got)
	}
	if _, e = s.SourceControl.Action(ctx, bot.ID, repo.ID, sourcecontrol.ActionRequest{RequestID: "no-confirm", Action: "discard", Paths: []string{target.Path}}); e == nil {
		t.Fatal("revert accepted without confirmation")
	}
	action("stage", false)
	_ = os.WriteFile(path, []byte(current+"extra\n"), 0600)
	action("discard", true)
	data, _ := os.ReadFile(path)
	if string(data) != current {
		t.Fatal("revert did not preserve index", string(data))
	}
	if _, e = s.SourceControl.Diff(ctx, bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "unstaged", Path: "../outside"}); e == nil {
		t.Fatal("path escape accepted")
	}
}

func TestEditorSourceControlNativeFossil(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for contained CLI fixtures")
	}
	if _, e := exec.LookPath("fossil"); e != nil {
		t.Skip(e)
	}
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	_ = os.Mkdir(checkout, 0700)
	fixtureSCM(t, root, "fossil", "init", "--admin-user", "echo-test", "test.fossil")
	fixtureSCM(t, checkout, "fossil", "open", filepath.Join(root, "test.fossil"))
	fixtureSCM(t, checkout, "fossil", "user", "default", "echo-test")
	path := filepath.Join(checkout, "file.txt")
	_ = os.WriteFile(path, []byte("base\n"), 0600)
	fixtureSCM(t, checkout, "fossil", "add", "file.txt")
	fixtureSCM(t, checkout, "fossil", "commit", "--nosync", "-m", "initial")
	_ = os.Remove(path)
	_ = os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("new\n"), 0600)
	s := testServer(t)
	bot, e := s.saveBot(domain.Bot{Handle: "fossil-fixture", Name: "Fossil fixture", Roots: []string{checkout}})
	if e != nil {
		t.Fatal(e)
	}
	if s.Files.Allowed(bot, filepath.Join(root, "test.fossil")) || s.Files.Allowed(bot, root) {
		t.Fatal("Fossil metadata access expanded bot file permissions")
	}
	ctx := context.Background()
	repos, e := s.SourceControl.Repositories(ctx, bot.ID)
	if e != nil || len(repos) != 1 {
		t.Fatalf("discover: %+v %v", repos, e)
	}
	if !repos[0].Available {
		t.Fatalf("sibling repository unavailable: %s", repos[0].Diagnostic)
	}
	status, e := s.SourceControl.Status(ctx, bot.ID, repos[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	found := map[string]int{}
	for _, group := range status.Groups {
		found[group.ID] = len(group.Changes)
	}
	if found["missing"] != 1 || found["untracked"] != 1 {
		raw, _ := s.executeSCM(ctx, bot.ID, scmexec.Request{Command: []string{"fossil", "changes", "--differ", "--classify", "--hash", "--rel-paths"}, WorkingDirectory: checkout})
		t.Fatalf("groups: %+v; raw=%q stderr=%q host=%q", status.Groups, raw.Stdout, raw.Stderr, fixtureSCM(t, checkout, "fossil", "changes", "--differ", "--classify", "--hash", "--rel-paths"))
	}
	for _, action := range []struct{ name, path string }{{"track", "new.txt"}, {"remove", "file.txt"}} {
		_, e = s.SourceControl.Action(ctx, bot.ID, repos[0].ID, sourcecontrol.ActionRequest{RequestID: action.name, Action: action.name, Paths: []string{action.path}})
		if e != nil {
			t.Fatal(action, e)
		}
	}
	response, e := s.Fossil.WebUI(ctx, bot.ID, repos[0].ID, "http://localhost/source-control/ui", "home")
	if e != nil {
		t.Fatal("native Fossil UI", e)
	}
	response.Body.Close()
	if response.StatusCode >= 400 {
		t.Fatal("native Fossil UI status", response.StatusCode)
	}
	changes := fixtureSCM(t, checkout, "fossil", "changes")
	if !strings.Contains(changes, "ADDED") || !strings.Contains(changes, "DELETED") {
		t.Fatal(changes)
	}
	// A commit writes the sibling repository and its SQLite journal, not just
	// the checkout database touched by add/remove.
	if _, e := s.SourceControl.Action(ctx, bot.ID, repos[0].ID, sourcecontrol.ActionRequest{RequestID: "commit-sibling", Action: "commit_all", Message: "sibling database commit"}); e != nil {
		t.Fatal("commit with sibling repository", e)
	}
	if changes := fixtureSCM(t, checkout, "fossil", "changes"); strings.TrimSpace(changes) != "" {
		t.Fatal("commit left changes", changes)
	}
}
