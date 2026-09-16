package fossil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/hunk"
)

func TestFossilHunksFreezeOnlyChosenBlock(t *testing.T) {
	i := newFossilIntegration(t)
	base := "first\n" + strings.Repeat("context\n", 12) + "last\n"
	working := strings.ReplaceAll(strings.ReplaceAll(base, "first", "FIRST"), "last", "LAST")
	writeFossilIntegrationFile(t, i.root, "tracked.txt", base)
	runFossilIntegration(t, i.binary, i.root, "commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "hunk base")
	writeFossilIntegrationFile(t, i.root, "tracked.txt", working)
	diff := func(group string) sourcecontrol.DiffDocument {
		t.Helper()
		d, err := i.provider.Diff(context.Background(), i.workspaceID, i.repositoryID, sourcecontrol.DiffTarget{Kind: "change", GroupID: group, Path: "tracked.txt"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	request := func(d sourcecontrol.DiffDocument, action string) sourcecontrol.ActionRequest {
		return sourcecontrol.ActionRequest{RequestID: "hunk-test", Action: action, Hunk: &hunk.Request{Target: hunk.Target(d.Target), Token: d.HunkToken, Original: hunk.Range{1, 2}, Modified: hunk.Range{1, 2}}}
	}
	original := diff("working")
	if original.ModifiedRevision == "" {
		t.Fatal("editable diff has no save revision")
	}
	i.action(t, request(original, "protect_hunk"))
	frozen := diff("protected")
	if frozen.Modified.Content != strings.ReplaceAll(base, "first", "FIRST") {
		t.Fatalf("protected other block: %q", frozen.Modified.Content)
	}
	if remaining := diff("working"); remaining.Original.Content != frozen.Modified.Content || remaining.Modified.Content != working {
		t.Fatalf("remaining: %#v", remaining)
	}
	if _, err := i.provider.Action(context.Background(), i.workspaceID, i.repositoryID, request(original, "protect_hunk")); err == nil {
		t.Fatal("accepted stale block")
	}
	i.action(t, request(frozen, "unprotect_hunk"))
	if d := diff("protected"); d.Original.Content != d.Modified.Content || len(d.HunkActions) > 0 {
		t.Fatalf("last block should leave an empty diff: %#v", d)
	}
	i.action(t, request(diff("working"), "protect_hunk"))
	i.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "one block"})
	content, exists, err := i.provider.revisionFile(context.Background(), mustFossilState(t, i), "current", "tracked.txt")
	if err != nil || !exists || string(content) != strings.ReplaceAll(base, "first", "FIRST") {
		t.Fatalf("committed unrelated content: %q %v", content, err)
	}
	data, _ := os.ReadFile(filepath.Join(i.root, "tracked.txt"))
	if string(data) != working {
		t.Fatal("protected commit lost working changes")
	}
}

func mustFossilState(t *testing.T, i *fossilIntegration) *repositoryState {
	t.Helper()
	state, err := i.provider.repository(context.Background(), i.workspaceID, i.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFossilHunksRefreshUntrackedAndDeletedFiles(t *testing.T) {
	i := newFossilIntegration(t)
	writeFossilIntegrationFile(t, i.root, "new.txt", "new")
	if err := os.Remove(filepath.Join(i.root, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, group        string
		original, modified hunk.Range
	}{
		{"new.txt", "untracked", hunk.Range{1, 2}, hunk.Range{1, 2}},
		{"tracked.txt", "working", hunk.Range{1, 2}, hunk.Range{1, 1}},
	} {
		target := sourcecontrol.DiffTarget{Kind: "change", GroupID: tc.group, Path: tc.path}
		d, err := i.provider.Diff(context.Background(), i.workspaceID, i.repositoryID, target)
		if err != nil {
			t.Fatal(err)
		}
		i.action(t, sourcecontrol.ActionRequest{Action: "protect_hunk", Hunk: &hunk.Request{Target: hunk.Target(target), Token: d.HunkToken, Original: tc.original, Modified: tc.modified}})
		refreshed, err := i.provider.Diff(context.Background(), i.workspaceID, i.repositoryID, target)
		if err != nil || refreshed.Original.Content != refreshed.Modified.Content || refreshed.Original.Exists != refreshed.Modified.Exists {
			t.Fatalf("refreshed %s: %#v, %v", tc.path, refreshed, err)
		}
		target.GroupID = protectedGroupID
		frozen, err := i.provider.Diff(context.Background(), i.workspaceID, i.repositoryID, target)
		if err != nil {
			t.Fatal(err)
		}
		i.action(t, sourcecontrol.ActionRequest{Action: "unprotect_hunk", Hunk: &hunk.Request{Target: hunk.Target(target), Token: frozen.HunkToken, Original: tc.original, Modified: tc.modified}})
	}
	data, _ := os.ReadFile(filepath.Join(i.root, "new.txt"))
	if string(data) != "new" {
		t.Fatal("unprotect modified the new file")
	}
	if _, err := os.Stat(filepath.Join(i.root, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatal("unprotect restored a working file")
	}
}
