package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestPermanentTrashDeletionRequiresExplicitConfirmation(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Shutdown(t.Context())
	rootPath := t.TempDir()
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "Trash", MainPath: rootPath})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := server.fs.Roots(workspace.ID)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots: %#v %v", roots, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "retained.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := server.fs.Trash(workspace.ID, workspacefs.FileRef{RootID: roots[0].ID, Path: "retained.txt"})
	if err != nil {
		t.Fatal(err)
	}

	base := "/api/workspaces/" + workspace.ID + "/fs/trash/" + item.ID
	response := doRequest(t, server, http.MethodDelete, base)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed permanent delete returned %d: %s", response.Code, response.Body.String())
	}
	items, err := server.fs.ListTrash(workspace.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("unconfirmed delete did not preserve the item: %#v %v", items, err)
	}

	response = doRequest(t, server, http.MethodDelete, base+"?confirmed=true")
	if response.Code != http.StatusOK {
		t.Fatalf("confirmed permanent delete returned %d: %s", response.Code, response.Body.String())
	}
}
