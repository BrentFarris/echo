package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestLoadReturnsDefaultsWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "echo.json"))
	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Endpoints) == 0 {
		t.Fatalf("expected at least one default endpoint")
	}
	if cfg.Endpoint == "" {
		t.Fatalf("expected a default endpoint url")
	}
}

func TestLoadReturnsDefaultsWhenSharedSettingsAreNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"settings":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("load null settings: %v", err)
	}
	if len(cfg.Endpoints) == 0 || cfg.Endpoint == "" || cfg.Model == "" {
		t.Fatalf("expected defaults for null shared settings: %#v", cfg)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.json")
	store := NewStore(path)

	cfg := llm.DefaultSettings()
	cfg.Endpoints = append(cfg.Endpoints, llm.LLMEndpoint{
		ID:       "second",
		Name:     "Second",
		Endpoint: "http://example.com/v1",
		Model:    "model-b",
	})
	cfg.EndpointSelection.Chat = "second"

	if err := store.Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(loaded.Endpoints))
	}
	if loaded.EndpointSelection.Chat != "second" {
		t.Fatalf("expected chat routing to second, got %q", loaded.EndpointSelection.Chat)
	}
	// The saved file should exist at the expected path.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected settings file at %s: %v", path, err)
	}
}

func TestLoadHandlesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.json")
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := NewStore(path)
	if _, err := store.Load(); err == nil {
		t.Fatalf("expected error for corrupt file")
	}
}
