package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAgentModeMigrationFromGlobalState verifies that agent modes stored in
// state.json are migrated to workspace disk storage on first load.
func TestAgentModeMigrationFromGlobalState(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := t.TempDir()
	storePath := filepath.Join(root, "state.json")

	// Create a state file with legacy global agentModes and a workspace.
	stored := storedAppState{
		Workspaces: []Workspace{
			{
				ID:          "ws-1",
				DisplayName: "test",
				Folders: []WorkspaceFolder{
					{
						ID:        "f-1",
						Label:     "test",
						Path:      workspaceRoot,
						UseAgents: true,
					},
				},
			},
		},
		ActiveWorkspaceID: "ws-1",
		KanbanCards:       []KanbanCard{},
	}

	// Write state with agentModes in the raw format.
	rawData := map[string]any{
		"settings":          map[string]any{},
		"webAccess":         map[string]any{},
		"workspaces":        stored.Workspaces,
		"activeWorkspaceId": "ws-1",
		"agentModes": []map[string]any{
			{
				"id":      "general",
				"name":    "General",
				"builtIn": true,
			},
			{
				"id":      "plan",
				"name":    "Plan",
				"builtIn": true,
			},
			{
				"id":              "custom-migration-id",
				"name":            "Migrated Mode",
				"builtIn":         false,
				"toolPermissions": []string{"filesystem_read_text"},
				"pathPermissions": []string{"src/**"},
				"prompt":          "Legacy mode prompt",
			},
		},
		"kanbanCards":   []any{},
		"chatSessions":  map[string]any{},
	}

	data, err := json.MarshalIndent(rawData, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(storePath, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// Verify the JSON can be unmarshaled into storedAppStateRaw.
	var rawCheck storedAppStateRaw
	if err := json.Unmarshal(data, &rawCheck); err != nil {
		t.Fatalf("unmarshal check: %v", err)
	}
	if len(rawCheck.AgentModes) != 3 {
		t.Fatalf("expected 3 agent modes in raw data, got %d", len(rawCheck.AgentModes))
	}
	if len(rawCheck.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace in raw data, got %d", len(rawCheck.Workspaces))
	}

	// Load the service; migration should happen automatically.
	svc := NewSystemServiceWithStorePath(storePath)
	state := svc.LoadState()

	// Verify the workspace was loaded.
	if len(state.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(state.Workspaces))
	}

	// Check that modes directory exists on disk.
	modesDir := filepath.Join(workspaceRoot, ".echo", "modes-ws-1")
	children, err := os.ReadDir(modesDir)
	if err != nil {
		t.Fatalf("read modes directory before listing: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 mode on disk after migration, got %d (dir=%s)", len(children), modesDir)
	}

	// List modes should include built-ins + migrated mode.
	modes := svc.ListAgentModes("")
	var migrated *AgentMode
	for i, m := range modes {
		if m.Name == "Migrated Mode" {
			migrated = &modes[i]
			break
		}
	}
	if migrated == nil {
		t.Fatalf("expected Migrated Mode to be present after migration; got %d modes: %v", len(modes), func() []string {
			names := make([]string, len(modes))
			for i, m := range modes {
				names[i] = m.Name
			}
			return names
		}())
	}
	if migrated.BuiltIn {
		t.Fatal("migrated mode should not be built-in")
	}
	if len(migrated.ToolPermissions) != 1 || migrated.ToolPermissions[0] != "filesystem_read_text" {
		t.Fatalf("unexpected migrated permissions: %#v", migrated.ToolPermissions)
	}

	// Verify the state file no longer has agentModes.
	newData, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read new state: %v", err)
	}
	var newStored map[string]json.RawMessage
	if err := json.Unmarshal(newData, &newStored); err != nil {
		t.Fatalf("decode new state: %v", err)
	}
	if _, ok := newStored["agentModes"]; ok {
		t.Fatal("state file should not contain agentModes after migration")
	}

	// Reload and verify the mode persists.
	svc2 := NewSystemServiceWithStorePath(storePath)
	modes2 := svc2.ListAgentModes("")
	var migrated2 *AgentMode
	for i, m := range modes2 {
		if m.Name == "Migrated Mode" {
			migrated2 = &modes2[i]
			break
		}
	}
	if migrated2 == nil {
		t.Fatal("migrated mode should persist across reloads")
	}
}

