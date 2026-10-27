package workspacegroups

import (
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/appdata"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
}

func TestCreateRequiresNameAndWorkspace(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create("  ", []string{"ws-1"}); err == nil {
		t.Fatal("expected empty name to be rejected")
	}
	if _, err := store.Create("Solo", nil); err == nil {
		t.Fatal("expected zero workspaces to be rejected")
	}
	group, err := store.Create("Solo", []string{" ws-1 ", "ws-1", ""})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(group.WorkspaceIDs) != 1 || group.WorkspaceIDs[0] != "ws-1" {
		t.Fatalf("workspace ids not cleaned: %+v", group.WorkspaceIDs)
	}
}

func TestUpdateAndDeleteGroup(t *testing.T) {
	store := newTestStore(t)
	group, err := store.Create("First", []string{"ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	renamed := "Renamed"
	updated, err := store.Update(group.ID, &renamed, []string{"ws-1", "ws-2"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Renamed" || len(updated.WorkspaceIDs) != 2 {
		t.Fatalf("unexpected update: %+v", updated)
	}
	if _, err := store.Update(group.ID, nil, []string{}); err == nil {
		t.Fatal("expected empty membership to be rejected")
	}
	if err := store.Delete(group.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := store.Get(group.ID); err != nil || ok {
		t.Fatalf("group still present after delete: %v %v", ok, err)
	}
	if err := store.Delete(group.ID); err == nil {
		t.Fatal("expected deleting a missing group to fail")
	}
}

func TestPendingTaskLifecycle(t *testing.T) {
	store := newTestStore(t)
	group, err := store.Create("Queue", []string{"ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTask(group.ID, PendingTask{Prompt: "   "}); err == nil {
		t.Fatal("expected blank prompt to be rejected")
	}
	saved, err := store.AddTask(group.ID, PendingTask{Prompt: "Do the thing", Model: "gpt-test", Ready: true})
	if err != nil {
		t.Fatalf("add task: %v", err)
	}
	if len(saved.PendingTasks) != 1 || saved.PendingTasks[0].ID == "" {
		t.Fatalf("task not stored: %+v", saved.PendingTasks)
	}
	taskID := saved.PendingTasks[0].ID

	if err := store.MarkDispatched(group.ID, taskID, "ws-1", "chat-1"); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	reloaded, ok, err := store.Get(group.ID)
	if err != nil || !ok {
		t.Fatalf("reload group: %v %v", ok, err)
	}
	if reloaded.PendingTasks[0].DispatchedWorkspaceID != "ws-1" || reloaded.PendingTasks[0].DispatchedChatID != "chat-1" {
		t.Fatalf("dispatch not recorded: %+v", reloaded.PendingTasks[0])
	}

	if _, err := store.DeleteTask(group.ID, taskID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if _, err := store.DeleteTask(group.ID, taskID); err == nil {
		t.Fatal("expected deleting a missing task to fail")
	}
}

func TestRemoveWorkspacePrunesEmptyGroups(t *testing.T) {
	store := newTestStore(t)
	survivor, err := store.Create("Survivor", []string{"ws-1", "ws-2"})
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := store.Create("Doomed", []string{"ws-3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTask(doomed.ID, PendingTask{Prompt: "orphaned", Ready: true}); err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveWorkspace("ws-3")
	if err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if len(removed) != 1 || removed[0] != doomed.ID {
		t.Fatalf("unexpected removed groups: %+v", removed)
	}
	groups, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != survivor.ID {
		t.Fatalf("unexpected remaining groups: %+v", groups)
	}

	// Removing a workspace that belongs to no groups is a no-op.
	if removed, err := store.RemoveWorkspace("ws-unknown"); err != nil || removed != nil {
		t.Fatalf("unexpected result for unknown workspace: %+v %v", removed, err)
	}

	// Removing one of two members keeps the group alive.
	if removed, err := store.RemoveWorkspace("ws-1"); err != nil || len(removed) != 0 {
		t.Fatalf("unexpected removal: %+v %v", removed, err)
	}
	groups, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].WorkspaceIDs) != 1 || groups[0].WorkspaceIDs[0] != "ws-2" {
		t.Fatalf("membership not pruned: %+v", groups)
	}
}

func TestGroupsPersistAcrossStoreInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo.json")
	store := NewStore(appdata.NewStore(path))
	if _, err := store.Create("Persisted", []string{"ws-1"}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore(appdata.NewStore(path))
	groups, err := reloaded.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "Persisted" {
		t.Fatalf("groups did not persist: %+v", groups)
	}
}
