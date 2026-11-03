package plugins

import (
	"github.com/brent/echo/internal/domain"
	"net/url"
)

func notesPlugin() domain.PluginManifest {
	return domain.PluginManifest{
		ID: "notes", Name: "Notes", Scope: "app",
		App:   &domain.AppRegistration{ViewID: "main", Icon: "data:image/svg+xml," + url.PathEscape(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="#b77920"/><path d="M13 9h17l6 6v24H13zM29 9v8h7M19 24h11M19 30h11" fill="none" stroke="white" stroke-width="2.5" stroke-linejoin="round"/></svg>`)},
		Views: []domain.View{{ID: "main", Title: "Notes", Kind: "embedded"}},
	}
}
