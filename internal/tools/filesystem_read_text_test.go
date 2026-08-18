package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemReadTextReturnsTargetedLineBlock(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{"one", "two", "three", "four", "five"}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "startLine": 3, "lineCount": 2}),
	)
	if !result.Success {
		t.Fatalf("line block read failed: %#v", result)
	}
	output, ok := result.Output.(readTextFileOutput)
	if !ok {
		t.Fatalf("unexpected read output type: %#v", result.Output)
	}
	if output.Content != "three\nfour\n" || output.StartLine != 3 || output.EndLine != 4 {
		t.Fatalf("unexpected line block output: %#v", output)
	}
	if output.BytesRead != int64(len("three\nfour\n")) || !output.Truncated {
		t.Fatalf("expected byte count and truncation marker for partial file read, got %#v", output)
	}
}

func TestFilesystemReadTextReturnsBlockAroundLine(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{"one", "two", "three", "four", "five"}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "aroundLine": 3, "contextLines": 1}),
	)
	if !result.Success {
		t.Fatalf("aroundLine read failed: %#v", result)
	}
	output, ok := result.Output.(readTextFileOutput)
	if !ok {
		t.Fatalf("unexpected read output type: %#v", result.Output)
	}
	if output.Content != "two\nthree\nfour\n" || output.StartLine != 2 || output.EndLine != 4 {
		t.Fatalf("unexpected aroundLine output: %#v", output)
	}

	singleLineResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "aroundLine": 3, "contextLines": 0}),
	)
	if !singleLineResult.Success {
		t.Fatalf("single-line aroundLine read failed: %#v", singleLineResult)
	}
	singleLineOutput := singleLineResult.Output.(readTextFileOutput)
	if singleLineOutput.Content != "three\n" || singleLineOutput.StartLine != 3 || singleLineOutput.EndLine != 3 {
		t.Fatalf("unexpected single-line aroundLine output: %#v", singleLineOutput)
	}
}

func TestFilesystemReadTextRejectsInvalidLineRange(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "startLine": -1}),
	)
	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid startLine error, got %#v", result)
	}
}

func TestFilesystemReadTextUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	docsRoot := filepath.Join(base, "docs")
	for _, path := range []string{appRoot, docsRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(docsRoot, "guide.md"), []byte("hello docs"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "app", Path: appRoot},
			{Label: "docs", Path: docsRoot},
		},
	}

	result := Execute(ctx, "filesystem_read_text", mustJSON(t, map[string]any{"path": "docs/guide.md"}))
	if !result.Success {
		t.Fatalf("read labeled file failed: %#v", result)
	}
	output := result.Output.(readTextFileOutput)
	if output.Path != "docs/guide.md" || output.Content != "hello docs" {
		t.Fatalf("unexpected read output: %#v", output)
	}
}

func TestFilesystemReadTextRejectsUnlabeledMultiRootPaths(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	unlabeled := Execute(ctx, "filesystem_read_text", mustJSON(t, map[string]any{"path": "notes.txt"}))
	if unlabeled.Success || unlabeled.Error == nil || unlabeled.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected unlabeled path rejection, got %#v", unlabeled)
	}

	traversal := Execute(ctx, "filesystem_read_text", mustJSON(t, map[string]any{"path": "app/../outside.txt"}))
	if traversal.Success || traversal.Error == nil || traversal.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected traversal rejection, got %#v", traversal)
	}
}

func TestFilesystemReadTextMissingFileNamesAttemptedPath(t *testing.T) {
	workspace := t.TempDir()

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "missing/notes.txt"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "path_not_found" {
		t.Fatalf("expected missing path error, got %#v", result)
	}
	if !strings.Contains(result.Error.Message, "missing/notes.txt") {
		t.Fatalf("expected missing path in error message, got %q", result.Error.Message)
	}
}

func TestFilesystemReadTextRejectsBinaryFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "binary.bin"), []byte{'h', 'i', 0, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "binary.bin"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "binary_file" {
		t.Fatalf("expected binary_file error, got %#v", result)
	}
}

func TestFilesystemReadTextRespectsMaxBytes(t *testing.T) {
	workspace := t.TempDir()
	content := "abcdefghij"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "maxBytes": 4}),
	)
	if !result.Success {
		t.Fatalf("maxBytes read failed: %#v", result)
	}
	output := result.Output.(readTextFileOutput)
	if output.Content != "abcd" || output.BytesRead != 4 || !output.Truncated {
		t.Fatalf("unexpected maxBytes output: %#v", output)
	}
}
