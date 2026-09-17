package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func bookmarkTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, _, workspace := newTestManager(t, false)
	manager.builtins = BuiltinPackages()
	manager.workspacePath = func(id string) (string, error) {
		if id == "workspace-1" || id == "workspace-2" {
			return workspace, nil
		}
		return "", os.ErrNotExist
	}
	stage, err := manager.StageBuiltin(context.Background(), "bookmarks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "global", Enable: true}); err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestBookmarksLifecycleAndPersistence(t *testing.T) {
	manager := bookmarkTestManager(t)
	ref := BookmarkRef{RootID: "root-1", Path: "src/main.go"}
	mutate := func(action BookmarkAction) BookmarkState {
		t.Helper()
		state, err := manager.MutateBookmarks("workspace-1", action)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := mutate(BookmarkAction{Action: "toggle", Ref: ref, Line: 4, Preview: "func main() {"})
	id := state.Bookmarks[0].ID
	mutate(BookmarkAction{Action: "rename", ID: id, Label: " Entry point "})
	mutate(BookmarkAction{Action: "positions", Positions: []BookmarkPosition{{ID: id, Ref: ref, Line: 8, Preview: "func main() {"}}})
	state = mutate(BookmarkAction{Action: "remap", PreviousRef: BookmarkRef{RootID: "root-1", Path: "src"}, NextRef: BookmarkRef{RootID: "root-2", Path: "lib"}})
	if len(state.Bookmarks) != 1 || state.Bookmarks[0].Label != "Entry point" || state.Bookmarks[0].Line != 8 || state.Bookmarks[0].Ref.Path != "lib/main.go" {
		t.Fatalf("unexpected state: %#v", state)
	}
	other, err := manager.Bookmarks("workspace-2")
	if err != nil || len(other.Bookmarks) != 0 {
		t.Fatalf("workspace leak: %#v %v", other, err)
	}
	reopened, err := NewManager(Options{RootDir: manager.root, Builtins: BuiltinPackages(), WorkspacePath: manager.workspacePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Shutdown(context.Background()) })
	persisted, err := reopened.Bookmarks("workspace-1")
	if err != nil || persisted.Revision != state.Revision || persisted.Bookmarks[0] != state.Bookmarks[0] {
		t.Fatalf("not persisted: %#v %v", persisted, err)
	}
	mutate(BookmarkAction{Action: "rename", ID: id})
	state = mutate(BookmarkAction{Action: "toggle", Ref: state.Bookmarks[0].Ref, Line: 8})
	if len(state.Bookmarks) != 0 {
		t.Fatal("toggle did not remove the bookmark")
	}
	if _, err := manager.CreateUISession("bookmarks", "bookmarks", "workspace-1"); err == nil {
		t.Fatal("native view got an iframe session")
	}
	if err := manager.Action(context.Background(), "bookmarks", ActionRequest{Action: "disable-global"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Bookmarks("workspace-1"); err == nil {
		t.Fatal("disabled bookmarks remained accessible")
	}
}

func TestBookmarksConcurrentUpdatesAndStalePositions(t *testing.T) {
	manager := bookmarkTestManager(t)
	ref := BookmarkRef{RootID: "root-1", Path: "file.go"}
	var group sync.WaitGroup
	for line := 1; line <= 16; line++ {
		group.Add(1)
		go func(line int) {
			defer group.Done()
			if _, err := manager.MutateBookmarks("workspace-1", BookmarkAction{Action: "toggle", Ref: ref, Line: line}); err != nil {
				t.Error(err)
			}
		}(line)
	}
	group.Wait()
	state, err := manager.Bookmarks("workspace-1")
	if err != nil || len(state.Bookmarks) != 16 {
		t.Fatalf("concurrent changes lost: %#v %v", state, err)
	}
	id := state.Bookmarks[0].ID
	if _, err := manager.MutateBookmarks("workspace-1", BookmarkAction{Action: "delete", ID: id}); err != nil {
		t.Fatal(err)
	}
	state, err = manager.MutateBookmarks("workspace-1", BookmarkAction{Action: "positions", Positions: []BookmarkPosition{{ID: id, Ref: ref, Line: 99}}})
	if err != nil || len(state.Bookmarks) != 15 {
		t.Fatalf("stale position recreated deletion: %#v %v", state, err)
	}
	for _, mark := range state.Bookmarks {
		if mark.ID == id {
			t.Fatal("deleted ID was restored")
		}
	}
}

func TestBookmarksRejectInvalidDataWithoutOverwriting(t *testing.T) {
	manager := bookmarkTestManager(t)
	ref := BookmarkRef{RootID: "root-1", Path: "main.go"}
	before, err := manager.MutateBookmarks("workspace-1", BookmarkAction{Action: "toggle", Ref: ref, Line: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []BookmarkAction{
		{Action: "toggle", Ref: BookmarkRef{RootID: "root-1", Path: "../escape.go"}, Line: 1},
		{Action: "toggle", Ref: ref, Line: 0},
		{Action: "rename", ID: before.Bookmarks[0].ID, Label: strings.Repeat("x", 2049)},
		{Action: "positions", Positions: []BookmarkPosition{{ID: before.Bookmarks[0].ID, Ref: ref, Line: -1}}},
	} {
		if _, err := manager.MutateBookmarks("workspace-1", action); err == nil {
			t.Fatalf("accepted invalid action: %#v", action)
		}
	}
	after, err := manager.Bookmarks("workspace-1")
	if err != nil || after.Revision != before.Revision || after.Bookmarks[0] != before.Bookmarks[0] {
		t.Fatal("rejected action changed saved bookmarks")
	}
	manager.safeMode = true
	if _, err := manager.Bookmarks("workspace-1"); err == nil {
		t.Fatal("safe mode allowed Bookmarks")
	}
}

func TestBookmarksNativeContributionProvenance(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	manager.builtins = BuiltinPackages()
	source := filepath.Join(base, "fake-bookmarks")
	if err := copyFS(BuiltinPackages()["bookmarks"], ".", source); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StageLocal(context.Background(), source); err == nil {
		t.Fatal("local package claimed a native sidebar")
	}
	stage, err := manager.StageBuiltin(context.Background(), "bookmarks")
	if err != nil {
		t.Fatal(err)
	}
	stage.Source = Source{Type: "local", Path: source}
	if err := writeStage(manager.root, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "global", Enable: true}); err == nil {
		t.Fatal("approval accepted a non-built-in native contribution")
	}
}
