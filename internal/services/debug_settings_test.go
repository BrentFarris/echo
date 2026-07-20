package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDebugSettingsTestService(t *testing.T, goWorkspace bool) (*SystemService, string, string, string) {
	t.Helper()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if goWorkspace {
		if err := os.WriteFile(filepath.Join(workspaceRoot, "go.mod"), []byte("module example.com/debugtest\n\ngo 1.24\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspaceRoot)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return service, state.ActiveWorkspaceID, workspaceRoot, storePath
}

func TestWorkspaceDebugSettingsRoundTripPreservesWorkspaceState(t *testing.T) {
	service, workspaceID, workspaceRoot, _ := newDebugSettingsTestService(t, true)
	cacheRoot := filepath.Join(workspaceRoot, workspaceCacheDirName)
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cacheRoot, workspaceStateFile)
	initialState := `{
  "version": 1,
  "tags": ["backend"],
  "git": {"parentRepositories":[{"folderId":"one","folderPath":"project","repositoryRoot":"C:/repo"}]},
  "futureSetting": {"keep": true}
}`
	if err := os.WriteFile(statePath, []byte(initialState), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := service.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatalf("load debug settings: %v", err)
	}
	if loaded.Revision == "" || !samePath(loaded.StoragePath, statePath) {
		t.Fatalf("unexpected initial settings view: %#v", loaded)
	}
	if !loaded.Implicit || loaded.SelectedConfiguration != implicitGoDebugConfiguration || len(loaded.Configurations) != 1 {
		t.Fatalf("expected implicit Go configuration, got %#v", loaded)
	}
	if !strings.Contains(loaded.JSON, `"configurations": []`) {
		t.Fatalf("expected an empty persisted debug document, got %s", loaded.JSON)
	}

	inputJSON := `{
  "version": "0.2.0",
  "futureDebugProperty": {"enabled": true},
  "configurations": [{
    "name": "Launch Go Package",
    "type": "go",
    "request": "launch",
    "mode": "debug",
    "program": "${workspaceFolder}",
    "adapterFutureOption": [1, 2, 3]
  }]
}`
	saved, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: inputJSON, ExpectedRevision: loaded.Revision})
	if err != nil {
		t.Fatalf("save debug settings: %v", err)
	}
	if saved.Implicit || saved.SelectedConfiguration != "Launch Go Package" || len(saved.Configurations) != 1 {
		t.Fatalf("unexpected saved settings view: %#v", saved)
	}
	if saved.Revision == loaded.Revision {
		t.Fatal("expected debug revision to change")
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "tags", "git", "futureSetting", "debug"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("workspace state lost %q: %s", key, data)
		}
	}
	if !strings.Contains(string(raw["debug"]), "adapterFutureOption") || !strings.Contains(string(raw["debug"]), "futureDebugProperty") {
		t.Fatalf("debug settings lost unknown fields: %s", raw["debug"])
	}
}

func TestWorkspaceDebugSettingsRevisionOnlyTracksDebugSection(t *testing.T) {
	service, workspaceID, workspaceRoot, _ := newDebugSettingsTestService(t, false)
	loaded, err := service.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON := `{"version":"0.2.0","configurations":[{"name":"One","type":"future-adapter","request":"launch","custom":true}]}`
	first, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: firstJSON, ExpectedRevision: loaded.Revision})
	if err != nil {
		t.Fatalf("save first: %v", err)
	}

	statePath := filepath.Join(workspaceRoot, workspaceCacheDirName, workspaceStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state["tags"] = []string{"changed-externally"}
	data, _ = json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(statePath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	secondJSON := `{"version":"0.2.0","configurations":[{"name":"Two","type":"go","request":"launch"}]}`
	second, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: secondJSON, ExpectedRevision: first.Revision})
	if err != nil {
		t.Fatalf("non-debug metadata caused a false conflict: %v", err)
	}

	state["debug"] = map[string]any{"version": workspaceDebugVersion, "configurations": []any{map[string]any{"name": "External", "type": "go", "request": "launch"}}}
	data, _ = json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(statePath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: firstJSON, ExpectedRevision: second.Revision})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected stale debug revision rejection, got %v", err)
	}
}

