package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestFilesystemListSingleRootWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_list", mustJSON(t, map[string]any{"path": "."}))
	if !result.Success {
		t.Fatalf("list failed: %#v", result)
	}
	output, ok := result.Output.(listDirectoryOutput)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if len(output.Entries) != 2 {
		t.Fatalf("expected two entries, got %#v", output.Entries)
	}
	// Directories sort before files; then by name.
	if output.Entries[0].Name != "src" || output.Entries[0].Kind != "directory" {
		t.Fatalf("expected src directory first, got %#v", output.Entries)
	}
	if output.Entries[1].Name != "notes.txt" || output.Entries[1].Kind != "file" {
		t.Fatalf("expected notes.txt file, got %#v", output.Entries)
	}
	if output.Entries[1].Path != "notes.txt" {
		t.Fatalf("unexpected relative path: %q", output.Entries[1].Path)
	}
}

func TestFilesystemListVirtualRoots(t *testing.T) {
	appRoot := t.TempDir()
	docsRoot := t.TempDir()
	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "app", Path: appRoot},
			{Label: "docs", Path: docsRoot},
		},
	}

	result := Execute(ctx, "filesystem_list", mustJSON(t, map[string]any{"path": "."}))
	if !result.Success {
		t.Fatalf("list failed: %#v", result)
	}
	output, ok := result.Output.(listDirectoryOutput)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if output.Path != "." {
		t.Fatalf("expected virtual root path '.', got %q", output.Path)
	}
	if len(output.Entries) != 2 {
		t.Fatalf("expected two root entries, got %#v", output.Entries)
	}
	if output.Entries[0].Name != "app" || output.Entries[1].Name != "docs" {
		t.Fatalf("unexpected root entries: %#v", output.Entries)
	}
}

func TestFilesystemListLabeledRoot(t *testing.T) {
	appRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(appRoot, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "app", Path: appRoot},
			{Label: "docs", Path: t.TempDir()},
		},
	}

	result := Execute(ctx, "filesystem_list", mustJSON(t, map[string]any{"path": "app"}))
	if !result.Success {
		t.Fatalf("list failed: %#v", result)
	}
	output, ok := result.Output.(listDirectoryOutput)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if output.Path != "app" {
		t.Fatalf("expected labeled path 'app', got %q", output.Path)
	}
	if len(output.Entries) != 1 || output.Entries[0].Name != "main.go" {
		t.Fatalf("unexpected entries: %#v", output.Entries)
	}
	if output.Entries[0].Path != "app/main.go" {
		t.Fatalf("expected labeled child path, got %q", output.Entries[0].Path)
	}
}

func TestFilesystemListHiddenFiltering(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "visible.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{Context: context.Background(), WorkspacePath: workspace}

	// Default excludes hidden files.
	result := Execute(ctx, "filesystem_list", mustJSON(t, map[string]any{"path": "."}))
	if !result.Success {
		t.Fatalf("list failed: %#v", result)
	}
	output := result.Output.(listDirectoryOutput)
	if len(output.Entries) != 1 || output.Entries[0].Name != "visible.txt" {
		t.Fatalf("expected only visible file, got %#v", output.Entries)
	}

	// includeHidden includes them.
	inclusive := Execute(ctx, "filesystem_list", mustJSON(t, map[string]any{"path": ".", "includeHidden": true}))
	if !inclusive.Success {
		t.Fatalf("list failed: %#v", inclusive)
	}
	incOutput := inclusive.Output.(listDirectoryOutput)
	if len(incOutput.Entries) != 2 {
		t.Fatalf("expected two entries with hidden, got %#v", incOutput.Entries)
	}
}

func TestFilesystemListMissingDirectory(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_list", mustJSON(t, map[string]any{"path": "does-not-exist"}))
	if result.Success {
		t.Fatalf("expected failure, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "path_not_found" {
		t.Fatalf("expected path_not_found, got %#v", result.Error)
	}
}

func TestFilesystemListPathNotDirectory(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_list", mustJSON(t, map[string]any{"path": "file.txt"}))
	if result.Success {
		t.Fatalf("expected failure, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "not_directory" {
		t.Fatalf("expected not_directory, got %#v", result.Error)
	}
}

func TestFilesystemListEscapesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_list", mustJSON(t, map[string]any{"path": "../"}))
	if result.Success {
		t.Fatalf("expected failure, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected path_outside_workspace, got %#v", result.Error)
	}
}
