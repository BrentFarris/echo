package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemStatFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_stat",
		mustJSON(t, map[string]any{"path": "notes.txt"}),
	)
	if !result.Success {
		t.Fatalf("stat failed: %#v", result)
	}
	output, ok := result.Output.(statPathOutput)
	if !ok {
		t.Fatalf("unexpected stat output type: %#v", result.Output)
	}
	if output.Kind != "file" || output.Path != "notes.txt" {
		t.Fatalf("unexpected stat output: %#v", output)
	}
	if output.Bytes != int64(len("hello")) {
		t.Fatalf("unexpected byte count: %d", output.Bytes)
	}
	if output.Mode == "" || output.ModifiedAt == "" {
		t.Fatalf("expected mode and modifiedAt, got %#v", output)
	}
}

func TestFilesystemStatDirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_stat",
		mustJSON(t, map[string]any{"path": "src"}),
	)
	if !result.Success {
		t.Fatalf("stat failed: %#v", result)
	}
	output := result.Output.(statPathOutput)
	if output.Kind != "directory" || output.Path != "src" {
		t.Fatalf("unexpected stat output: %#v", output)
	}
}

func TestFilesystemStatUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_stat", mustJSON(t, map[string]any{"path": "app/main.go"}))
	if !result.Success {
		t.Fatalf("stat labeled file failed: %#v", result)
	}
	output := result.Output.(statPathOutput)
	if output.Path != "app/main.go" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
}

func TestFilesystemStatMissingPath(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_stat",
		mustJSON(t, map[string]any{"path": "missing.txt"}),
	)
	if result.Success || result.Error == nil || result.Error.Code != "path_not_found" {
		t.Fatalf("expected path_not_found, got %#v", result)
	}
}

func TestFilesystemStatRejectsPathsOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_stat",
		mustJSON(t, map[string]any{"path": ".."}),
	)
	if result.Success || result.Error == nil || result.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected path safety error, got %#v", result)
	}
}
