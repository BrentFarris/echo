package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/tools"
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

func TestWorkspaceToolRootsDisambiguatesDuplicateLabels(t *testing.T) {
	ws := workspaces.Workspace{Folders: []string{"C:/one/src", "D:/two/src", "E:/three/src-2", "F:/four/..."}}
	roots := workspaceToolRoots(ws)
	if len(roots) != 4 || roots[0].Label != "src" || roots[1].Label != "src-2" || roots[2].Label != "src-2-2" || roots[3].Label != "workspace" {
		t.Fatalf("unexpected disambiguated labels: %#v", roots)
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

func TestToolResolverReportsUnavailableAdditionalRoot(t *testing.T) {
	server, directory := newTestServer(t)
	main := filepath.Join(directory, "main")
	extra := filepath.Join(directory, "extra")
	for _, folder := range []string{main, extra} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{
		Name: "Tools", MainPath: main, Folders: []string{extra},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	roots := server.confinedToolRoots(workspace)
	if len(roots) != 2 {
		t.Fatalf("expected configured tool roots, got %#v", roots)
	}
	_, err = server.toolPathResolver(workspace.ID, roots, false)(roots[1].Label)
	var safe tools.SafeError
	if !errors.As(err, &safe) || safe.Code != "workspace_root_unavailable" {
		t.Fatalf("expected safe unavailable-root error, got %T %v", err, err)
	}
}
