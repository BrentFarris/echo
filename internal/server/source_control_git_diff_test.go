package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/scmexec"
)

func TestNativeGitDiffCommandCost(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	fixtureSCM(t, root, "git", "init")
	fixtureSCM(t, root, "git", "config", "core.autocrlf", "false")
	const name = "file with spaces.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("head\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, root, "git", "add", ".")
	fixtureSCM(t, root, "git", "commit", "-m", "baseline")
	if err := os.WriteFile(path, []byte("index\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, root, "git", "add", ".")
	if err := os.WriteFile(path, []byte("working\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "git-diff-cost", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	g := gitservice.New(&scmexec.Workspaces{Get: func(string) (scmexec.Workspace, bool, error) {
		return scmexec.Workspace{}, true, nil
	}}, s.EditorFS)
	g.SetRuntime(&scmexec.Runtime{Available: s.scmAvailable, ValidatePath: func(_ string, path string) (string, error) { return path, nil }, Execute: func(ctx context.Context, id string, r scmexec.Request) (scmexec.Result, error) {
		start := time.Now()
		out, err := s.executeSCM(ctx, id, r)
		command := strings.Join(r.Command[3:], " ")
		commands = append(commands, command)
		t.Logf("%s: %s", command, time.Since(start))
		return out, err
	}})
	t.Cleanup(g.Close)
	ctx := context.Background()
	repos, err := g.Repositories(ctx, bot.ID)
	if err != nil || len(repos) != 1 {
		t.Fatalf("discover: %+v %v", repos, err)
	}
	for _, scope := range []string{"unstaged", "staged"} {
		commands = nil
		start := time.Now()
		diff, err := g.Diff(ctx, bot.ID, repos[0].ID, scope, name, "", "")
		t.Logf("%s diff: %s; %d commands", scope, time.Since(start), len(commands))
		indexReads := 0
		for _, command := range commands {
			if strings.HasPrefix(command, "ls-files ") {
				indexReads++
			}
		}
		limit := 5
		if scope == "staged" {
			limit = 7
		}
		if indexReads != 1 || len(commands) > limit {
			t.Fatalf("redundant diff commands: %v", commands)
		}
		if err != nil || diff.HunkToken == "" || len(diff.HunkActions) == 0 {
			t.Fatalf("diff: %+v %v", diff, err)
		}
		before, after := "index\n", "working\n"
		if scope == "staged" {
			before, after = "head\n", "index\n"
		}
		if diff.Original.Content != before || diff.Modified.Content != after {
			t.Fatalf("wrong %s baseline: %+v", scope, diff)
		}
	}
	// Keep checkout conversions, BOMs and no-final-newline bytes intact when
	// reading the filtered index directly instead of fetching it twice.
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.txt text eol=crlf\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const encodedName = "line endings.txt"
	encodedPath := filepath.Join(root, encodedName)
	if err := os.WriteFile(encodedPath, []byte("\xef\xbb\xbfbase\r\nlast"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, root, "git", "add", ".gitattributes", encodedName)
	if err := os.WriteFile(encodedPath, []byte("\xef\xbb\xbfchanged\r\nlast"), 0600); err != nil {
		t.Fatal(err)
	}
	diff, err := g.Diff(ctx, bot.ID, repos[0].ID, "unstaged", encodedName, "", "")
	if err != nil || diff.Original.Content != "base\r\nlast" || !diff.Original.HasBOM || diff.Original.EOL != "crlf" || diff.Modified.Content != "changed\r\nlast" {
		t.Fatalf("checkout conversions: %+v %v", diff, err)
	}
	const renamed = "renamed file.txt"
	if err := os.Rename(encodedPath, filepath.Join(root, renamed)); err != nil {
		t.Fatal(err)
	}
	diff, err = g.Diff(ctx, bot.ID, repos[0].ID, "unstaged", renamed, encodedName, "")
	if err != nil || diff.Original.Content != "base\r\nlast" || diff.Modified.Content != "changed\r\nlast" {
		t.Fatalf("rename baseline: %+v %v", diff, err)
	}
	for _, test := range []struct {
		name, kind string
		content    []byte
	}{
		{"binary.dat", "binary", []byte("before\x00after")},
		{"large.dat", "too-large", []byte(strings.Repeat("a", (10<<20)+1))},
	} {
		if err := os.WriteFile(filepath.Join(root, test.name), test.content, 0600); err != nil {
			t.Fatal(err)
		}
		fixtureSCM(t, root, "git", "add", test.name)
		diff, err := g.Diff(ctx, bot.ID, repos[0].ID, "unstaged", test.name, "", "")
		if err != nil || diff.Kind != test.kind || len(diff.HunkActions) != 0 {
			t.Fatalf("%s fallback: %+v %v", test.kind, diff, err)
		}
	}
}
