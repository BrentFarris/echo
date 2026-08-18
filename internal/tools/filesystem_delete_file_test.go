package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemDeleteFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_delete_file",
		mustJSON(t, map[string]any{"path": "notes.txt"}),
	)
	if !result.Success {
		t.Fatalf("delete failed: %#v", result)
	}
	output, ok := result.Output.(deleteFileOutput)
	if !ok {
		t.Fatalf("unexpected delete output type: %#v", result.Output)
	}
	if output.Path != "notes.txt" || output.Bytes != int64(len("hello")) {
		t.Fatalf("unexpected delete output: %#v", output)
	}
	if _, err := os.Stat(filepath.Join(workspace, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, stat error: %v", err)
	}
}

func TestFilesystemDeleteFileUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_delete_file", mustJSON(t, map[string]any{"path": "app/notes.txt"}))
	if !result.Success {
		t.Fatalf("delete labeled file failed: %#v", result)
	}
	output := result.Output.(deleteFileOutput)
	if output.Path != "app/notes.txt" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
	if _, err := os.Stat(filepath.Join(appRoot, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, stat error: %v", err)
	}
}

func TestFilesystemDeleteFileRejectsMissingFile(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_delete_file",
		mustJSON(t, map[string]any{"path": "missing.txt"}),
	)
	if result.Success || result.Error == nil || result.Error.Code != "path_not_found" {
		t.Fatalf("expected path_not_found, got %#v", result)
	}
}

func TestFilesystemDeleteFileRejectsDirectories(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_delete_file",
		mustJSON(t, map[string]any{"path": "src"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "not_file" {
		t.Fatalf("expected not_file error, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "src")); err != nil {
		t.Fatalf("directory should remain, stat error: %v", err)
	}
}

func TestFilesystemDeleteFileRejectsSymlinks(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "target.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(workspace, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_delete_file",
		mustJSON(t, map[string]any{"path": "link.txt"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "not_file" {
		t.Fatalf("expected not_file error, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "target.txt")); err != nil {
		t.Fatalf("target should remain, stat error: %v", err)
	}
}

func TestFilesystemDeleteFileEmitsFileChange(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var changes []FileChange
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		FileChanges: func(next []FileChange) {
			changes = append(changes, next...)
		},
	}

	result := Execute(ctx, "filesystem_delete_file", mustJSON(t, map[string]any{"path": "notes.txt"}))
	if !result.Success {
		t.Fatalf("delete failed: %#v", result)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %#v", changes)
	}
	if changes[0].Operation != FileChangeDeleted || changes[0].Path != "notes.txt" || changes[0].Before == nil || changes[0].Before.Text != "new\n" || changes[0].After != nil {
		t.Fatalf("unexpected delete change: %#v", changes[0])
	}
}