func TestWorkspaceDebugSettingsStrictValidation(t *testing.T) {
	service, workspaceID, _, _ := newDebugSettingsTestService(t, false)
	loaded, err := service.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "empty", json: "  ", want: "required"},
		{name: "comments", json: `{"version":"0.2.0",/* no comments */"configurations":[]}`, want: "strict JSON"},
		{name: "trailing", json: `{"version":"0.2.0","configurations":[]} true`, want: "strict JSON"},
		{name: "version", json: `{"version":"9","configurations":[]}`, want: "unsupported debug version"},
		{name: "duplicate names", json: `{"version":"0.2.0","configurations":[{"name":"Same","type":"go","request":"launch"},{"name":"Same","type":"cpp","request":"launch"}]}`, want: "duplicated"},
		{name: "missing type", json: `{"version":"0.2.0","configurations":[{"name":"No Type","request":"launch"}]}`, want: "type is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: test.json, ExpectedRevision: loaded.Revision})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}

	oversized := strings.Repeat(" ", workspaceDebugSettingsMaxBytes+1)
	if _, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: oversized, ExpectedRevision: loaded.Revision}); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("expected oversized input error, got %v", err)
	}
}

func TestWorkspaceDebugSettingsUsesFirstFolderOnly(t *testing.T) {
	service, workspaceID, firstRoot, _ := newDebugSettingsTestService(t, false)
	secondRoot := filepath.Join(t.TempDir(), "second")
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddWorkspaceFolder(workspaceID, secondRoot); err != nil {
		t.Fatalf("add second folder: %v", err)
	}
	loaded, err := service.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	document := `{"version":"0.2.0","configurations":[{"name":"First","type":"go","request":"launch"}]}`
	if _, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: document, ExpectedRevision: loaded.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(firstRoot, workspaceCacheDirName, workspaceStateFile)); err != nil {
		t.Fatalf("first folder did not own debug settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondRoot, workspaceCacheDirName, workspaceStateFile)); !os.IsNotExist(err) {
		t.Fatalf("second folder unexpectedly owned debug settings: %v", err)
	}

	if err := os.RemoveAll(firstRoot); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	for i := range service.state.Workspaces {
		if service.state.Workspaces[i].ID == workspaceID {
			service.state.Workspaces[i].Folders[0].Missing = true
		}
	}
	service.mu.Unlock()
	if _, err := service.LoadWorkspaceDebugSettings(workspaceID); err == nil || !strings.Contains(err.Error(), "first workspace folder") {
		t.Fatalf("expected unavailable first-folder error, got %v", err)
	}
}

func TestWorkspaceSelectedDebugConfigurationPersistsLocally(t *testing.T) {
	service, workspaceID, _, storePath := newDebugSettingsTestService(t, false)
	loaded, err := service.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	document := `{"version":"0.2.0","configurations":[{"name":"First","type":"go","request":"launch"},{"name":"Second","type":"go","request":"launch"}]}`
	if _, err := service.SaveWorkspaceDebugSettings(workspaceID, WorkspaceDebugSettingsInput{JSON: document, ExpectedRevision: loaded.Revision}); err != nil {
		t.Fatal(err)
	}
	selected, err := service.SetWorkspaceSelectedDebugConfiguration(workspaceID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	if selected.SelectedConfiguration != "Second" {
		t.Fatalf("unexpected selection: %#v", selected)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	settings, err := reloaded.LoadWorkspaceDebugSettings(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.SelectedConfiguration != "Second" {
		t.Fatalf("selection was not persisted locally: %#v", settings)
	}
	if _, err := reloaded.SetWorkspaceSelectedDebugConfiguration(workspaceID, "Missing"); err == nil {
		t.Fatal("expected missing configuration selection to fail")
	}
}

func TestResolveWorkspaceDebugConfigurationUsesImplicitGoLaunch(t *testing.T) {
	service, workspaceID, _, _ := newDebugSettingsTestService(t, true)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := resolveWorkspaceDebugConfiguration(workspace, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if configuration["name"] != implicitGoDebugConfiguration || configuration["program"] != "${workspaceFolder}" || configuration["type"] != "go" {
		t.Fatalf("unexpected implicit configuration: %#v", configuration)
	}
}

func TestWorkspaceDebugSettingsRejectsOversizedAndNonRegularStateFile(t *testing.T) {
	service, workspaceID, workspaceRoot, _ := newDebugSettingsTestService(t, false)
	cacheRoot := filepath.Join(workspaceRoot, workspaceCacheDirName)
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cacheRoot, workspaceStateFile)
	oversized := append([]byte(`{"version":1,"padding":"`), []byte(strings.Repeat("x", workspaceDebugSettingsMaxBytes))...)
	oversized = append(oversized, []byte(`"}`)...)
	if err := os.WriteFile(statePath, oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadWorkspaceDebugSettings(workspaceID); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("expected oversized workspace state error, got %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadWorkspaceDebugSettings(workspaceID); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected non-regular workspace state error, got %v", err)
	}
}
