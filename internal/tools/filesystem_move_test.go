package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemMoveDefaultRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	result := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: root}, "filesystem_move", mustJSON(t, map[string]any{"path": "source.txt", "destination": "renamed.txt"}))
	if !result.Success {
		t.Fatalf("move: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "renamed.txt"))
	if err != nil || string(content) != "keep me" {
		t.Fatalf("content: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
}

func TestFilesystemMoveValidatesDestinationAndEveryChild(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src", "dst"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"visible.txt", "private.txt"} {
		if err := os.WriteFile(filepath.Join(root, "src", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scopes := NewToolScopeChecker([]ToolPermission{{Name: "filesystem_move", Paths: []string{"src", "src/visible.txt", "dst", "dst/visible.txt"}}})
	ctx := ExecutionContext{Context: context.Background(), WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: root}}, ToolScopes: scopes}
	result := Execute(ctx, "filesystem_move", mustJSON(t, map[string]any{"path": "app/src", "destination": "app/dst"}))
	if result.Success || result.Error.Code != "path_not_allowed" {
		t.Fatalf("descendant scope: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "private.txt")); err != nil {
		t.Fatal("part of rejected folder was moved")
	}
	result = Execute(ctx, "filesystem_move", mustJSON(t, map[string]any{"path": "app/src/visible.txt", "destination": "app/outside.txt"}))
	if result.Success || result.Error.Code != "path_not_allowed" {
		t.Fatalf("destination scope: %+v", result)
	}
}

func TestFilesystemMoveRejectsCrossRootAndExistingDestination(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	for _, root := range []string{one, two} {
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := ExecutionContext{Context: context.Background(), WorkspaceRoots: []WorkspaceRoot{{Label: "one", Path: one}, {Label: "two", Path: two}}}
	result := Execute(ctx, "filesystem_move", mustJSON(t, map[string]any{"path": "one/file.txt", "destination": "two/new.txt"}))
	if result.Success || result.Error.Code != "cross_root_move_unsupported" {
		t.Fatalf("cross-root move: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(one, "occupied.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	result = Execute(ctx, "filesystem_move", mustJSON(t, map[string]any{"path": "one/file.txt", "destination": "one/occupied.txt"}))
	if result.Success {
		t.Fatal("destination overwrite accepted")
	}
	content, err := os.ReadFile(filepath.Join(one, "occupied.txt"))
	if err != nil || string(content) != "original" {
		t.Fatalf("destination overwritten: %q %v", content, err)
	}
}
