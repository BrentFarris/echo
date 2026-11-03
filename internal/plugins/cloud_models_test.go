package plugins

import (
	"context"
	"os"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

func TestCloudModelsMigrationPreservesConnections(t *testing.T) {
	guest, err := os.ReadFile("guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Put("plugins", "cloud-models", domain.PluginManifest{ID: "cloud-models", Builtin: true, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	endpoint := domain.Endpoint{ID: "saved-cloud", URL: "https://example.com/v1", Cloud: true, Model: "saved-model", Provider: "openai-compatible-cloud", Credential: "saved-credential"}
	if err = db.Put("endpoints", endpoint.ID, endpoint); err != nil {
		t.Fatal(err)
	}
	h, err := New(context.Background(), db, t.TempDir(), guest, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	manifests, err := h.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range manifests {
		if manifest.ID == "cloud-models" {
			t.Fatal("obsolete Cloud Models switch is still installed")
		}
	}
	var saved domain.Endpoint
	if err = db.Get("endpoints", endpoint.ID, &saved); err != nil || saved != endpoint {
		t.Fatal("migration changed the saved connection", saved, err)
	}
	provider, err := h.Provider(domain.Bot{ID: "bot", Plugins: map[string]bool{"cloud-models": false}}, saved.Provider)
	if err != nil || !provider.Builtin || !provider.Enabled {
		t.Fatal("legacy cloud provider is unavailable", provider, err)
	}
}