// TestWorkspaceScopedModesIsolation verifies that modes created in one
// workspace do not appear in another workspace sharing the same folder.
func TestWorkspaceScopedModesIsolation(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")

	workspaceRootA := filepath.Join(root, "workspace-a")
	workspaceRootB := filepath.Join(root, "workspace-b")
	if err := os.MkdirAll(workspaceRootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceRootB, 0o755); err != nil {
		t.Fatal(err)
	}

	svc := NewSystemServiceWithStorePath(storePath)

	// Add two workspaces pointing to different folders.
	stateA, err := svc.AddWorkspace(workspaceRootA)
	if err != nil {
		t.Fatalf("add workspace A: %v", err)
	}
	workspaceAID := stateA.ActiveWorkspaceID

	stateB, err := svc.AddWorkspace(workspaceRootB)
	if err != nil {
		t.Fatalf("add workspace B: %v", err)
	}
	workspaceBID := stateB.ActiveWorkspaceID

	// Ensure cache folders for both workspaces so modes directories exist.
	for _, wid := range []string{workspaceAID, workspaceBID} {
		if _, err := svc.ensureWorkspaceCacheFolders(wid); err != nil {
			t.Fatalf("ensure cache folders for %s: %v", wid, err)
		}
	}

	// Activate workspace A and create a mode.
	svc.SetActiveWorkspace(workspaceAID)
	modesA, err := svc.CreateAgentMode("Mode-A", "Prompt A", nil, nil)
	if err != nil {
		t.Fatalf("create mode in workspace A: %v", err)
	}
	var modeA *AgentMode
	for i, m := range modesA {
		if m.Name == "Mode-A" {
			modeA = &modesA[i]
			break
		}
	}
	if modeA == nil {
		t.Fatal("expected Mode-A to be created")
	}

	// Activate workspace B and create a different mode.
	svc.SetActiveWorkspace(workspaceBID)
	modesB, err := svc.CreateAgentMode("Mode-B", "Prompt B", nil, nil)
	if err != nil {
		t.Fatalf("create mode in workspace B: %v", err)
	}
	var modeB *AgentMode
	for i, m := range modesB {
		if m.Name == "Mode-B" {
			modeB = &modesB[i]
			break
		}
	}
	if modeB == nil {
		t.Fatal("expected Mode-B to be created")
	}

	// Verify workspace A sees its own mode but not B's.
	svc.SetActiveWorkspace(workspaceAID)
	listA := svc.ListAgentModes(workspaceAID)
	foundModeA := false
	for _, m := range listA {
		if m.Name == "Mode-A" {
			foundModeA = true
		}
		if m.Name == "Mode-B" {
			t.Fatal("workspace A should not see Mode-B")
		}
	}
	if !foundModeA {
		t.Fatal("workspace A should see its own Mode-A")
	}

	// Verify workspace B sees its own mode but not A's.
	svc.SetActiveWorkspace(workspaceBID)
	listB := svc.ListAgentModes(workspaceBID)
	foundModeB := false
	for _, m := range listB {
		if m.Name == "Mode-B" {
			foundModeB = true
		}
		if m.Name == "Mode-A" {
			t.Fatal("workspace B should not see Mode-A")
		}
	}
	if !foundModeB {
		t.Fatal("workspace B should see its own Mode-B")
	}

	// Verify on-disk isolation: each workspace has its own scoped directory.
	state := svc.LoadState()
	var wsA, wsB Workspace
	for _, w := range state.Workspaces {
		if w.ID == workspaceAID {
			wsA = w
		}
		if w.ID == workspaceBID {
			wsB = w
		}
	}
	aModesDir := filepath.Join(wsA.Folders[0].Path, ".echo", "modes-"+workspaceAID)
	bModesDir := filepath.Join(wsB.Folders[0].Path, ".echo", "modes-"+workspaceBID)

	aChildren, err := os.ReadDir(aModesDir)
	if err != nil {
		t.Fatalf("read workspace A modes dir: %v", err)
	}
	if len(aChildren) != 1 {
		t.Fatalf("expected 1 mode in workspace A, got %d", len(aChildren))
	}

	bChildren, err := os.ReadDir(bModesDir)
	if err != nil {
		t.Fatalf("read workspace B modes dir: %v", err)
	}
	if len(bChildren) != 1 {
		t.Fatalf("expected 1 mode in workspace B, got %d", len(bChildren))
	}

	// Verify the mode directories are different UUIDs.
	if aChildren[0].Name() == bChildren[0].Name() {
		t.Fatal("workspace A and B modes should have different directory names")
	}
}

