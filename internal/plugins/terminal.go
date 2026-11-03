package plugins

import (
	"github.com/brent/echo/internal/domain"
	"net/url"
)

func terminalPlugin() domain.PluginManifest {
	return domain.PluginManifest{
		ID: "terminal", Name: "Terminal", Scope: "app",
		App:   &domain.AppRegistration{ViewID: "main", Icon: "data:image/svg+xml," + url.PathEscape(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="#283346"/><path d="m10 14 10 10-10 10m15 0h13" fill="none" stroke="white" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`)},
		Views: []domain.View{{ID: "main", Title: "Terminal", Kind: "embedded"}},
	}
}
