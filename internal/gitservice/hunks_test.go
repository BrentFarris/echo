package gitservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol/hunk"
)

func gitHunkRequest(document DiffDocument, action string, original, modified hunk.Range) ActionRequest {
	return ActionRequest{RequestID: "hunk-test", Action: action, Hunk: &hunk.Request{Target: hunk.Target{Kind: "change", GroupID: document.Scope, Path: document.Path, OldPath: document.OldPath}, Token: document.HunkToken, Original: original, Modified: modified}}
}

func TestGitHunksKeepOtherChangesAndRejectStaleSnapshots(t *testing.T) {
	s, fs, workspace, root := newGitServiceTestWorkspace(t)
	defer s.Close()
	defer fs.Close()
	gitTestCommand(t, root, "config", "core.autocrlf", "false")
	base := "first\n" + strings.Repeat("context\n", 12) + "last\n"
	working := strings.ReplaceAll(strings.ReplaceAll(base, "first", "FIRST"), "last", "LAST")
	writeTestFile(t, root, "file with spaces ü.txt", base)
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "base")
	writeTestFile(t, root, "file with spaces ü.txt", working)
	repos, discoveryErr := s.Repositories(context.Background(), workspace)
	if discoveryErr != nil || len(repos) != 1 {
		t.Fatalf("discovery: %#v %v", repos, discoveryErr)
	}
	repo := repos[0].ID
	diff := func(scope string) DiffDocument {
		t.Helper()
		d, err := s.Diff(context.Background(), workspace, repo, scope, "file with spaces ü.txt", "", "")
		if err != nil || d.HunkToken == "" {
			t.Fatalf("diff: %#v %v", d, err)
		}
		return d
	}
	d := diff("unstaged")
	request := gitHunkRequest(d, "stage_hunk", hunk.Range{1, 2}, hunk.Range{1, 2})
	if _, err := s.Action(context.Background(), workspace, repo, request); err != nil {
		t.Fatal(err)
	}
	staged := diff("staged")
	want := strings.ReplaceAll(base, "first", "FIRST")
	if staged.Modified.Content != want {
		t.Fatalf("staged unrelated changes: %q", staged.Modified.Content)
	}
	if _, err := s.Action(context.Background(), workspace, repo, request); err == nil {
		t.Fatal("accepted a stale block")
	}
	// Later external edits are also detected even without a status refresh.
	stale := diff("unstaged")
	writeTestFile(t, root, "file with spaces ü.txt", working+"external\n")
	_, err := s.Action(context.Background(), workspace, repo, gitHunkRequest(stale, "stage_hunk", hunk.Range{14, 15}, hunk.Range{14, 15}))
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "stale_diff" {
		t.Fatalf("external change: %v", err)
	}
	if _, err := s.Action(context.Background(), workspace, repo, gitHunkRequest(staged, "unstage_hunk", hunk.Range{1, 2}, hunk.Range{1, 2})); err != nil {
		t.Fatal(err)
	}
	if got := diff("staged").Modified.Content; got != base {
		t.Fatalf("unstage: %q", got)
	}
	data, _ := os.ReadFile(filepath.Join(root, "file with spaces ü.txt"))
	if string(data) != working+"external\n" {
		t.Fatal("changed the worktree")
	}
	bad := gitHunkRequest(diff("unstaged"), "stage_hunk", hunk.Range{1, 999}, hunk.Range{1, 2})
	if _, err := s.Action(context.Background(), workspace, repo, bad); err == nil {
		t.Fatal("accepted invalid range")
	}
	bad.Hunk.Target.Path = "../outside.txt"
	if _, err := s.Action(context.Background(), workspace, repo, bad); err == nil {
		t.Fatal("accepted outside path")
	}
}

