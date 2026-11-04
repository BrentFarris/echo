package gitservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
)

func TestScopedGitCommandArguments(t *testing.T) {
	root := t.TempDir()
	r := &repositoryState{root: root, parent: true, scopes: []repositoryScope{{Scope: Scope{RepoPrefix: "one"}}, {Scope: Scope{RepoPrefix: "two"}}}}
	directory := filepath.Join(root, "one")
	for _, tc := range []struct {
		name, command string
		args          []string
		want, stage   []string
	}{
		{"status", "status", []string{"--short"}, []string{"status", "--short", "--", ":(top,literal)one", ":(top,literal)two"}, nil},
		{"optional status value", "status", []string{"-uno"}, []string{"status", "-uno", "--", ":(top,literal)one", ":(top,literal)two"}, nil},
		{"diff revision", "diff", []string{"HEAD~1", "--", "file with spaces.txt"}, []string{"diff", "HEAD~1", "--no-ext-diff", "--no-textconv", "--", ":(top,literal)one/file with spaces.txt"}, nil},
		{"stage all", "add", []string{"-A"}, []string{"add", "-A", "--", ":(top,literal)one", ":(top,literal)two"}, nil},
		{"add without selection", "add", nil, []string{"add"}, nil},
		{"stage selection", "add", []string{"--", "."}, []string{"add", "--", ":(top,literal)one"}, nil},
		{"stage deletion", "add", []string{"-u", "deleted.txt"}, []string{"add", "-u", "--", ":(top,literal)one/deleted.txt"}, nil},
		{"commit index", "commit", []string{"-m", "message -a"}, []string{"commit", "-m", "message -a"}, nil},
		{"commit message file", "commit", []string{"-F", "message.txt"}, []string{"commit", "-F", filepath.Join(directory, "message.txt")}, nil},
		{"commit stdin", "commit", []string{"--file=-"}, []string{"commit", "--file=-"}, nil},
		{"commit tracked", "commit", []string{"-am", "message"}, []string{"commit", "-m", "message"}, []string{"add", "-u", "--", ":(top,literal)one", ":(top,literal)two"}},
		{"commit attached message", "commit", []string{"-ammessage"}, []string{"commit", "-m", "message"}, []string{"add", "-u", "--", ":(top,literal)one", ":(top,literal)two"}},
		{"commit long all", "commit", []string{"--all", "--message=message"}, []string{"commit", "--message=message"}, []string{"add", "-u", "--", ":(top,literal)one", ":(top,literal)two"}},
		{"disable all", "commit", []string{"-a", "--no-all", "-m", "message"}, []string{"commit", "-m", "message"}, nil},
		{"explicit commit", "commit", []string{"-m", "message", "--", "file.txt"}, []string{"commit", "-m", "message", "--", ":(top,literal)one/file.txt"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := r.prepareScopedCommand(directory, tc.command, tc.args)
			withoutExclusions := func(args []string) []string {
				return slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return strings.HasPrefix(arg, ":(top,exclude)") })
			}
			if err != nil || !reflect.DeepEqual(withoutExclusions(plan.args), tc.want) || !reflect.DeepEqual(withoutExclusions(plan.stage), tc.stage) {
				t.Fatalf("plan: %+v, error: %v", plan, err)
			}
		})
	}
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"add", []string{"--", "../outside.txt"}},
		{"add", []string{"--", "../../escape"}},
		{"add", []string{"--", ":(top)*"}},
		{"add", []string{"--", "*.txt"}},
		{"add", []string{"--", ".git/config"}},
		{"add", []string{"--pathspec-from-file=-"}},
		{"add", []string{"--al"}},
		{"add", []string{"-p"}},
		{"diff", []string{"--no-index", "one", "two"}},
		{"diff", []string{"--output=../outside.txt"}},
		{"diff", []string{"--ext-diff"}},
		{"commit", []string{"-am"}},
		{"commit", []string{"-a", "--dry-run"}},
		{"commit", []string{"-a", "--short"}},
		{"commit", []string{"-a", "--only"}},
		{"commit", []string{"-am", "message", "--", "file.txt"}},
		{"commit", []string{"--pathspec-from-file=-"}},
		{"reset", []string{"--hard"}},
		{"custom-alias", nil},
	} {
		if _, err := r.prepareScopedCommand(directory, tc.command, tc.args); err == nil {
			t.Fatalf("accepted unconfined %s %v", tc.command, tc.args)
		}
	}
	selected, err := r.commandPathspecs(root, []string{"."})
	if err != nil || !slices.Contains(selected, ":(top,literal)one") || !slices.Contains(selected, ":(top,literal)two") {
		t.Fatalf("root selection: %v %v", selected, err)
	}
	r.root = filepath.Join(root, "[checkout]")
	if _, err := r.commandPathspecs(r.root, nil); err != nil {
		t.Fatal("host-generated paths must not be interpreted as patterns", err)
	}
}

func TestGitCommandCancellationAndSharedLock(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "one")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	bot := domain.Bot{ID: "bot", Roots: []string{folder}}
	fs := editorfs.New(func(string) (domain.Bot, error) { return bot, nil }, t.TempDir())
	s := New(nil, fs)
	common := filepath.Join(root, ".git")
	t.Run("revoke queued command", func(t *testing.T) {
		ctx, cancel := s.CommandContext(context.Background(), bot.ID)
		defer cancel()
		other, cancelOther := s.CommandContext(context.Background(), "other")
		defer cancelOther()
		// Acquire the exact mutex used by editor repository states.
		s.mu.Lock()
		lock := s.commandLock(common)
		s.mu.Unlock()
		lock.Lock()
		called := make(chan struct{}, 1)
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(started)
			done <- s.RunScopedCommand(ctx, bot.ID, root, common, folder, "status", nil, func(context.Context, string, []string) error {
				called <- struct{}{}
				return nil
			})
		}()
		<-started
		select {
		case <-done:
			t.Fatal("bot command bypassed the editor mutation lock")
		case <-time.After(20 * time.Millisecond):
		}
		s.StopWorkspaceProcesses(bot.ID)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("revocation did not cancel the command context")
		}
		if ctx.Err() == nil || other.Err() != nil {
			t.Fatal("workspace revocation did not isolate the affected bot")
		}
		lock.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) || len(called) != 0 {
			t.Fatalf("revoked queued command ran: called=%v, error=%v", len(called), err)
		}
		fresh, cancelFresh := s.CommandContext(context.Background(), bot.ID)
		defer cancelFresh()
		if fresh.Err() != nil {
			t.Fatal("new invocation inherited a revoked context")
		}
	})
}