// TestLegacyMigrationMovesModeFilesToScopedPath verifies that the legacy
// .echo/modes/ directory is renamed to .echo/modes-{workspaceID}/ during
// workspace cache initialization, and that the migrated mode files are
// correctly loaded by ListAgentModes.
func TestLegacyMigrationMovesModeFilesToScopedPath(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")

	workspaceRoot := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	svc := NewSystemServiceWithStorePath(storePath)

	// Add a workspace.
	state, err := svc.AddWorkspace(workspaceRoot)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	// Resolve the workspace folder path.
	loadedState := svc.LoadState()
	var ws Workspace
	for _, w := range loadedState.Workspaces {
		if w.ID == workspaceID {
			ws = w
			break
		}
	}
	folderPath := ws.Folders[0].Path

	// Create the legacy .echo/modes/ directory with a mode file.
	legacyPath := filepath.Join(folderPath, ".echo", "modes")
	modeUUID := "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6"
	modeDir := filepath.Join(legacyPath, modeUUID)
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		t.Fatalf("create legacy modes dir: %v", err)
	}

	// Write a valid mode.json.
	modeJSON := map[string]any{
		"id":   modeUUID,
		"name": "Legacy Mode",
		"prompt": "I am from the old days.",
	}
	data, err := json.MarshalIndent(modeJSON, "", "  ")
	if err != nil {
		t.Fatalf("marshal mode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "mode.json"), data, 0o600); err != nil {
		t.Fatalf("write legacy mode: %v", err)
	}

	// Ensure cache folders; migration should run automatically.
	if _, err := svc.ensureWorkspaceCacheFolders(workspaceID); err != nil {
		t.Fatalf("ensure workspace cache folders: %v", err)
	}

	// Verify legacy directory no longer exists.
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatal("legacy .echo/modes/ should be gone after migration")
	}

	// Verify scoped directory exists with the mode.
	scopedPath := filepath.Join(folderPath, ".echo", "modes-"+workspaceID)
	children, err := os.ReadDir(scopedPath)
	if err != nil {
		t.Fatalf("read scoped modes dir: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 mode in scoped dir, got %d", len(children))
	}
	if children[0].Name() != modeUUID {
		t.Fatalf("expected mode dir %q, got %q", modeUUID, children[0].Name())
	}

	// Verify the mode file content is intact.
	migratedContent, err := os.ReadFile(filepath.Join(scopedPath, modeUUID, "mode.json"))
	if err != nil {
		t.Fatalf("read migrated mode file: %v", err)
	}
	var loadedMode map[string]any
	if err := json.Unmarshal(migratedContent, &loadedMode); err != nil {
		t.Fatalf("unmarshal migrated mode: %v", err)
	}
	if loadedMode["name"] != "Legacy Mode" {
		t.Fatalf("expected name 'Legacy Mode', got %v", loadedMode["name"])
	}

	// Verify ListAgentModes loads the migrated mode.
	svc.SetActiveWorkspace(workspaceID)
	modes := svc.ListAgentModes(workspaceID)
	var found *AgentMode
	for i, m := range modes {
		if m.ID == modeUUID {
			found = &modes[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("migrated mode %q not found in ListAgentModes", modeUUID)
	}
	if found.Name != "Legacy Mode" {
		t.Fatalf("expected migrated name 'Legacy Mode', got %q", found.Name)
	}
	if found.BuiltIn {
		t.Fatal("migrated mode should not be built-in")
	}
}
