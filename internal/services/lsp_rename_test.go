package services

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLSPPrepareRenameResponse(t *testing.T) {
	target, placeholder, available, err := parseLSPPrepareRenameResponse(json.RawMessage(`{
		"range": {
			"start": {"line": 2, "character": 4},
			"end": {"line": 2, "character": 10}
		},
		"placeholder": "helper"
	}`))
	if err != nil {
		t.Fatalf("parse prepare rename: %v", err)
	}
	if !available || placeholder != "helper" ||
		target.Start != (lspPosition{Line: 2, Character: 4}) ||
		target.End != (lspPosition{Line: 2, Character: 10}) {
		t.Fatalf("unexpected prepare rename result: %#v, %q, %v", target, placeholder, available)
	}

	_, _, available, err = parseLSPPrepareRenameResponse(json.RawMessage(`{"defaultBehavior":true}`))
	if err != nil || !available {
		t.Fatalf("expected default rename behavior, available=%v err=%v", available, err)
	}
	_, _, available, err = parseLSPPrepareRenameResponse(json.RawMessage(`null`))
	if err != nil || available {
		t.Fatalf("expected unavailable null response, available=%v err=%v", available, err)
	}
}

func TestParseLSPWorkspaceEditSupportsChangesAndDocumentChanges(t *testing.T) {
	edits, err := parseLSPWorkspaceEdit(json.RawMessage(`{
		"changes": {
			"file:///workspace/main.go": [{
				"range": {
					"start": {"line": 1, "character": 4},
					"end": {"line": 1, "character": 8}
				},
				"newText": "next"
			}]
		},
		"documentChanges": [{
			"textDocument": {"uri": "file:///workspace/other.go", "version": 2},
			"edits": [{
				"range": {
					"start": {"line": 3, "character": 1},
					"end": {"line": 3, "character": 5}
				},
				"newText": "next"
			}]
		}]
	}`))
	if err != nil {
		t.Fatalf("parse workspace edit: %v", err)
	}
	if len(edits) != 2 || len(edits["file:///workspace/main.go"]) != 1 || len(edits["file:///workspace/other.go"]) != 1 {
		t.Fatalf("unexpected workspace edits: %#v", edits)
	}
}

func TestParseLSPWorkspaceEditRejectsResourceOperations(t *testing.T) {
	_, err := parseLSPWorkspaceEdit(json.RawMessage(`{
		"documentChanges": [{"kind": "rename", "oldUri": "file:///old.go", "newUri": "file:///new.go"}]
	}`))
	if err == nil {
		t.Fatal("expected resource operation to be rejected")
	}
}

func TestApplyLSPTextEditsUsesUTF16Ranges(t *testing.T) {
	content := "package main\n\nfunc helper() {\n\thelper()\n\t_ = \"🙂\"\n}\n"
	updated, err := applyLSPTextEdits(content, []lspTextEdit{
		{
			Range: lspRange{
				Start: lspPosition{Line: 2, Character: 5},
				End:   lspPosition{Line: 2, Character: 11},
			},
			NewText: "renamed",
		},
		{
			Range: lspRange{
				Start: lspPosition{Line: 3, Character: 1},
				End:   lspPosition{Line: 3, Character: 7},
			},
			NewText: "renamed",
		},
	})
	if err != nil {
		t.Fatalf("apply text edits: %v", err)
	}
	expected := "package main\n\nfunc renamed() {\n\trenamed()\n\t_ = \"🙂\"\n}\n"
	if updated != expected {
		t.Fatalf("expected %q, got %q", expected, updated)
	}
}

func TestApplyLSPTextEditsRejectsOverlappingRanges(t *testing.T) {
	_, err := applyLSPTextEdits("abcdef", []lspTextEdit{
		{Range: lspRange{Start: lspPosition{Character: 1}, End: lspPosition{Character: 4}}, NewText: "x"},
		{Range: lspRange{Start: lspPosition{Character: 3}, End: lspPosition{Character: 5}}, NewText: "y"},
	})
	if err == nil {
		t.Fatal("expected overlapping edits to be rejected")
	}
}

func TestRenameFallbackRangeUsesIdentifierAtCursor(t *testing.T) {
	content := "call helper_name now"
	from, to := renameFallbackRange(content, 9)
	if got := textForUTF16Range(content, from, to); got != "helper_name" {
		t.Fatalf("expected helper_name fallback range, got %q (%d-%d)", got, from, to)
	}
}

