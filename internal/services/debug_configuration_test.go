package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareDebugConfigurationExpandsVariablesAndPreservesAdapterProperties(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "cmd", "app", "main.go")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := debugTestWorkspace(root)
	raw := map[string]any{
		"name": "Current Go file", "type": "go", "request": "launch", "mode": "debug",
		"program": "${file}", "cwd": "${workspaceFolder}",
		"args":                  []any{"--source", "${fileBasename}"},
		"customAdapterProperty": map[string]any{"separator": "${pathSeparator}"},
		"dlvToolPath":           filepath.Join(root, "malicious-dlv"),
	}
	config, err := prepareDebugConfiguration(workspace, raw, workspace.Folders[0].Label+"/cmd/app/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := config["program"]; !samePath(got.(string), mainPath) {
		t.Fatalf("program = %#v, want %q", got, mainPath)
	}
	if _, exists := config["dlvToolPath"]; exists {
		t.Fatal("workspace configuration must not override the Delve executable")
	}
	custom := config["customAdapterProperty"].(map[string]any)
	if custom["separator"] != string(filepath.Separator) {
		t.Fatalf("path separator = %#v", custom["separator"])
	}
}

func TestPrepareDebugConfigurationRejectsCommandAndEscapingPaths(t *testing.T) {
	root := t.TempDir()
	workspace := debugTestWorkspace(root)
	for name, program := range map[string]string{
		"command": "${command:pickProcess}",
		"escape":  filepath.Join(root, "..", "outside"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareDebugConfiguration(workspace, map[string]any{
				"name": name, "type": "go", "request": "launch", "program": program,
			}, "")
			if err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}

func TestPrepareDebugConfigurationInfersDelveModuleWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	moduleRoot := filepath.Join(root, "src")
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.com/game\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := prepareDebugConfiguration(debugTestWorkspace(root), map[string]any{
		"name": "Debug Game", "type": "go", "request": "launch", "mode": "debug",
		"program": "${workspaceFolder}/src", "cwd": "${workspaceFolder}",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := config["dlvCwd"]; !samePath(got.(string), moduleRoot) {
		t.Fatalf("dlvCwd = %#v, want %q", got, moduleRoot)
	}
	if got := config["cwd"]; !samePath(got.(string), root) {
		t.Fatalf("runtime cwd = %#v, want %q", got, root)
	}
}

func TestDebugPersistentStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.json")
	state := debugPersistentState{Version: debugPersistentStateVersion, Workspaces: map[string]map[string][]DebugSourceBreakpoint{
		"workspace": {"root/main.go": {{Line: 12}, {Line: 18, Column: 2}}},
	}}
	if err := writeDebugPersistentState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded := loadDebugPersistentState(path)
	if got := loaded.Workspaces["workspace"]["root/main.go"]; len(got) != 2 || got[1].Column != 2 {
		t.Fatalf("unexpected persisted breakpoints: %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("debug state permissions = %o", info.Mode().Perm())
		}
	}
}

func TestNormalizeDebugSourceBreakpoints(t *testing.T) {
	breakpoints, err := normalizeDebugSourceBreakpoints([]DebugSourceBreakpoint{{Line: 9}, {Line: 2}, {Line: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if len(breakpoints) != 2 || breakpoints[0].Line != 2 || breakpoints[1].Line != 9 {
		t.Fatalf("unexpected normalized breakpoints: %#v", breakpoints)
	}
	if _, err := normalizeDebugSourceBreakpoints([]DebugSourceBreakpoint{{Line: 0}}); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("expected invalid line error, got %v", err)
	}
}

func debugTestWorkspace(root string) Workspace {
	return Workspace{ID: "workspace", Folders: []WorkspaceFolder{{ID: "root", Label: "root", Path: root}}}
}
