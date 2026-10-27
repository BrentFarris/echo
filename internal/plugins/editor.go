package plugins

import (
	"github.com/brent/echo/internal/domain"
	"net/url"
)

func monacoEditorPlugin() domain.PluginManifest {
	return domain.PluginManifest{
		ID: "monaco-editor", Name: "Monaco Editor", Scope: "app",
		App:   &domain.AppRegistration{ViewID: "main", Icon: "data:image/svg+xml," + url.PathEscape(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="#1464c0"/><path d="m17 14-10 10 10 10m14-20 10 10-10 10m-5-23-5 26" fill="none" stroke="white" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`)},
		Views: []domain.View{{ID: "main", Title: "Monaco Editor", Kind: "embedded"}},
		Settings: Schema(map[string]any{
			"editorFontSize":                  map[string]any{"type": "number", "description": "Editor font size (8–30; default 13.5)"},
			"editorInsertSpaces":              B("Indent using spaces"),
			"editorTabSize":                   map[string]any{"type": "integer", "description": "Indentation width (1–8; default 4)"},
			"hideLeadingWhitespaceIndicators": B("Hide whitespace indicators"),
			"editorColumnGuidesEnabled":       B("Show column guides"),
			"editorColumnGuides":              S("Guide columns, comma separated (default 80, 90)"),
			"editorLineEndingsOnSave":         S("Line endings on save: unchanged, lf or crlf"),
		}),
	}
}
