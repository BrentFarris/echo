package plugins

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

func appTestHost(t *testing.T) *Host {
	t.Helper()
	guest, err := os.ReadFile("guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	host, err := New(context.Background(), db, t.TempDir(), guest, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close(context.Background()) })
	return host
}

func TestAppRegistrationValidation(t *testing.T) {
	host := appTestHost(t)
	for _, tc := range []struct {
		name, view, icon string
		invalid          bool
	}{
		{"floating", "main", "", false},
		{"svg", "main", "data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'/%3E", false},
		{"png", "main", "data:image/png;base64,aWNvbg==", false},
		{"jpeg", "main", "data:image/jpeg;base64,aWNvbg==", false},
		{"webp", "main", "data:image/webp;base64,aWNvbg==", false},
		{"empty view", "", "", true},
		{"missing view", "absent", "", true},
		{"renderer", "render", "", true},
		{"external", "main", "https://example.com/icon.png", true},
		{"html", "main", "data:text/html,<script>alert(1)</script>", true},
		{"gif", "main", "data:image/gif;base64,aWNvbg==", true},
		{"empty image", "main", "data:image/png,", true},
		{"bad base64", "main", "data:image/png;base64,!!!", true},
		{"bad escape", "main", "data:image/svg+xml,%oops", true},
		{"too large", "main", "data:image/svg+xml," + strings.Repeat("a", 256<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := domain.PluginManifest{API: 1, ID: "calculator", Name: "Calculator", Views: []domain.View{{ID: "render", Kind: "renderer"}, {ID: "main", HTML: "<p>Calculator</p>"}}, App: &domain.AppRegistration{ViewID: tc.view, Icon: tc.icon}}
			_, err := host.Install(m, nil)
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%t, error=%v", tc.invalid, err)
			}
		})
	}
}

func TestAppMetadataDigestRollbackAndExplicitRegistration(t *testing.T) {
	host := appTestHost(t)
	template, err := Template("ui")
	if err != nil {
		t.Fatal(err)
	}
	m := template.(map[string]any)["manifest"].(domain.PluginManifest)
	if m.App == nil || m.App.ViewID != "main" {
		t.Fatal("UI template did not register its app")
	}
	first, err := host.Install(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.App = &domain.AppRegistration{ViewID: "main", Icon: "data:image/svg+xml,%3Csvg/%3E"}
	second, err := host.Install(m, nil)
	if err != nil || first.Digest == second.Digest {
		t.Fatal("registration did not change package digest", err)
	}
	if err := host.Rollback(m.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := host.Get(m.ID)
	if err != nil || restored.Digest != first.Digest || restored.App == nil || restored.App.Icon != "" {
		t.Fatal("app metadata not rolled back", restored, err)
	}
	m.ID = "unregistered"
	m.App = nil
	unregistered, err := host.Install(m, nil)
	if err != nil || unregistered.App != nil {
		t.Fatal("floating view was implicitly registered", err)
	}
}

func TestFloatingLaunchSelectsRegisteredViewAndExcludesRenderers(t *testing.T) {
	m := domain.PluginManifest{Views: []domain.View{{ID: "render", Kind: "renderer"}, {ID: "one", Kind: "floating"}, {ID: "two", Kind: "floating"}}}
	view, err := FloatingView(m, "")
	if err != nil || view.ID != "one" {
		t.Fatal(view, err)
	}
	m.App = &domain.AppRegistration{ViewID: "two"}
	view, err = FloatingView(m, "")
	if err != nil || view.ID != "two" {
		t.Fatal(view, err)
	}
	view, err = FloatingView(m, "one")
	if err != nil || view.ID != "one" {
		t.Fatal(view, err)
	}
	if _, err = FloatingView(m, "render"); err == nil {
		t.Fatal("renderer opened as floating view")
	}
}

func TestEmbeddedAppRegistrationAndBuiltinProvenance(t *testing.T) {
	host := appTestHost(t)
	installed, err := host.Install(domain.PluginManifest{API: 1, ID: "embedded-example", Name: "Embedded example", Builtin: true, App: &domain.AppRegistration{ViewID: "main"}, Views: []domain.View{{ID: "main", Kind: "embedded", HTML: "<p>Embedded</p>"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := FloatingView(installed, "")
	if err != nil || view.Kind != "embedded" || installed.Builtin {
		t.Fatal("presentation or provenance changed", installed, err)
	}
	builtin, err := host.Get("monaco-editor")
	if err != nil || !builtin.Builtin || builtin.Scope != "app" || builtin.App == nil || builtin.Views[0].Kind != "embedded" {
		t.Fatal(builtin, err)
	}
	if _, err := host.Install(domain.PluginManifest{API: 1, ID: "monaco-editor", Name: "Impersonation"}, nil); err == nil {
		t.Fatal("built-in registry entry replaced")
	}
}
