package services

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLSPUTF16PositionMapping(t *testing.T) {
	content := "a🙂b\néx"
	cases := []struct {
		offset   int
		position lspPosition
	}{
		{offset: 0, position: lspPosition{Line: 0, Character: 0}},
		{offset: 1, position: lspPosition{Line: 0, Character: 1}},
		{offset: 3, position: lspPosition{Line: 0, Character: 3}},
		{offset: 4, position: lspPosition{Line: 0, Character: 4}},
		{offset: 5, position: lspPosition{Line: 1, Character: 0}},
		{offset: 6, position: lspPosition{Line: 1, Character: 1}},
	}
	for _, tc := range cases {
		if got := lspPositionFromUTF16Offset(content, tc.offset); got != tc.position {
			t.Fatalf("offset %d: expected position %#v, got %#v", tc.offset, tc.position, got)
		}
		if got := utf16OffsetForPosition(content, tc.position); got != tc.offset {
			t.Fatalf("position %#v: expected offset %d, got %d", tc.position, tc.offset, got)
		}
	}
}

func TestParseLSPCompletionResponseUsesTextEdits(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Pr\n}\n"
	position := utf16Length(content[:strings.Index(content, "Pr")+len("Pr")])
	raw := json.RawMessage(`{
		"isIncomplete": false,
		"items": [
			{
				"label": "Println",
				"kind": 3,
				"detail": "func Println(a ...any) (n int, err error)",
				"documentation": {"kind": "markdown", "value": "Println formats its operands."},
				"textEdit": {
					"range": {
						"start": {"line": 3, "character": 5},
						"end": {"line": 3, "character": 7}
					},
					"newText": "Println"
				},
				"additionalTextEdits": [
					{
						"range": {
							"start": {"line": 1, "character": 0},
							"end": {"line": 1, "character": 0}
						},
						"newText": "import \"fmt\"\n"
					}
				]
			}
		]
	}`)

	response, err := parseLSPCompletionResponse(raw, content, position)
	if err != nil {
		t.Fatalf("parse completion response: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("expected one completion item, got %#v", response.Items)
	}
	item := response.Items[0]
	expectedFrom := utf16Length(content[:strings.Index(content, "Pr")])
	if item.From != expectedFrom || item.To != position {
		t.Fatalf("expected completion edit range %d-%d, got %d-%d", expectedFrom, position, item.From, item.To)
	}
	if item.InsertText != "Println" || item.Label != "Println" || item.Kind != 3 {
		t.Fatalf("unexpected item metadata: %#v", item)
	}
	if item.Documentation != "Println formats its operands." {
		t.Fatalf("expected markdown documentation value, got %q", item.Documentation)
	}
	if len(item.AdditionalTextEdits) != 1 {
		t.Fatalf("expected import edit, got %#v", item.AdditionalTextEdits)
	}
	importOffset := utf16Length("package main\n")
	edit := item.AdditionalTextEdits[0]
	if edit.From != importOffset || edit.To != importOffset || edit.NewText != "import \"fmt\"\n" {
		t.Fatalf("unexpected import edit: %#v", edit)
	}
}

func TestParseLSPCompletionResponseUsesFallbackRange(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tPrin\n}\n"
	position := utf16Length(content[:strings.Index(content, "Prin")+len("Prin")])
	response, err := parseLSPCompletionResponse(
		json.RawMessage(`[{"label":"Println","kind":3}]`),
		content,
		position,
	)
	if err != nil {
		t.Fatalf("parse completion response: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("expected one completion item, got %#v", response.Items)
	}
	expectedFrom := utf16Length(content[:strings.Index(content, "Prin")])
	if response.Items[0].From != expectedFrom || response.Items[0].To != position {
		t.Fatalf("expected fallback range %d-%d, got %d-%d", expectedFrom, position, response.Items[0].From, response.Items[0].To)
	}
}

