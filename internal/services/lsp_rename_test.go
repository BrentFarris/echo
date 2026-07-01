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
	if !renamed.Applied || len(renamed.Files) != 2 {
		t.Fatalf("expected two renamed files, got %#v", renamed)
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
