package server

import (
	"testing"

	"github.com/brent/echo/internal/workspaces"
)

func TestWorkspaceToolRootsLabels(t *testing.T) {
	ws := workspaces.Workspace{
		Folders: []string{
			"C:/projects/My App",
			"C:/projects/docs",
		},
	}
	roots := workspaceToolRoots(ws)
	if len(roots) != 2 {
		t.Fatalf("expected two roots, got %d", len(roots))
	}
	if roots[0].Label != "my-app" {
		t.Fatalf("expected label 'my-app', got %q", roots[0].Label)
	}
	if roots[1].Label != "docs" {
		t.Fatalf("expected label 'docs', got %q", roots[1].Label)
	}
	if roots[0].Path != "C:/projects/My App" {
		t.Fatalf("expected original path preserved, got %q", roots[0].Path)
	}
}

func TestWorkspaceToolRootsSkipsEmptyFolders(t *testing.T) {
	ws := workspaces.Workspace{Folders: []string{"", "  ", "C:/real"}}
	roots := workspaceToolRoots(ws)
	if len(roots) != 1 {
		t.Fatalf("expected one root, got %d", len(roots))
	}
	if roots[0].Label != "real" {
		t.Fatalf("expected label 'real', got %q", roots[0].Label)
	}
}

func TestNormalizeWorkspaceFolderLabel(t *testing.T) {
	cases := map[string]string{
		"Hello World": "hello-world",
		"my_app":      "my_app",
		"UPPER":       "upper",
		"a b  c":      "a-b-c",
		"...":         "",
		"  spaced  ":  "spaced",
	}
	for in, want := range cases {
		if got := normalizeWorkspaceFolderLabel(in); got != want {
			t.Errorf("normalizeWorkspaceFolderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