func TestPrepareWorkspaceRenameFilesPreservesOpenContentForHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	diskContent := "package main\n\nfunc helper() {}\n"
	openContent := "package main\n\n// unsaved\nfunc helper() {}\n"
	if err := os.WriteFile(path, []byte(diskContent), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{
		ID: "workspace",
		Folders: []WorkspaceFolder{{
			ID:    "folder",
			Path:  root,
			Label: filepath.Base(root),
		}},
	}
	files, err := prepareWorkspaceRenameFiles(
		workspace,
		map[string][]lspTextEdit{
			fileURI(path): {{
				Range: lspRange{
					Start: lspPosition{Line: 3, Character: 5},
					End:   lspPosition{Line: 3, Character: 11},
				},
				NewText: "renamed",
			}},
		},
		map[string]WorkspaceRenameFileContent{
			path: {
				FilePath: filepath.Base(root) + "/main.go",
				Content:  openContent,
			},
		},
	)
	if err != nil {
		t.Fatalf("prepare rename files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one rename file, got %#v", files)
	}
	if files[0].original != diskContent {
		t.Fatalf("expected rollback content from disk, got %q", files[0].original)
	}
	if files[0].before != openContent {
		t.Fatalf("expected history content from the open editor, got %q", files[0].before)
	}
	if !strings.Contains(files[0].updated, "func renamed()") {
		t.Fatalf("expected rename to apply to open content, got %q", files[0].updated)
	}
}