func TestGitHunksTextFormatsAdditionsAndDeletions(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		original, modified  hunk.Range
		remove, added       bool
	}{
		{"CRLF BOM", "\ufeffa\r\nb\r\n", "\ufeffA\r\nb\r\n", hunk.Range{1, 2}, hunk.Range{1, 2}, false, false},
		{"no final newline", "a\nb", "a\nB", hunk.Range{2, 3}, hunk.Range{2, 3}, false, false},
		{"insertion", "a\n", "new\na\n", hunk.Range{1, 1}, hunk.Range{1, 2}, false, false},
		{"deletion", "a\nb\n", "a\n", hunk.Range{2, 3}, hunk.Range{2, 2}, false, false},
		{"new file", "", "new\n", hunk.Range{1, 1}, hunk.Range{1, 2}, false, true},
		{"removed file", "old\n", "", hunk.Range{1, 2}, hunk.Range{1, 1}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fs, workspace, root := newGitServiceTestWorkspace(t)
			defer s.Close()
			defer fs.Close()
			gitTestCommand(t, root, "config", "core.autocrlf", "false")
			writeTestFile(t, root, "anchor", "base")
			if !tc.added {
				writeTestFile(t, root, "test.txt", tc.before)
			}
			gitTestCommand(t, root, "add", ".")
			gitTestCommand(t, root, "commit", "-m", "base")
			if tc.remove {
				if err := os.Remove(filepath.Join(root, "test.txt")); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, root, "test.txt", tc.after)
			}
			repos, discoveryErr := s.Repositories(context.Background(), workspace)
			if discoveryErr != nil || len(repos) != 1 {
				t.Fatalf("discovery: %#v %v", repos, discoveryErr)
			}
			repo := repos[0].ID
			d, err := s.Diff(context.Background(), workspace, repo, "unstaged", "test.txt", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Action(context.Background(), workspace, repo, gitHunkRequest(d, "stage_hunk", tc.original, tc.modified)); err != nil {
				t.Fatal(err)
			}
			staged, err := s.Diff(context.Background(), workspace, repo, "staged", "test.txt", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if string(hunk.Bytes(gitHunkSide(staged.Modified))) != tc.after || staged.Modified.Exists == tc.remove {
				t.Fatalf("stage: %#v", staged)
			}
			if _, err := s.Action(context.Background(), workspace, repo, gitHunkRequest(staged, "unstage_hunk", tc.original, tc.modified)); err != nil {
				t.Fatal(err)
			}
			restored, _ := s.Diff(context.Background(), workspace, repo, "staged", "test.txt", "", "")
			if string(hunk.Bytes(gitHunkSide(restored.Modified))) != tc.before || restored.Modified.Exists == tc.added {
				t.Fatalf("unstage: %#v", restored)
			}
		})
	}
}

func TestGitHunkAttributesAndConcurrentRequests(t *testing.T) {
	s, fs, workspace, root := newGitServiceTestWorkspace(t)
	defer s.Close()
	defer fs.Close()
	writeTestFile(t, root, ".gitattributes", "*.txt text eol=crlf\n")
	writeTestFile(t, root, "file.txt", "first\r\ncontext\r\nlast\r\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "attributes")
	working := "FIRST\r\ncontext\r\nLAST\r\n"
	writeTestFile(t, root, "file.txt", working)
	repos, err := s.Repositories(context.Background(), workspace)
	if err != nil || len(repos) != 1 {
		t.Fatalf("discovery: %#v %v", repos, err)
	}
	repo := repos[0].ID
	d, err := s.Diff(context.Background(), workspace, repo, "unstaged", "file.txt", "", "")
	if err != nil || d.Original.EOL != "crlf" {
		t.Fatalf("filtered diff: %#v %v", d, err)
	}
	request := gitHunkRequest(d, "stage_hunk", hunk.Range{1, 2}, hunk.Range{1, 2})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Action(context.Background(), workspace, repo, request)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "stale_diff" {
				t.Fatal(err)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("applied %d concurrent requests", successes)
	}
	if got := gitTestCommand(t, root, "show", ":file.txt"); got != "FIRST\ncontext\nlast\n" {
		t.Fatalf("index normalization: %q", got)
	}
	data, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(data) != working {
		t.Fatal("changed worktree EOLs")
	}
}

func TestQuotePatchPathUsesGitCEscapes(t *testing.T) {
	if got := quotePatchPath("a/quote\"tab\tline\nü.txt"); got != `"a/quote\"tab\011line\012ü.txt"` {
		t.Fatalf("patch path: %s", got)
	}
}
