package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemCreateTextFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "src/new.txt", "content": "fresh notes"}),
	)
	if !result.Success {
		t.Fatalf("create failed: %#v", result)
	}
	output, ok := result.Output.(createTextFileOutput)
	if !ok {
		t.Fatalf("unexpected create output type: %#v", result.Output)
	}
	if output.Path != "src/new.txt" || output.BytesWritten != int64(len("fresh notes")) || output.Overwritten {
		t.Fatalf("unexpected create output: %#v", output)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "src", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "fresh notes" {
		t.Fatalf("unexpected created content: %q", created)
	}
}

func TestFilesystemCreateTextRejectsExistingWithoutOverwrite(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "content": "replacement"}),
	)
	if result.Success || result.Error == nil || result.Error.Code != "file_exists" {
		t.Fatalf("expected file_exists error, got %#v", result)
	}
}

func TestFilesystemCreateTextOverwrites(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "content": "replacement", "overwrite": true}),
	)
	if !result.Success {
		t.Fatalf("overwrite failed: %#v", result)
	}
	output, ok := result.Output.(createTextFileOutput)
	if !ok {
		t.Fatalf("unexpected overwrite output type: %#v", result.Output)
	}
	if !output.Overwritten {
		t.Fatalf("expected overwrite output to report replacement: %#v", output)
	}
	overwritten, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(overwritten) != "replacement" {
		t.Fatalf("unexpected overwritten content: %q", overwritten)
	}
}

func TestFilesystemCreateTextUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_create_text", mustJSON(t, map[string]any{"path": "app/new.txt", "content": "hello"}))
	if !result.Success {
		t.Fatalf("create labeled file failed: %#v", result)
	}
	output := result.Output.(createTextFileOutput)
	if output.Path != "app/new.txt" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
	created, err := os.ReadFile(filepath.Join(appRoot, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "hello" {
		t.Fatalf("unexpected created content: %q", created)
	}
}

func TestFilesystemCreateTextRejectsParentOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "linked/escape.txt", "content": "nope"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected path safety error, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file should not exist, stat error: %v", err)
	}
}

func TestFilesystemCreateTextEmitsFileChange(t *testing.T) {
	workspace := t.TempDir()
	var changes []FileChange
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		FileChanges: func(next []FileChange) {
			changes = append(changes, next...)
		},
	}

	result := Execute(ctx, "filesystem_create_text", mustJSON(t, map[string]any{"path": "created.txt", "content": "new\n"}))
	if !result.Success {
		t.Fatalf("create failed: %#v", result)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %#v", changes)
	}
	if changes[0].Operation != FileChangeCreated || changes[0].Path != "created.txt" || changes[0].After == nil || changes[0].After.Text != "new\n" {
		t.Fatalf("unexpected create change: %#v", changes[0])
	}
}

func TestFilesystemCreateTextNormalizesUnicodeLineControls(t *testing.T) {
	workspace := t.TempDir()

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{
			"path":    "notes.txt",
			"content": "one\u0085two\u2028three\u2029",
		}),
	)
	if !result.Success {
		t.Fatalf("create failed: %#v", result)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "one\ntwo\nthree\n" {
		t.Fatalf("unexpected created content: %q", created)
	}
}
