package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemSearchWorkspaceSearchesMultipleRoots(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	docsRoot := filepath.Join(base, "docs")
	for _, path := range []string{appRoot, docsRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appRoot, "main.go"), []byte("package main\n// shared target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsRoot, "guide.md"), []byte("# Guide\nshared target\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "app", Path: appRoot},
			{Label: "docs", Path: docsRoot},
		},
	}
	result := Execute(ctx, "filesystem_search_workspace", mustJSON(t, map[string]any{"query": "shared target"}))
	if !result.Success {
		t.Fatalf("workspace search failed: %#v", result)
	}
	output, ok := result.Output.(searchWorkspaceTextOutput)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if output.MatchCount != 2 || output.ReturnedMatches != 2 || output.FilesSearched != 2 {
		t.Fatalf("unexpected workspace search counts: %#v", output)
	}
	paths := []string{output.Matches[0].Path, output.Matches[1].Path}
	if strings.Join(paths, ",") != "app/main.go,docs/guide.md" {
		t.Fatalf("unexpected match paths: %#v", output.Matches)
	}
}

func TestFilesystemSearchWorkspaceSkipsIgnoredAndHiddenByDefault(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("visible target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "node_modules", "pkg.js"), []byte("ignored target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".secret", "notes.txt"), []byte("hidden target\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{Context: context.Background(), WorkspacePath: workspace}
	defaultResult := Execute(ctx, "filesystem_search_workspace", mustJSON(t, map[string]any{"query": "target"}))
	if !defaultResult.Success {
		t.Fatalf("default workspace search failed: %#v", defaultResult)
	}
	defaultOutput := defaultResult.Output.(searchWorkspaceTextOutput)
	if defaultOutput.MatchCount != 1 || defaultOutput.Matches[0].Path != "src/main.go" {
		t.Fatalf("expected only visible match by default, got %#v", defaultOutput)
	}

	inclusiveResult := Execute(ctx, "filesystem_search_workspace", mustJSON(t, map[string]any{
		"query":          "target",
		"includeHidden":  true,
		"includeIgnored": true,
	}))
	if !inclusiveResult.Success {
		t.Fatalf("inclusive workspace search failed: %#v", inclusiveResult)
	}
	inclusiveOutput := inclusiveResult.Output.(searchWorkspaceTextOutput)
	if inclusiveOutput.MatchCount != 3 || inclusiveOutput.ReturnedMatches != 3 {
		t.Fatalf("expected hidden and ignored matches when requested, got %#v", inclusiveOutput)
	}
}

func TestFilesystemSearchWorkspaceSupportsRegexCaseInsensitiveAndContext(t *testing.T) {
	workspace := t.TempDir()
	content := strings.Join([]string{
		"before",
		"func Target() {",
		"\treturn true",
		"}",
		"after",
	}, "\n")
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	caseSensitive := false
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_workspace",
		mustJSON(t, map[string]any{
			"query":         `func target\(\) \{\s+return true\s+\}`,
			"regex":         true,
			"multiline":     true,
			"caseSensitive": caseSensitive,
			"contextLines":  1,
		}),
	)
	if !result.Success {
		t.Fatalf("regex workspace search failed: %#v", result)
	}
	output := result.Output.(searchWorkspaceTextOutput)
	if output.MatchCount != 1 || output.Matches[0].Line != 2 || output.Matches[0].EndLine != 4 {
		t.Fatalf("unexpected regex match: %#v", output)
	}
	if len(output.Matches[0].Lines) != 5 || output.Matches[0].Lines[0].Text != "before" || output.Matches[0].Lines[4].Text != "after" {
		t.Fatalf("expected context around multiline match, got %#v", output.Matches[0].Lines)
	}
}

func TestFilesystemSearchWorkspaceSkipsBinaryLargeAndCapsMatches(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "binary.bin"), []byte{'h', 'i', 't', 0, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("hit\n", maxSearchFileBytes/4+2)
	if err := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "zz_hits.txt"), []byte("hit\nhit\nhit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_search_workspace",
		mustJSON(t, map[string]any{"query": "hit", "maxMatches": 2}),
	)
	if !result.Success {
		t.Fatalf("capped workspace search failed: %#v", result)
	}
	output := result.Output.(searchWorkspaceTextOutput)
	if output.MatchCount != 3 || output.ReturnedMatches != 2 || !output.Truncated {
		t.Fatalf("expected capped matches with truncation, got %#v", output)
	}
	if output.FilesSearched != 1 || output.FilesSkipped != 2 {
		t.Fatalf("expected binary and large files to be skipped, got %#v", output)
	}
}

func TestFilesystemSearchWorkspaceScopedToSubdirectory(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("target in src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "other", "notes.txt"), []byte("target in other\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{Context: context.Background(), WorkspacePath: workspace}
	result := Execute(ctx, "filesystem_search_workspace", mustJSON(t, map[string]any{"path": "src", "query": "target"}))
	if !result.Success {
		t.Fatalf("scoped workspace search failed: %#v", result)
	}
	output := result.Output.(searchWorkspaceTextOutput)
	if output.MatchCount != 1 || output.Matches[0].Path != "src/main.go" {
		t.Fatalf("expected only src match, got %#v", output)
	}
}

func TestFilesystemSearchWorkspaceRequiresQuery(t *testing.T) {
	workspace := t.TempDir()
	ctx := ExecutionContext{Context: context.Background(), WorkspacePath: workspace}
	result := Execute(ctx, "filesystem_search_workspace", mustJSON(t, map[string]any{}))
	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments error, got %#v", result)
	}
}
