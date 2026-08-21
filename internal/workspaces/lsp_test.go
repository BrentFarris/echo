package workspaces

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/lspconfig"
)

func TestLanguageServerConfigPersistsWithoutBreakingPortableWorkspacePaths(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main")
	extraPath := filepath.Join(directory, "extra")
	for _, path := range []string{mainPath, extraPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	workspace, err := manager.Create(CreateRequest{Name: "LSP Workspace", MainPath: mainPath, Folders: []string{extraPath}})
	if err != nil {
		t.Fatal(err)
	}
	command := "custom-gopls"
	config := lspconfig.WorkspaceConfig{
		EnabledProfileIDs: []string{"gopls"}, FormatOnSave: true, FormatOnSaveTimeout: 1250,
		Overrides: map[string]lspconfig.ProfileOverride{"gopls": {Command: &command}},
	}
	updated, err := manager.SetLanguageServerConfig(workspace.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LanguageServers.FormatOnSave || updated.LanguageServers.FormatOnSaveTimeout != 1250 {
		t.Fatalf("config not returned: %+v", updated.LanguageServers)
	}

	data, err := os.ReadFile(filepath.Join(mainPath, EchoDirName, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted workspaceFile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.MainPath != "../" || len(persisted.Folders) != 2 || persisted.Folders[1] != "../../extra" {
		t.Fatalf("portable paths changed: %+v", persisted)
	}
	if len(persisted.LanguageServers.EnabledProfileIDs) != 1 || persisted.LanguageServers.EnabledProfileIDs[0] != "gopls" {
		t.Fatalf("language server config missing: %+v", persisted.LanguageServers)
	}
	if got := persisted.LanguageServers.Overrides["gopls"].Command; got == nil || *got != command {
		t.Fatalf("workspace override missing: %+v", persisted.LanguageServers.Overrides)
	}
}
