package appdata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.json")
	s := NewStore(path)

	f := File{
		Settings: json.RawMessage(`{"endpoint":"http://x"}`),
		Workspaces: []Workspace{
			{ID: "ws-1", Name: "A", MainPath: "/a", IconExt: "png", Folders: []string{"/a", "/b"}},
		},
	}
	if err := s.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var gotSettings map[string]any
	if err := json.Unmarshal(got.Settings, &gotSettings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	if gotSettings["endpoint"] != "http://x" {
		t.Fatalf("unexpected settings: %s", got.Settings)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].Name != "A" {
		t.Fatalf("unexpected workspaces: %+v", got.Workspaces)
	}
}

func TestLoadMigratesLegacyBareSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "echo.json")
	// A legacy file written as a bare settings object (no "settings" key).
	if err := os.WriteFile(path, []byte(`{"endpoint":"http://legacy","model":"m"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := NewStore(path)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(got.Settings) != `{"endpoint":"http://legacy","model":"m"}` {
		t.Fatalf("unexpected migrated settings: %s", got.Settings)
	}
	if len(got.Workspaces) != 0 {
		t.Fatalf("expected no workspaces, got %+v", got.Workspaces)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "echo.json"))
	f, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(f.Settings) != 0 || len(f.Workspaces) != 0 {
		t.Fatalf("expected empty file, got %+v", f)
	}
}

func TestWorkspaceParentSearchPrefersProviderNeutralKey(t *testing.T) {
	var explicit Workspace
	if err := json.Unmarshal([]byte(`{"searchParentRepositories":false,"searchParentGitRepositories":true}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.ParentRepositorySearchEnabled() {
		t.Fatal("explicit provider-neutral false must override the deprecated true value")
	}

	var legacy Workspace
	if err := json.Unmarshal([]byte(`{"searchParentGitRepositories":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if !legacy.ParentRepositorySearchEnabled() {
		t.Fatal("deprecated value was not used when the provider-neutral key was absent")
	}
}
