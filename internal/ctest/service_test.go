package ctest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/workspacefs"
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

func TestAliasedWorkspaceLensesAndCoverage(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// The parent alias is longer than the nested alias. Root precedence must
	// follow the canonical directories, not the lengths of their aliases.
	alias := filepath.Join(dir, "long-workspace-alias")
	nestedAlias := filepath.Join(dir, "n")
	for link, target := range map[string]string{alias: root, nestedAlias: filepath.Join(root, "nested")} {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("directory symlinks are unavailable: %v", err)
		}
	}
	source := "int main(void) { return 0; }\n"
	if err := os.WriteFile(filepath.Join(root, "nested", "suite.c"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), "echo.json")
	manager := workspaces.NewManagerWithData(appdata.NewStore(settingsPath))
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "Aliased C tests", MainPath: alias, Folders: []string{nestedAlias}})
	if err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, settingsPath)
	t.Cleanup(fs.Close)
	roots, err := fs.Roots(workspace.ID)
	if err != nil || len(roots) != 2 {
		t.Fatalf("roots = %#v, %v", roots, err)
	}
	var nestedRootID string
	for _, candidate := range roots {
		if samePath(candidate.HostPath, nestedAlias) {
			nestedRootID = candidate.ID
		}
	}
	if nestedRootID == "" {
		t.Fatalf("nested root alias was lost: %#v", roots)
	}
	service := New(manager, fs, nil, nil, nil)
	config, err := service.SetConfig(workspace.ID, gotestconfig.CConfig{CodeLens: true, Coverage: true, Targets: []gotestconfig.CTarget{{
		ID: "unit", Name: "Unit tests", Entry: gotestconfig.CEntry{File: "${workspaceFolder}/nested/suite.c", Function: "main"},
		Executable: "${workspaceFolder}/missing/資料/tests.exe", Cwd: "${workspaceFolder}", SourceRoots: []string{"${workspaceFolder}/nested"},
		Coverage: gotestconfig.CCoverage{Provider: "gcov", ObjectRoots: []string{"${workspaceFolder}/build"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.resolveTarget(workspace.ID, config.Targets[0], false)
	if err != nil {
		t.Fatal(err)
	}
	wantRef := workspacefs.FileRef{RootID: nestedRootID, Path: "suite.c"}
	if target.entryRef != wantRef || target.entryPath != filepath.Join(root, "nested", "suite.c") {
		t.Fatalf("entry = %q, %#v; want canonical file and %#v", target.entryPath, target.entryRef, wantRef)
	}
	if target.executable != filepath.Join(root, "missing", "資料", "tests.exe") {
		t.Fatalf("prospective executable = %q", target.executable)
	}
	lenses, err := service.Lenses(workspace.ID, LensRequest{Ref: wantRef, Text: source})
	if err != nil || len(lenses) != 2 || lenses[0].Action != "run" || lenses[1].Action != "debug" {
		t.Fatalf("lenses = %#v, %v", lenses, err)
	}
	reports := []struct{ file, cwd string }{
		{filepath.Join(root, "nested", "suite.c"), ""},
		{filepath.Join(alias, "nested", "suite.c"), ""},
		{filepath.Join(nestedAlias, "suite.c"), ""},
		{"suite.c", nestedAlias},
	}
	if runtime.GOOS == "windows" {
		reports = append(reports, struct{ file, cwd string }{strings.ToUpper(filepath.Join(nestedAlias, "suite.c")), ""})
	}
	for _, report := range reports {
		filename := runtimeReportPath(service, workspace.ID, report.file, report.cwd)
		files, err := finalizeCoverage(service, workspace.ID, target, map[string]map[int]*lineAccumulator{
			filename: {1: {covered: true, count: 1}},
		})
		if err != nil || len(files) != 1 || files[0].Ref != wantRef || files[0].Lines[0].State != "covered" {
			t.Fatalf("coverage for %q in %q = %#v, %v", report.file, report.cwd, files, err)
		}
	}
	if _, err := refForHostPath(fs, workspace.ID, filepath.Join(dir, "outside.c")); err == nil {
		t.Fatal("outside coverage file was accepted")
	}
}