func TestReplayWorkspaceSymbolRenameAppliesAndReversesMultipleFiles(t *testing.T) {
	root := t.TempDir()
	before := map[string]string{
		"main.go":  "package main\n\nfunc helper() {}\n",
		"other.go": "package main\n\nfunc use() { helper() }\n",
	}
	after := map[string]string{
		"main.go":  "package main\n\nfunc renamed() {}\n",
		"other.go": "package main\n\nfunc use() { renamed() }\n",
	}
	for name, content := range before {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	replayFiles := func(expected map[string]string, target map[string]string) []WorkspaceRenameReplayFile {
		files := make([]WorkspaceRenameReplayFile, 0, len(expected))
		for _, name := range []string{"main.go", "other.go"} {
			files = append(files, WorkspaceRenameReplayFile{
				FilePath:        labeledTestPath(t, service, workspaceID, name),
				ExpectedContent: expected[name],
				Content:         target[name],
			})
		}
		return files
	}

	applied, err := service.ReplayWorkspaceSymbolRename(workspaceID, WorkspaceRenameReplayRequest{
		Files: replayFiles(before, after),
	})
	if err != nil {
		t.Fatalf("apply rename history: %v", err)
	}
	if len(applied.Files) != 2 {
		t.Fatalf("expected two replayed files, got %#v", applied)
	}
	assertRenameReplayContents(t, root, after)

	undone, err := service.ReplayWorkspaceSymbolRename(workspaceID, WorkspaceRenameReplayRequest{
		Files: replayFiles(after, before),
	})
	if err != nil {
		t.Fatalf("reverse rename history: %v", err)
	}
	if len(undone.Files) != 2 {
		t.Fatalf("expected two reversed files, got %#v", undone)
	}
	assertRenameReplayContents(t, root, before)
}

func TestReplayWorkspaceSymbolRenameRejectsStaleFileBeforeWriting(t *testing.T) {
	root := t.TempDir()
	mainBefore := "package main\n\nfunc helper() {}\n"
	otherBefore := "package main\n\nfunc use() { helper() }\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte("externally changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	_, err = service.ReplayWorkspaceSymbolRename(workspaceID, WorkspaceRenameReplayRequest{
		Files: []WorkspaceRenameReplayFile{
			{
				FilePath:        labeledTestPath(t, service, workspaceID, "main.go"),
				ExpectedContent: mainBefore,
				Content:         strings.ReplaceAll(mainBefore, "helper", "renamed"),
			},
			{
				FilePath:        labeledTestPath(t, service, workspaceID, "other.go"),
				ExpectedContent: otherBefore,
				Content:         strings.ReplaceAll(otherBefore, "helper", "renamed"),
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("expected stale-file error, got %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "main.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != mainBefore {
		t.Fatalf("expected validation before writes, got %q", content)
	}
}

func TestReplayWorkspaceSymbolRenameRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	_, err = service.ReplayWorkspaceSymbolRename(state.ActiveWorkspaceID, WorkspaceRenameReplayRequest{
		Files: []WorkspaceRenameReplayFile{{
			FilePath:        "../outside.go",
			ExpectedContent: "before",
			Content:         "after",
		}},
	})
	if err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

func TestWriteWorkspaceRenameFilesRollsBackEarlierWrites(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.go")
	if err := os.WriteFile(firstPath, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := writeWorkspaceRenameFiles([]workspaceRenameFile{
		{
			resolved: firstPath,
			path:     "first.go",
			original: "before",
			updated:  "after",
			mode:     0o600,
		},
		{
			resolved: root,
			path:     "invalid.go",
			original: "",
			updated:  "cannot write a directory",
			mode:     0o600,
		},
	})
	if err == nil {
		t.Fatal("expected the second write to fail")
	}
	content, readErr := os.ReadFile(firstPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "before" {
		t.Fatalf("expected the first write to be rolled back, got %q", content)
	}
}

func assertRenameReplayContents(t *testing.T, root string, expected map[string]string) {
	t.Helper()
	for name, expectedContent := range expected {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(content) != expectedContent {
			t.Fatalf("expected %s to contain %q, got %q", name, expectedContent, content)
		}
	}
}

func TestPrepareWorkspaceSymbolRenameUnsupportedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	response, err := service.PrepareWorkspaceSymbolRename(state.ActiveWorkspaceID, WorkspaceDefinitionRequest{
		FilePath: labeledTestPath(t, service, state.ActiveWorkspaceID, "notes.txt"),
		Content:  "plain text",
		Position: 2,
	})
	if err != nil {
		t.Fatalf("prepare unsupported rename: %v", err)
	}
	if response.Available || response.Message != "Rename is not available for this file type." {
		t.Fatalf("unexpected unsupported response: %#v", response)
	}
}

func TestSystemServiceRenameWorkspaceSymbolWithGopls(t *testing.T) {
	if os.Getenv("ECHO_RUN_GOPLS_RENAME_INTEGRATION") != "1" {
		t.Skip("set ECHO_RUN_GOPLS_RENAME_INTEGRATION=1 to run the real gopls rename integration test")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls was not found on PATH: %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/rename_test\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainContent := "package main\n\nfunc helper() int { return 1 }\n\nfunc main() {\n\t_ = helper()\n}\n"
	otherContent := "package main\n\nfunc use() {\n\t_ = helper()\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte(otherContent), 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	defer service.Shutdown()
	oldCommandForLanguage := lspCommandForLanguage
	lspCommandForLanguage = func(languageID string) (lspServerCommand, bool) {
		if languageID != "go" {
			return lspServerCommand{}, false
		}
		return lspServerCommand{name: "gopls", args: []string{"serve"}}, true
	}
	defer func() {
		lspCommandForLanguage = oldCommandForLanguage
	}()

	workspaceID := state.ActiveWorkspaceID
	mainPath := labeledTestPath(t, service, workspaceID, "main.go")
	position := utf16Length(mainContent[:strings.Index(mainContent, "helper")+2])
	prepared, err := service.PrepareWorkspaceSymbolRename(workspaceID, WorkspaceDefinitionRequest{
		FilePath: mainPath,
		Content:  mainContent,
		Position: position,
	})
	if err != nil {
		t.Fatalf("prepare rename: %v", err)
	}
	if !prepared.Available || prepared.Placeholder != "helper" {
		t.Fatalf("expected helper to be renameable, got %#v", prepared)
	}

	opened, err := service.ReadWorkspaceFile(workspaceID, mainPath)
	if err != nil {
		t.Fatalf("read source file: %v", err)
	}
	renamed, err := service.RenameWorkspaceSymbol(workspaceID, WorkspaceRenameRequest{
		FilePath: mainPath,
		Content:  mainContent,
		Position: position,
		NewName:  "renamedHelper",
		OpenFiles: []WorkspaceRenameFileContent{{
			FilePath:   mainPath,
			Content:    mainContent,
			ModifiedAt: opened.ModifiedAt,
		}},
	})
	if err != nil {
		t.Fatalf("rename symbol: %v", err)
	}
	if !renamed.Applied || len(renamed.Files) != 2 || len(renamed.History) != 2 {
		t.Fatalf("expected two renamed files, got %#v", renamed)
	}
	historyBefore := make(map[string]string, len(renamed.History))
	for _, file := range renamed.History {
		historyBefore[filepath.Base(filepath.FromSlash(file.FilePath))] = file.BeforeContent
		if !strings.Contains(file.AfterContent, "renamedHelper") {
			t.Fatalf("expected renamed history content for %s, got %q", file.FilePath, file.AfterContent)
		}
	}
	if historyBefore["main.go"] != mainContent || historyBefore["other.go"] != otherContent {
		t.Fatalf("unexpected rename history snapshots: %#v", historyBefore)
	}
	for _, name := range []string{"main.go", "other.go"} {
		content, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatalf("read renamed %s: %v", name, readErr)
		}
		if strings.Contains(string(content), "helper") || !strings.Contains(string(content), "renamedHelper") {
			t.Fatalf("expected all helper usages renamed in %s, got:\n%s", name, content)
		}
	}
}
