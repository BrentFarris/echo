package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/mutation"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

type eventTrackedSubtree struct{ root string }

func (t eventTrackedSubtree) Tracks(_, path string) bool {
	return filepath.Clean(path) == filepath.Clean(t.root)
}
func (eventTrackedSubtree) Run(_ context.Context, op mutation.Operation, apply func() error) (mutation.Result, error) {
	return mutation.Apply(op, apply)
}

func TestShellSnapshotSkipsNestedEventTrackedRoot(t *testing.T) {
	main := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(main); err == nil {
		main = canonical
	}
	nested := filepath.Join(main, "assets")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(main, "git.txt"), filepath.Join(nested, "p4.txt")} {
		if err := os.WriteFile(path, []byte("local file\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	settings := filepath.Join(t.TempDir(), "echo.json")
	manager := workspaces.NewManager(settings)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "Mixed roots", MainPath: main, Folders: []string{nested}})
	if err != nil {
		t.Fatal(err)
	}
	filesystem := workspacefs.New(manager, settings)
	t.Cleanup(filesystem.Close)
	filesystem.SetMutationCoordinator(eventTrackedSubtree{root: nested})
	execution := ExecutionContext{Context: context.Background(), WorkspaceID: workspace.ID, WorkspaceFiles: filesystem, WorkspaceRoots: []WorkspaceRoot{{Label: "main", Path: main}, {Label: "p4", Path: nested}}}
	snapshot, err := snapshotWorkspaceDirectoryChanges(context.Background(), execution, main)
	if err != nil || len(snapshot) != 1 || !snapshot["main/git.txt"].Exists {
		t.Fatalf("mixed-root snapshot: %+v %v", snapshot, err)
	}
}
