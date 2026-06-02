package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemToolsInvestigateWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	listArgs := mustJSON(t, map[string]any{"path": "."})
	listResult := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_list", listArgs)
	if !listResult.Success {
		t.Fatalf("list failed: %#v", listResult)
	}
	listOutput, ok := listResult.Output.(listDirectoryOutput)
	if !ok {
		t.Fatalf("unexpected list output type: %#v", listResult.Output)
	}
	if len(listOutput.Entries) != 2 {
		t.Fatalf("expected two entries, got %#v", listOutput.Entries)
	}
	if listOutput.Entries[0].Name != "src" || listOutput.Entries[0].Kind != "directory" {
		t.Fatalf("expected directories first, got %#v", listOutput.Entries)
	}

	readArgs := mustJSON(t, map[string]any{"path": "notes.txt"})
	readResult := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_read_text", readArgs)
	if !readResult.Success {
		t.Fatalf("read failed: %#v", readResult)
	}
	readOutput, ok := readResult.Output.(readTextFileOutput)
	if !ok {
		t.Fatalf("unexpected read output type: %#v", readResult.Output)
	}
	if readOutput.Content != "hello workspace" {
		t.Fatalf("unexpected file content: %q", readOutput.Content)
	}

	editResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "hello", "newText": "goodbye"}),
	)
	if !editResult.Success {
		t.Fatalf("edit failed: %#v", editResult)
	}
	editOutput, ok := editResult.Output.(editTextFileOutput)
	if !ok {
		t.Fatalf("unexpected edit output type: %#v", editResult.Output)
	}
	if editOutput.Path != "notes.txt" || editOutput.Replacements != 1 {
		t.Fatalf("unexpected edit output: %#v", editOutput)
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "goodbye workspace" {
		t.Fatalf("unexpected edited content: %q", edited)
	}

	statResult := Execute(ExecutionContext{Context: context.Background(), WorkspacePath: workspace}, "filesystem_stat", mustJSON(t, map[string]any{"path": "notes.txt"}))
	if !statResult.Success {
		t.Fatalf("stat failed: %#v", statResult)
	}
	statOutput, ok := statResult.Output.(statPathOutput)
	if !ok {
		t.Fatalf("unexpected stat output type: %#v", statResult.Output)
	}
	if statOutput.Kind != "file" || statOutput.Path != "notes.txt" {
		t.Fatalf("unexpected stat output: %#v", statOutput)
	}
}

func TestFilesystemCreateAndDeleteFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	createResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "src/new.txt", "content": "fresh notes"}),
	)
	if !createResult.Success {
		t.Fatalf("create failed: %#v", createResult)
	}
	createOutput, ok := createResult.Output.(createTextFileOutput)
	if !ok {
		t.Fatalf("unexpected create output type: %#v", createResult.Output)
	}
	if createOutput.Path != "src/new.txt" || createOutput.BytesWritten != int64(len("fresh notes")) || createOutput.Overwritten {
		t.Fatalf("unexpected create output: %#v", createOutput)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "src", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "fresh notes" {
		t.Fatalf("unexpected created content: %q", created)
	}

	duplicateResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "src/new.txt", "content": "replacement"}),
	)
	if duplicateResult.Success || duplicateResult.Error == nil || duplicateResult.Error.Code != "file_exists" {
		t.Fatalf("expected file_exists error, got %#v", duplicateResult)
	}

	overwriteResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{"path": "src/new.txt", "content": "replacement", "overwrite": true}),
	)
	if !overwriteResult.Success {
		t.Fatalf("overwrite failed: %#v", overwriteResult)
	}
	overwriteOutput, ok := overwriteResult.Output.(createTextFileOutput)
	if !ok {
		t.Fatalf("unexpected overwrite output type: %#v", overwriteResult.Output)
	}
	if !overwriteOutput.Overwritten {
		t.Fatalf("expected overwrite output to report replacement: %#v", overwriteOutput)
	}
	overwritten, err := os.ReadFile(filepath.Join(workspace, "src", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(overwritten) != "replacement" {
		t.Fatalf("unexpected overwritten content: %q", overwritten)
	}

	deleteResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_delete_file",
		mustJSON(t, map[string]any{"path": "src/new.txt"}),
	)
	if !deleteResult.Success {
		t.Fatalf("delete failed: %#v", deleteResult)
	}
	deleteOutput, ok := deleteResult.Output.(deleteFileOutput)
	if !ok {
		t.Fatalf("unexpected delete output type: %#v", deleteResult.Output)
	}
	if deleteOutput.Path != "src/new.txt" || deleteOutput.Bytes != int64(len("replacement")) {
		t.Fatalf("unexpected delete output: %#v", deleteOutput)
	}
	if _, err := os.Stat(filepath.Join(workspace, "src", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, stat error: %v", err)
	}
}

func TestFilesystemCreateRejectsParentOutsideWorkspace(t *testing.T) {
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

func TestFilesystemDeleteRejectsDirectories(t *testing.T) {
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

func TestFilesystemDeleteRejectsSymlinks(t *testing.T) {
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

func TestFilesystemToolsRejectPathsOutsideWorkspace(t *testing.T) {
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

func TestFilesystemEditRejectsAmbiguousMatchesWithExpandedCandidates(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{
		"first block",
		"target",
		"after first",
		"",
		"second block",
		"target",
		"after second",
	}, "\n")
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "target", "newText": "replacement"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "ambiguous_match" {
		t.Fatalf("expected ambiguous match error, got %#v", result)
	}
	for _, expected := range []string{"first block\ntarget", "second block\ntarget"} {
		if !strings.Contains(result.Error.Message, expected) {
			t.Fatalf("expected expanded candidate %q in message %q", expected, result.Error.Message)
		}
	}
	unchanged, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != content {
		t.Fatalf("ambiguous edit changed file: %q", unchanged)
	}
}

func TestFilesystemEditMatchesEquivalentWhitespace(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{
		"func main() {",
		"\tfmt.Println(\"hello\")",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{
			"path":    "main.go",
			"oldText": "func main() {\n    fmt.Println(\"hello\")\n}",
			"newText": "func main() {\n\tfmt.Println(\"goodbye\")\n}",
		}),
	)

	if !result.Success {
		t.Fatalf("edit failed: %#v", result)
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Join([]string{
		"func main() {",
		"\tfmt.Println(\"goodbye\")",
		"}",
		"",
	}, "\n")
	if string(edited) != expected {
		t.Fatalf("unexpected edited content: %q", edited)
	}
}

func TestFilesystemEditFlexibleWhitespaceStillRequiresUniqueMatch(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{
		"first:",
		"\ttarget",
		"",
		"second:",
		"    target",
	}, "\n")
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "\t target", "newText": "\t replacement"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "ambiguous_match" {
		t.Fatalf("expected ambiguous match error, got %#v", result)
	}
	unchanged, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != content {
		t.Fatalf("ambiguous edit changed file: %q", unchanged)
	}
}

func TestFilesystemEditFlexibleWhitespaceDoesNotCrossLinesForSpaces(t *testing.T) {
	workspace := t.TempDir()
	content := "one\ntwo\n"
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "one two", "newText": "joined"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "match_not_found" {
		t.Fatalf("expected match_not_found error, got %#v", result)
	}
	unchanged, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != content {
		t.Fatalf("failed edit changed file: %q", unchanged)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