func TestParseLSPDefinitionResponse(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"targetUri": "file:///C:/work/main.go",
			"targetRange": {
				"start": {"line": 2, "character": 0},
				"end": {"line": 4, "character": 1}
			},
			"targetSelectionRange": {
				"start": {"line": 3, "character": 5},
				"end": {"line": 3, "character": 11}
			}
		},
		{
			"uri": "file:///C:/work/other.go",
			"range": {
				"start": {"line": 6, "character": 2},
				"end": {"line": 6, "character": 8}
			}
		}
	]`)

	locations, err := parseLSPDefinitionResponse(raw)
	if err != nil {
		t.Fatalf("parse definition response: %v", err)
	}
	if len(locations) != 2 {
		t.Fatalf("expected two locations, got %#v", locations)
	}
	if locations[0].URI != "file:///C:/work/main.go" || locations[0].Range.Start != (lspPosition{Line: 3, Character: 5}) {
		t.Fatalf("expected location link target selection range, got %#v", locations[0])
	}
	if locations[1].URI != "file:///C:/work/other.go" || locations[1].Range.Start != (lspPosition{Line: 6, Character: 2}) {
		t.Fatalf("expected location range, got %#v", locations[1])
	}
}

func TestWorkspaceReferenceLocationsUseActiveContentAndFilterWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	mainPath := filepath.Join(root, "main.go")
	otherPath := filepath.Join(root, "other.go")
	outsidePath := filepath.Join(t.TempDir(), "outside.go")

	if err := os.WriteFile(mainPath, []byte("package main\n\nvar diskOnly = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("package main\n\nfunc use() {\n\tName()\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	activeContent := "package main\r\n\r\nfunc Name() {}\r\n\r\nfunc main() {\r\n\tName()\r\n}\r\n"
	locations := []lspDefinitionLocation{
		{
			URI: fileURI(mainPath),
			Range: lspRange{
				Start: lspPosition{Line: 2, Character: 5},
				End:   lspPosition{Line: 2, Character: 9},
			},
		},
		{
			URI: fileURI(mainPath),
			Range: lspRange{
				Start: lspPosition{Line: 5, Character: 1},
				End:   lspPosition{Line: 5, Character: 5},
			},
		},
		{
			URI: fileURI(otherPath),
			Range: lspRange{
				Start: lspPosition{Line: 3, Character: 1},
				End:   lspPosition{Line: 3, Character: 5},
			},
		},
		{
			URI: fileURI(outsidePath),
			Range: lspRange{
				Start: lspPosition{Line: 0, Character: 0},
				End:   lspPosition{Line: 0, Character: 4},
			},
		},
	}

	references, skippedOutside := workspaceReferenceLocations(workspace, mainPath, activeContent, locations)
	if skippedOutside != 1 {
		t.Fatalf("expected one outside-workspace location to be skipped, got %d", skippedOutside)
	}
	if len(references) != 3 {
		t.Fatalf("expected three workspace references, got %#v", references)
	}
	if references[0].Path != workspaceRelativePath(workspace, mainPath) {
		t.Fatalf("expected source path %q, got %q", workspaceRelativePath(workspace, mainPath), references[0].Path)
	}
	if references[0].Preview != "func Name() {}" {
		t.Fatalf("expected active editor content preview, got %q", references[0].Preview)
	}
	expectedCallOffset := utf16Length(activeContent[:strings.Index(activeContent, "\tName")+1])
	if references[1].Range.Start.Offset != expectedCallOffset {
		t.Fatalf("expected CRLF-aware call offset %d, got %d", expectedCallOffset, references[1].Range.Start.Offset)
	}
	if references[1].PreviewLines == nil || references[1].PreviewLines[4].HighlightStart != 1 || references[1].PreviewLines[4].HighlightEnd != 5 {
		t.Fatalf("expected target line highlight 1-5, got %#v", references[1].PreviewLines)
	}
	if references[2].Path != workspaceRelativePath(workspace, otherPath) || references[2].Preview != "\tName()" {
		t.Fatalf("expected disk-backed other-file preview, got %#v", references[2])
	}
}

func TestSystemServiceCompleteWorkspaceFileWithGopls(t *testing.T) {
	if os.Getenv("ECHO_RUN_GOPLS_INTEGRATION") != "1" {
		t.Skip("set ECHO_RUN_GOPLS_INTEGRATION=1 to run the real gopls integration test")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls was not found on PATH: %v", err)
	}
	base := os.Getenv("ECHO_GOPLS_INTEGRATION_DIR")
	if base == "" {
		if runtime.GOOS == "windows" {
			base = filepath.Join(`C:\tmp`, "echo-gopls-integration")
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			base = filepath.Join(cwd, "..", "..", ".gotmp", "gopls-integration")
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "workspace-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	defer service.Shutdown()
	logPath := filepath.Join(root, "gopls.log")
	oldCommandForLanguage := lspCommandForLanguage
	lspCommandForLanguage = func(languageID string) (lspServerCommand, bool) {
		if languageID != "go" {
			return lspServerCommand{}, false
		}
		return lspServerCommand{
			name: "gopls",
			args: []string{"-rpc.trace", "-logfile=" + logPath, "serve"},
		}, true
	}
	defer func() {
		lspCommandForLanguage = oldCommandForLanguage
	}()

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/echo_lsp_test\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := "package main\n\nimport \"fmt\"\n\nfunc helper() {}\n\nfunc main() {\n\thelper()\n\tfmt.Pr\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	position := utf16Length(content[:strings.Index(content, "Pr")+len("Pr")])

	response, err := service.CompleteWorkspaceFile(workspaceID, WorkspaceCompletionRequest{
		FilePath:    "main.go",
		Content:     content,
		Position:    position,
		TriggerKind: 1,
	})
	if err != nil {
		if data, readErr := os.ReadFile(logPath); readErr == nil {
			t.Logf("gopls log:\n%s", data)
		}
		t.Fatalf("complete workspace file: %v", err)
	}
	for _, item := range response.Items {
		if item.Label == "Println" {
			definition, err := service.FindWorkspaceFileDefinition(workspaceID, WorkspaceDefinitionRequest{
				FilePath: "main.go",
				Content:  content,
				Position: utf16Length(content[:strings.LastIndex(content, "helper()")+len("helper")]),
			})
			if err != nil {
				t.Fatalf("find definition: %v", err)
			}
			if !definition.Found || definition.TargetPath != "main.go" {
				t.Fatalf("expected main definition in main.go, got %#v", definition)
			}
			return
		}
	}
	t.Fatalf("expected Println completion, got %#v", response.Items)
}
