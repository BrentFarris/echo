package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestFilesystemReadImageReturnsLLMImageContent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "screen.png"), testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "screen.png", "detail": "low"}),
	)
	if !result.Success {
		t.Fatalf("read image failed: %#v", result)
	}
	output, ok := result.Output.(readImageFileOutput)
	if !ok {
		t.Fatalf("unexpected read image output type: %#v", result.Output)
	}
	if output.Path != "screen.png" || output.MediaType != "image/png" || output.ContentType != "image_url" || output.Detail != "low" {
		t.Fatalf("unexpected image output: %#v", output)
	}
	image, ok := output.LLMImageContent()
	if !ok || !strings.HasPrefix(image.DataURL, "data:image/png;base64,") {
		t.Fatalf("expected image_url data URL content, got ok=%v image=%#v", ok, image)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "data:image") {
		t.Fatalf("expected serialized tool result to omit image data URL, got %s", data)
	}
}

func TestFilesystemReadImageRejectsUnsupportedImage(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "vector.svg"), []byte("<svg></svg>"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "vector.svg"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "unsupported_image" {
		t.Fatalf("expected unsupported image error, got %#v", result)
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

func TestFilesystemMutationsEmitFileChanges(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("before\n"), 0o600); err != nil {
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

	createResult := Execute(ctx, "filesystem_create_text", mustJSON(t, map[string]any{"path": "created.txt", "content": "new\n"}))
	if !createResult.Success {
		t.Fatalf("create failed: %#v", createResult)
	}
	editResult := Execute(ctx, "filesystem_edit_text", mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "before\n", "newText": "after\n"}))
	if !editResult.Success {
		t.Fatalf("edit failed: %#v", editResult)
	}
	deleteResult := Execute(ctx, "filesystem_delete_file", mustJSON(t, map[string]any{"path": "created.txt"}))
	if !deleteResult.Success {
		t.Fatalf("delete failed: %#v", deleteResult)
	}

	if len(changes) != 3 {
		t.Fatalf("expected three changes, got %#v", changes)
	}
	if changes[0].Operation != FileChangeCreated || changes[0].Path != "created.txt" || changes[0].After == nil || changes[0].After.Text != "new\n" {
		t.Fatalf("unexpected create change: %#v", changes[0])
	}
	if changes[1].Operation != FileChangeEdited || changes[1].Path != "notes.txt" || changes[1].Before.Text != "before\n" || changes[1].After.Text != "after\n" {
		t.Fatalf("unexpected edit change: %#v", changes[1])
	}
	if changes[2].Operation != FileChangeDeleted || changes[2].Path != "created.txt" || changes[2].Before == nil || changes[2].Before.Text != "new\n" || changes[2].After != nil {
		t.Fatalf("unexpected delete change: %#v", changes[2])
	}
}

func TestShellCommandEmitsWorkspaceSnapshotChangesAndSkipsNoisyDirectories(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "gone.txt"), []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	var command string
	if runtime.GOOS == "windows" {
		command = "Set-Content -LiteralPath keep.txt -NoNewline -Value changed; Set-Content -LiteralPath fresh.txt -NoNewline -Value fresh; Remove-Item -LiteralPath gone.txt; New-Item -ItemType Directory -Force -Path node_modules | Out-Null; Set-Content -LiteralPath node_modules\\ignored.txt -NoNewline -Value ignored"
	} else {
		command = "printf changed > keep.txt; printf fresh > fresh.txt; rm gone.txt; mkdir -p node_modules; printf ignored > node_modules/ignored.txt"
	}
	var changes []FileChange
	result := Execute(ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		FileChanges: func(next []FileChange) {
			changes = append(changes, next...)
		},
	}, "shell_command", mustJSON(t, map[string]any{"command": command}))
	if !result.Success {
		t.Fatalf("shell failed: %#v", result)
	}

	byPath := map[string]FileChange{}
	for _, change := range changes {
		byPath[change.Path] = change
	}
	if len(byPath) != 3 {
		t.Fatalf("expected three tracked paths, got %#v", changes)
	}
	if byPath["keep.txt"].Operation != FileChangeEdited || byPath["keep.txt"].Before.Text != "before" || byPath["keep.txt"].After.Text != "changed" {
		t.Fatalf("unexpected keep change: %#v", byPath["keep.txt"])
	}
	if byPath["fresh.txt"].Operation != FileChangeCreated || byPath["fresh.txt"].After.Text != "fresh" {
		t.Fatalf("unexpected fresh change: %#v", byPath["fresh.txt"])
	}
	if byPath["gone.txt"].Operation != FileChangeDeleted || byPath["gone.txt"].Before.Text != "remove" {
		t.Fatalf("unexpected gone change: %#v", byPath["gone.txt"])
	}
	if _, ok := byPath["node_modules/ignored.txt"]; ok {
		t.Fatalf("expected node_modules change to be ignored, got %#v", changes)
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

func TestFilesystemMutationsNormalizeUnicodeLineControls(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("func main() {\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	editResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{
			"path":    "main.go",
			"oldText": "{\u0085}",
			"newText": "{\u0085\tprintln(\"Hello, World!\")\u2028}",
		}),
	)
	if !editResult.Success {
		t.Fatalf("edit failed: %#v", editResult)
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "func main() {\n\tprintln(\"Hello, World!\")\n}\n" {
		t.Fatalf("unexpected edited content: %q", edited)
	}

	createResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_create_text",
		mustJSON(t, map[string]any{
			"path":    "notes.txt",
			"content": "one\u0085two\u2028three\u2029",
		}),
	)
	if !createResult.Success {
		t.Fatalf("create failed: %#v", createResult)
	}
	created, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "one\ntwo\nthree\n" {
		t.Fatalf("unexpected created content: %q", created)
	}
}

func TestFilesystemEditPreservesCRLFLineBreaks(t *testing.T) {
	workspace := t.TempDir()
	content := "func main() {\r\n}\r\n"
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{
			"path":    "main.go",
			"oldText": "{\n}",
			"newText": "{\n\tprintln(\"Hello, World!\")\n}",
		}),
	)
	if !result.Success {
		t.Fatalf("edit failed: %#v", result)
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	expected := "func main() {\r\n\tprintln(\"Hello, World!\")\r\n}\r\n"
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

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}
