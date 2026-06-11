package services

import (
	"strings"
	"testing"
)

func TestGoLSPLanguageRegistration(t *testing.T) {
	languageID, ok := lspLanguageIDForPath("main.go")
	if !ok || languageID != "go" {
		t.Fatalf("expected .go files to use go LSP language, got %q, %v", languageID, ok)
	}
	command, ok := registeredLSPCommandForLanguage(languageID)
	if !ok || command.name != "gopls" {
		t.Fatalf("expected go LSP command to be gopls, got %#v, %v", command, ok)
	}
}

func TestFilterGoCompletionItemsKeepsPublicReachableSymbols(t *testing.T) {
	items := []WorkspaceCompletionItem{
		{Label: "Println", Kind: 3},
		{Label: "privateHelper", Kind: 3},
		{Label: "ExportedField", Kind: 5},
		{Label: "privateField", Kind: 5},
		{Label: "fmt", Kind: 9},
		{Label: "for", Kind: 14},
		{Label: "len", Kind: 3},
		{Label: "error", Kind: 7},
		{Label: "_private", Kind: 6},
	}

	filtered := filterGoCompletionItems(items)
	labels := make([]string, 0, len(filtered))
	for _, item := range filtered {
		labels = append(labels, item.Label)
	}

	expected := []string{"Println", "ExportedField", "fmt", "for", "len", "error"}
	if strings.Join(labels, ",") != strings.Join(expected, ",") {
		t.Fatalf("expected filtered labels %v, got %v", expected, labels)
	}
}
