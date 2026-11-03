package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/fossil"
)

func TestNativeFossilDiffCommandCost(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	if _, err := exec.LookPath("fossil"); err != nil {
		t.Skip(err)
	}
	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, parent, "fossil", "init", "--admin-user", "echo-test", "repository.fossil")
	fixtureSCM(t, checkout, "fossil", "open", filepath.Join(parent, "repository.fossil"))
	fixtureSCM(t, checkout, "fossil", "user", "default", "echo-test")
	for i := range 4000 {
		if err := os.WriteFile(filepath.Join(checkout, fmt.Sprintf("file-%04d.txt", i)), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureSCM(t, checkout, "fossil", "add", ".")
	fixtureSCM(t, checkout, "fossil", "commit", "--nosync", "-m", "baseline")
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "fossil-diff", Roots: []string{checkout}})
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	runner := &scmexec.Runtime{
		Available: s.scmAvailable, ValidateFossilRepository: s.validateFossilRepository,
		ValidatePath: func(_ string, path string) (string, error) {
			if !s.Files.Allowed(bot, path) {
				return "", fmt.Errorf("outside test workspace")
			}
			return path, nil
		},
		Execute: func(ctx context.Context, id string, request scmexec.Request) (scmexec.Result, error) {
			start := time.Now()
			result, err := s.executeSCM(ctx, id, request)
			commands = append(commands, request.Command[1])
			t.Logf("%s: %s", request.Command[1], time.Since(start))
			return result, err
		},
	}
	p := fossil.New(&scmexec.Workspaces{Get: func(string) (scmexec.Workspace, bool, error) {
		return scmexec.Workspace{}, true, nil
	}}, s.EditorFS, runner, t.TempDir())
	t.Cleanup(p.Close)
	ctx := context.Background()
	repos, err := p.Repositories(ctx, bot.ID)
	if err != nil || len(repos) != 1 || !repos[0].Available {
		t.Fatalf("discovery: %+v %v", repos, err)
	}
	// No status subscription/cache: opening a file must not hash the checkout
	// merely to enable an undoable editor-buffer operation.
	for _, contents := range []string{"first edit\n", "second edit\n"} {
		if err := os.WriteFile(filepath.Join(checkout, "file-0000.txt"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		commands = nil
		start := time.Now()
		diff, err := p.Diff(ctx, bot.ID, repos[0].ID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "working", Path: "file-0000.txt"})
		t.Logf("diff: %s; commands=%v", time.Since(start), commands)
		if len(commands) != 1 || commands[0] != "finfo" {
			t.Fatalf("opening a working diff launched extra checkout commands: %v", commands)
		}
		if err != nil || diff.Original.Content != "base\n" || diff.Modified.Content != contents || !diff.Editable {
			t.Fatalf("diff: %+v %v", diff, err)
		}
		if len(diff.HunkActions) != 1 || diff.HunkActions[0] != "revert_hunk" || diff.HunkToken == "" {
			t.Fatal("missing buffer hunk revert", diff.HunkActions)
		}
	}
}
