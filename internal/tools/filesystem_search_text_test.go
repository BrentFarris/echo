package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemSearchTextReturnsContextAndMultilineRegex(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{
		"package main",
		"",
		"func target() {",
		"\ttargetCall()",
		"}",
		"",
		"func other() {",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	literalResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{"path": "main.go", "query": "targetCall", "contextLines": 1}),
	)
	if !literalResult.Success {
		t.Fatalf("literal search failed: %#v", literalResult)
	}
	literalOutput, ok := literalResult.Output.(searchTextFileOutput)
	if !ok {
		t.Fatalf("unexpected literal search output type: %#v", literalResult.Output)
	}
	if literalOutput.MatchCount != 1 || literalOutput.ReturnedMatches != 1 {
		t.Fatalf("unexpected literal match counts: %#v", literalOutput)
	}
	literalMatch := literalOutput.Matches[0]
	if literalMatch.Line != 4 || literalMatch.Column != 2 || len(literalMatch.Lines) != 3 {
		t.Fatalf("unexpected literal match context: %#v", literalMatch)
	}
	if literalMatch.Lines[0].Text != "func target() {" || literalMatch.Lines[1].Text != "\ttargetCall()" || literalMatch.Lines[2].Text != "}" {
		t.Fatalf("unexpected literal context lines: %#v", literalMatch.Lines)
	}

	regexResult := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{
			"path":         "main.go",
			"query":        `(?s)func target\(\) \{.*?\n\}`,
			"regex":        true,
			"multiline":    true,
			"contextLines": 0,
		}),
	)
	if !regexResult.Success {
		t.Fatalf("regex search failed: %#v", regexResult)
	}
	regexOutput, ok := regexResult.Output.(searchTextFileOutput)
	if !ok {
		t.Fatalf("unexpected regex search output type: %#v", regexResult.Output)
	}
	if regexOutput.MatchCount != 1 || regexOutput.ReturnedMatches != 1 {
		t.Fatalf("unexpected regex match counts: %#v", regexOutput)
	}
	regexMatch := regexOutput.Matches[0]
	if regexMatch.Line != 3 || regexMatch.EndLine != 5 || len(regexMatch.Lines) != 3 {
		t.Fatalf("expected multiline function block context, got %#v", regexMatch)
	}
}

func TestFilesystemSearchTextUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "notes.txt"), []byte("hello app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_search_text", mustJSON(t, map[string]any{"path": "app/notes.txt", "query": "hello"}))
	if !result.Success {
		t.Fatalf("search labeled root failed: %#v", result)
	}
	output := result.Output.(searchTextFileOutput)
	if output.Path != "app/notes.txt" || output.MatchCount != 1 {
		t.Fatalf("expected labeled search paths, got %#v", output)
	}
}

func TestFilesystemSearchTextRejectsUnlabeledMultiRootPaths(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	unlabeled := Execute(ctx, "filesystem_search_text", mustJSON(t, map[string]any{"path": "notes.txt", "query": "x"}))
	if unlabeled.Success || unlabeled.Error == nil || unlabeled.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected unlabeled path rejection, got %#v", unlabeled)
	}

	traversal := Execute(ctx, "filesystem_search_text", mustJSON(t, map[string]any{"path": "app/../outside.txt", "query": "x"}))
	if traversal.Success || traversal.Error == nil || traversal.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected traversal rejection, got %#v", traversal)
	}
}

func TestFilesystemSearchTextCaseInsensitive(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("Hello World\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	caseSensitive := false
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "query": "hello", "caseSensitive": caseSensitive}),
	)
	if !result.Success {
		t.Fatalf("case-insensitive search failed: %#v", result)
	}
	output := result.Output.(searchTextFileOutput)
	if output.MatchCount != 1 || output.CaseSensitive {
		t.Fatalf("expected case-insensitive match, got %#v", output)
	}
}

func TestFilesystemSearchTextCapsMatches(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hit\nhit\nhit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{"path": "notes.txt", "query": "hit", "maxMatches": 2}),
	)
	if !result.Success {
		t.Fatalf("capped search failed: %#v", result)
	}
	output := result.Output.(searchTextFileOutput)
	if output.MatchCount != 3 || output.ReturnedMatches != 2 {
		t.Fatalf("expected capped matches, got %#v", output)
	}
}

func TestFilesystemSearchTextRejectsBinaryFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "binary.bin"), []byte{'h', 'i', 0, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{"path": "binary.bin", "query": "hi"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "binary_file" {
		t.Fatalf("expected binary_file error, got %#v", result)
	}
}

func TestFilesystemSearchTextMissingFileNamesAttemptedPath(t *testing.T) {
	workspace := t.TempDir()

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_text",
		mustJSON(t, map[string]any{"path": "missing/notes.txt", "query": "x"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "path_not_found" {
		t.Fatalf("expected missing path error, got %#v", result)
	}
	if !strings.Contains(result.Error.Message, "missing/notes.txt") {
		t.Fatalf("expected missing path in error message, got %q", result.Error.Message)
	}
}
