package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemEditTextReplacesUniqueMatch(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello workspace"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_edit_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "hello", "newText": "goodbye"}),
	)
	if !result.Success {
		t.Fatalf("edit failed: %#v", result)
	}
	output, ok := result.Output.(editTextFileOutput)
	if !ok {
		t.Fatalf("unexpected edit output type: %#v", result.Output)
	}
	if output.Path != "notes.txt" || output.Replacements != 1 {
		t.Fatalf("unexpected edit output: %#v", output)
	}
	edited, err := os.ReadFile(filepath.Join(workspace, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "goodbye workspace" {
		t.Fatalf("unexpected edited content: %q", edited)
	}
}

func TestFilesystemEditTextUsesWorkspaceRootLabels(t *testing.T) {
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

	result := Execute(ctx, "filesystem_edit_text", mustJSON(t, map[string]any{"path": "app/notes.txt", "oldText": "hello", "newText": "goodbye"}))
	if !result.Success {
		t.Fatalf("edit labeled file failed: %#v", result)
	}
	output := result.Output.(editTextFileOutput)
	if output.Path != "app/notes.txt" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
	edited, err := os.ReadFile(filepath.Join(appRoot, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "goodbye" {
		t.Fatalf("unexpected edited content: %q", edited)
	}
}

func TestFilesystemEditTextRejectsAmbiguousMatchesWithExpandedCandidates(t *testing.T) {
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

func TestFilesystemEditTextMatchesEquivalentWhitespace(t *testing.T) {
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

func TestFilesystemEditTextNormalizesUnicodeLineControls(t *testing.T) {
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
}

func TestFilesystemEditTextPreservesCRLFLineBreaks(t *testing.T) {
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

func TestFilesystemEditTextFlexibleWhitespaceStillRequiresUniqueMatch(t *testing.T) {
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

func TestFilesystemEditTextFlexibleWhitespaceDoesNotCrossLinesForSpaces(t *testing.T) {
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

func TestFilesystemEditTextEmitsFileChange(t *testing.T) {
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

	result := Execute(ctx, "filesystem_edit_text", mustJSON(t, map[string]any{"path": "notes.txt", "oldText": "before\n", "newText": "after\n"}))
	if !result.Success {
		t.Fatalf("edit failed: %#v", result)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %#v", changes)
	}
	if changes[0].Operation != FileChangeEdited || changes[0].Path != "notes.txt" || changes[0].Before.Text != "before\n" || changes[0].After.Text != "after\n" {
		t.Fatalf("unexpected edit change: %#v", changes[0])
	}
}
