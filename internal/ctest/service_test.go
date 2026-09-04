package ctest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/workspaces"
)

func TestPortableCExpansionSupportsWorkspaceFoldersSeparatorAndEnvironment(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ECHO_C_TEST_VALUE", "from-environment")
	options := expansionOptions(workspaces.Workspace{MainPath: root, Folders: []string{root, shared}}, false)
	expanded, err := debugconfig.ExpandString("${workspaceFolder:shared}${pathSeparator}fixture-${env:ECHO_C_TEST_VALUE}.c", options)
	if err != nil {
		t.Fatal(err)
	}
	if expanded != filepath.Join(shared, "fixture-from-environment.c") {
		t.Fatalf("expanded = %q", expanded)
	}
}

func TestConfinedPathRejectsOutsideAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	workspace := workspaces.Workspace{MainPath: root, Folders: []string{root}}
	if _, err := confinedPath(workspace, filepath.Join(outside, "tests.exe"), false, false); err == nil {
		t.Fatal("outside path was accepted")
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	if _, err := confinedPath(workspace, filepath.Join(link, "tests.exe"), false, false); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestWindowsPathMatchingIgnoresCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific path semantics")
	}
	root := `C:\Work\Project`
	if !pathWithin(root, `c:\work\project\src\logic.c`) || !samePath(`C:\WORK\file.c`, `c:\work\FILE.c`) {
		t.Fatal("Windows paths were matched case-sensitively")
	}
	actualRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualRoot, "Source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actualRoot, "Source", "Logic.C"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := actualRelativeCase(actualRoot, filepath.Join("source", "logic.c")); got != filepath.Join("Source", "Logic.C") {
		t.Fatalf("actual relative case = %q", got)
	}
}
