package services

import (
	"encoding/json"
	"testing"
)

func TestLSPQueryLineColumnMapping(t *testing.T) {
	content := "a🙂b\n\tfield\n"
	position, offset, err := lspPositionForLineColumn(content, 1, 3)
	if err != nil {
		t.Fatalf("position for line/column: %v", err)
	}
	if position != (lspPosition{Line: 0, Character: 3}) || offset != 3 {
		t.Fatalf("expected line 0 character 3 offset 3, got %#v offset %d", position, offset)
	}

	codePosition := codePositionFromLSP(content, position)
	if codePosition.Line != 1 || codePosition.Column != 3 || codePosition.Offset != 3 {
		t.Fatalf("expected 1-based code position at 1:3, got %#v", codePosition)
	}
}

func TestLSPQueryFlattensDocumentSymbols(t *testing.T) {
	content := "package main\n\ntype Server struct {}\n\nfunc (s Server) Start() {}\n"
	raw := json.RawMessage(`[
		{
			"name": "Server",
			"kind": 23,
			"range": {"start": {"line": 2, "character": 0}, "end": {"line": 2, "character": 21}},
			"selectionRange": {"start": {"line": 2, "character": 5}, "end": {"line": 2, "character": 11}},
			"children": [
				{
					"name": "Start",
					"kind": 6,
					"range": {"start": {"line": 4, "character": 0}, "end": {"line": 4, "character": 28}},
					"selectionRange": {"start": {"line": 4, "character": 16}, "end": {"line": 4, "character": 21}}
				}
			]
		}
	]`)

	navigator := workspaceCodeNavigator{}
	symbols, err := navigator.codeDocumentSymbols(WorkspaceFile{Path: "app/main.go", Content: content}, raw)
	if err != nil {
		t.Fatalf("document symbols: %v", err)
	}
	if len(symbols) != 2 {
		t.Fatalf("expected two flattened symbols, got %#v", symbols)
	}
	if symbols[0].Name != "Server" || symbols[0].KindName != "struct" || symbols[0].Range.Start.Line != 3 {
		t.Fatalf("unexpected parent symbol: %#v", symbols[0])
	}
	if symbols[1].Name != "Start" || symbols[1].ContainerName != "Server" || symbols[1].KindName != "method" {
		t.Fatalf("unexpected child symbol: %#v", symbols[1])
	}
}
