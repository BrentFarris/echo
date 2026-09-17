package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/workspaces"
)

func TestBookmarksAPIWorkspaceAndActivation(t *testing.T) {
	server := newPluginAPITestServer(t, false)
	directory, err := os.MkdirTemp(".", ".bookmarks-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absolute) })
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "bookmarks", MainPath: absolute})
	if err != nil {
		t.Fatal(err)
	}
	target := "/api/plugins/bookmarks/state?workspaceId=" + workspace.ID
	if response := pluginAPIRequest(t, server, http.MethodGet, target, nil); response.Code != http.StatusForbidden {
		t.Fatalf("uninstalled plugin access: %d", response.Code)
	}
	stage, err := server.plugins.StageBuiltin(context.Background(), "bookmarks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.plugins.Approve(context.Background(), stage.ID, plugins.ApprovalRequest{Scope: "global", Enable: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.MainPath, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := server.fs.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	actionTarget := "/api/plugins/bookmarks/state?workspaceId=" + workspace.ID
	created := pluginAPIRequest(t, server, http.MethodPost, actionTarget, plugins.BookmarkAction{
		Action: "toggle", Ref: plugins.BookmarkRef{RootID: roots[0].ID, Path: "main.go"}, Line: 1, Preview: "package main",
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s roots=%#v workspace=%#v", created.Code, created.Body.String(), roots, workspace)
	}
	marks := pluginAPIData(t, created)["bookmarks"].([]any)
	if len(marks) != 1 {
		t.Fatal("missing bookmark")
	}
	invalid := pluginAPIRequest(t, server, http.MethodPost, actionTarget, plugins.BookmarkAction{
		Action: "toggle", Ref: plugins.BookmarkRef{RootID: roots[0].ID, Path: "../outside.go"}, Line: 1,
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("traversal accepted: %d", invalid.Code)
	}
	// Missing files remain available for renaming/removing the bookmark.
	if err := os.Remove(filepath.Join(workspace.MainPath, "main.go")); err != nil {
		t.Fatal(err)
	}
	renamed := pluginAPIRequest(t, server, http.MethodPost, actionTarget, plugins.BookmarkAction{Action: "rename", ID: marks[0].(map[string]any)["id"].(string), Label: "Entry"})
	if renamed.Code != http.StatusOK {
		t.Fatal(renamed.Body.String())
	}
	if err := server.plugins.Action(context.Background(), "bookmarks", plugins.ActionRequest{Action: "disable-global"}); err != nil {
		t.Fatal(err)
	}
	if response := pluginAPIRequest(t, server, http.MethodGet, target, nil); response.Code != http.StatusForbidden {
		t.Fatalf("disabled plugin access: %d", response.Code)
	}
}
