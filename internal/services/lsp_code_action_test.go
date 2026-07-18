package services

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseLSPCodeActionResponseSupportsEditsAndCommands(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"title": "Organize Imports",
			"kind": "source.organizeImports",
			"edit": {
				"changes": {
					"file:///workspace/main.go": [{
						"range": {
							"start": {"line": 1, "character": 0},
							"end": {"line": 1, "character": 0}
						},
						"newText": "import \"fmt\"\n"
					}]
				}
			}
		},
		{
			"title": "Run organize imports",
			"command": "gopls.apply_fix",
			"arguments": [{"kind": "source.organizeImports"}]
		}
	]`)

	actions, err := parseLSPCodeActionResponse(raw)
	if err != nil {
		t.Fatalf("parse code actions: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected two code actions, got %#v", actions)
	}
	if actions[0].Edit == nil || len(actions[0].Edit.Changes["file:///workspace/main.go"]) != 1 {
		t.Fatalf("expected direct workspace edit, got %#v", actions[0])
	}
	if actions[1].Command == nil || actions[1].Command.Command != "gopls.apply_fix" || len(actions[1].Command.Arguments) != 1 {
		t.Fatalf("expected direct command action, got %#v", actions[1])
	}
}

func TestApplyWorkspaceEditToSingleFileSupportsDocumentChanges(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"ok\")\n}\n"
	edit := lspWorkspaceEdit{
		DocumentChanges: []json.RawMessage{json.RawMessage(`{
			"textDocument": {"uri": "file:///workspace/main.go", "version": 1},
			"edits": [{
				"range": {
					"start": {"line": 1, "character": 0},
					"end": {"line": 1, "character": 0}
				},
				"newText": "import \"fmt\"\n"
			}]
		}`)},
	}

	updated, changed, err := applyWorkspaceEditToSingleFile(content, "/workspace/main.go", edit)
	if err != nil {
		t.Fatalf("apply workspace edit: %v", err)
	}
	if !changed || !strings.Contains(updated, "import \"fmt\"") {
		t.Fatalf("expected import edit to be applied, changed=%v content=%q", changed, updated)
	}
}

func TestApplyWorkspaceEditToSingleFileRejectsOtherFiles(t *testing.T) {
	_, _, err := applyWorkspaceEditToSingleFile("package main\n", "/workspace/main.go", lspWorkspaceEdit{
		Changes: map[string][]lspTextEdit{
			"file:///workspace/other.go": {{
				Range: lspRange{
					Start: lspPosition{Line: 0, Character: 0},
					End:   lspPosition{Line: 0, Character: 0},
				},
				NewText: "// other\n",
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "another file") {
		t.Fatalf("expected other-file edit rejection, got %v", err)
	}
}
