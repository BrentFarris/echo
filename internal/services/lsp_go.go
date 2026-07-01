package services

import (
	"strings"
	"unicode"
)

func init() {
	registerLSPLanguage(lspLanguageDefinition{
		ID:               "go",
		DisplayName:      "Go",
		Extensions:       []string{".go"},
		WorkspaceMarkers: []string{"go.mod", "go.work"},
		Command:          lspServerCommand{name: "gopls"},
		CompletionFilter: func(items []WorkspaceCompletionItem) []WorkspaceCompletionItem {
			return filterGoCompletionItems(items)
		},
	})
}

func filterGoCompletionItems(items []WorkspaceCompletionItem) []WorkspaceCompletionItem {
	if len(items) == 0 {
		return items
	}
	filtered := make([]WorkspaceCompletionItem, 0, len(items))
	for _, item := range items {
		if isPublicGoCompletionItem(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func isPublicGoCompletionItem(item WorkspaceCompletionItem) bool {
	switch item.Kind {
	case 9, 14, 15, 17, 18, 19, 24:
		return true
	}
	name := leadingGoIdentifier(item.Label)
	if name == "" {
		return true
	}
	if goPredeclaredIdentifier(name) {
		return true
	}
	return goIdentifierExported(name)
}

func leadingGoIdentifier(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	var builder strings.Builder
	for index, char := range label {
		if index == 0 {
			if char != '_' && !unicode.IsLetter(char) {
				return ""
			}
			builder.WriteRune(char)
			continue
		}
		if char != '_' && !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			break
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func goIdentifierExported(name string) bool {
	for _, char := range name {
		return unicode.IsUpper(char)
	}
	return false
}

func goPredeclaredIdentifier(name string) bool {
	_, ok := goPredeclaredIdentifiers[name]
	return ok
}

var goPredeclaredIdentifiers = map[string]struct{}{
	"any":        {},
	"append":     {},
	"bool":       {},
	"byte":       {},
	"cap":        {},
	"clear":      {},
	"close":      {},
	"comparable": {},
	"complex":    {},
	"complex64":  {},
	"complex128": {},
	"copy":       {},
	"delete":     {},
	"error":      {},
	"false":      {},
	"float32":    {},
	"float64":    {},
	"imag":       {},
	"int":        {},
	"int8":       {},
	"int16":      {},
	"int32":      {},
	"int64":      {},
	"iota":       {},
	"len":        {},
	"make":       {},
	"max":        {},
	"min":        {},
	"new":        {},
	"nil":        {},
	"panic":      {},
	"print":      {},
	"println":    {},
	"real":       {},
	"recover":    {},
	"rune":       {},
	"string":     {},
	"true":       {},
	"uint":       {},
	"uint8":      {},
	"uint16":     {},
	"uint32":     {},
	"uint64":     {},
	"uintptr":    {},
}
