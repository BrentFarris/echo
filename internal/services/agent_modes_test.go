package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAgentModesReturnsGeneralAndPlan(t *testing.T) {
	modes := DefaultAgentModes()
	if len(modes) != 2 {
		t.Fatalf("expected 2 default modes, got %d", len(modes))
	}

	general := modes[0]
	if general.ID != AgentModeIDGeneral || general.Name != "General" || !general.BuiltIn {
		t.Fatalf("unexpected general mode: %#v", general)
	}
	if general.ToolPermissions != nil || general.PathPermissions != nil {
		t.Fatalf("expected nil permissions on general mode, got %#v", general)
	}

	plan := modes[1]
	if plan.ID != AgentModeIDPlan || plan.Name != "Plan" || !plan.BuiltIn {
		t.Fatalf("unexpected plan mode: %#v", plan)
	}
}

func TestListAgentModesReturnsClone(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	modes := service.ListAgentModes("")
	if len(modes) != 2 {
		t.Fatalf("expected 2 default modes, got %d", len(modes))
	}

	// Mutate the returned slice and verify internal state is unchanged.
	modes[0].Name = "MUTATED"
	modes = append(modes, AgentMode{ID: "fake"})
	actual := service.ListAgentModes("")
	if actual[0].Name != "General" {
		t.Fatalf("expected unmutated General, got %q", actual[0].Name)
	}
	if len(actual) != 2 {
		t.Fatalf("expected 2 modes after mutation, got %d", len(actual))
	}
}

func TestCreateAgentModeRejectsEmptyName(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	_, err := service.CreateAgentMode("", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected name required error, got %v", err)
	}

	_, err = service.CreateAgentMode("  ", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected whitespace-only name to fail, got %v", err)
	}
}

func newAgentModeTestService(t *testing.T) (*SystemService, string) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	svc := NewSystemServiceWithStorePath(storePath)
	if _, err := svc.AddWorkspace(root); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return svc, root
}

func TestCreateAgentModeRejectsDuplicateName(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, err := svc.CreateAgentMode("Research", "Explore the codebase.", []string{"filesystem_read_text"}, nil)
	if err != nil {
		t.Fatalf("create mode: %v", err)
	}

	_, err = svc.CreateAgentMode("research", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate name error (case-insensitive), got %v", err)
	}

	// Verify original mode is still present.
	for _, m := range modes {
		if strings.EqualFold(m.Name, "Research") && !m.BuiltIn {
			return
		}
	}
	t.Fatal("expected Research mode to exist after duplicate rejection")
}

func TestCreateAgentModeSucceedsWithPermissions(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, err := svc.CreateAgentMode(
		"Read Only",
		"Inspection mode.",
		[]string{"filesystem_read_text", "filesystem_list"},
		[]string{"src/**"},
	)
	if err != nil {
		t.Fatalf("create mode: %v", err)
	}

	var custom *AgentMode
	for i, m := range modes {
		if m.Name == "Read Only" {
			custom = &modes[i]
			break
		}
	}
	if custom == nil {
		t.Fatal("expected created mode in list")
	}
	if custom.BuiltIn {
		t.Fatal("expected custom mode to not be built-in")
	}
	if len(custom.ID) == 0 {
		t.Fatal("expected non-empty ID")
	}
	if len(custom.ToolPermissions) != 2 {
		t.Fatalf("expected 2 tool permissions, got %d", len(custom.ToolPermissions))
	}
	// ToolPermissions are sorted; verify both are present.
	hasRead, hasList := false, false
	for _, tp := range custom.ToolPermissions {
		if tp == "filesystem_read_text" {
			hasRead = true
		}
		if tp == "filesystem_list" {
			hasList = true
		}
	}
	if !hasRead || !hasList {
		t.Fatalf("unexpected tool permissions: %#v", custom.ToolPermissions)
	}
	if len(custom.PathPermissions) != 1 || custom.PathPermissions[0] != "src/**" {
		t.Fatalf("unexpected path permissions: %#v", custom.PathPermissions)
	}
}

func TestCreateAgentModeDeepCopiesSlices(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	tools := []string{"filesystem_read_text"}
	paths := []string{"src/**"}
	modes, err := svc.CreateAgentMode("Test", "", tools, paths)
	if err != nil {
		t.Fatalf("create mode: %v", err)
	}

	// Mutate input slices after creation.
	tools[0] = "MUTATED"
	paths[0] = "MUTATED"

	for _, m := range modes {
		if m.Name == "Test" {
			for _, tp := range m.ToolPermissions {
				if tp == "MUTATED" {
					t.Fatal("expected deep copy of tool permissions")
				}
			}
			for _, pp := range m.PathPermissions {
				if pp == "MUTATED" {
					t.Fatal("expected deep copy of path permissions")
				}
			}
			return
		}
	}
	t.Fatal("Test mode not found")
}

func TestUpdateAgentModeRejectsEmptyID(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.UpdateAgentMode("", "New Name", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("expected id required error, got %v", err)
	}
}

func TestUpdateAgentModeRejectsEmptyName(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, _ := svc.CreateAgentMode("Original", "", nil, nil)
	var custom AgentMode
	for _, m := range modes {
		if !m.BuiltIn {
			custom = m
			break
		}
	}
	_, err := svc.UpdateAgentMode(custom.ID, "", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected name required error, got %v", err)
	}
}

func TestUpdateAgentModeProtectsBuiltIn(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.UpdateAgentMode(AgentModeIDGeneral, "Hacked", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("expected built-in protection error, got %v", err)
	}
}

func TestUpdateAgentModeRejectsNotFound(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.UpdateAgentMode("nonexistent", "Name", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestUpdateAgentModeSucceeds(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, _ := svc.CreateAgentMode("Original", "", nil, nil)
	var custom AgentMode
	for _, m := range modes {
		if !m.BuiltIn {
			custom = m
			break
		}
	}

	modes, err := svc.UpdateAgentMode(custom.ID, "Updated", "New prompt", []string{"filesystem_list"}, []string{"docs/**"})
	if err != nil {
		t.Fatalf("update mode: %v", err)
	}

	for _, m := range modes {
		if m.ID == custom.ID {
			if m.Name != "Updated" {
				t.Fatalf("expected updated name, got %q", m.Name)
			}
			if m.Prompt != "New prompt" {
				t.Fatalf("expected updated prompt, got %q", m.Prompt)
			}
			if len(m.ToolPermissions) != 1 || m.ToolPermissions[0] != "filesystem_list" {
				t.Fatalf("unexpected tool permissions: %#v", m.ToolPermissions)
			}
			if len(m.PathPermissions) != 1 || m.PathPermissions[0] != "docs/**" {
				t.Fatalf("unexpected path permissions: %#v", m.PathPermissions)
			}
			return
		}
	}
	t.Fatal("updated mode not found")
}

func TestUpdateAgentModeRejectsDuplicateName(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, _ := svc.CreateAgentMode("Alpha", "", nil, nil)
	var beta AgentMode
	for _, m := range modes {
		if !m.BuiltIn {
			beta = m
			break
		}
	}

	_, err := svc.UpdateAgentMode(beta.ID, "General", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate name error (built-in collision), got %v", err)
	}
}

func TestDeleteAgentModeRejectsEmptyID(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.DeleteAgentMode("")
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("expected id required error, got %v", err)
	}
}

func TestDeleteAgentModeProtectsBuiltIn(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.DeleteAgentMode(AgentModeIDGeneral)
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("expected built-in protection error, got %v", err)
	}

	_, err = svc.DeleteAgentMode(AgentModeIDPlan)
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("expected built-in protection error for plan mode, got %v", err)
	}
}

func TestDeleteAgentModeRejectsNotFound(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	_, err := svc.DeleteAgentMode("nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestDeleteAgentModeSucceeds(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, _ := svc.CreateAgentMode("Temp", "", nil, nil)
	var custom AgentMode
	for _, m := range modes {
		if !m.BuiltIn {
			custom = m
			break
		}
	}

	modes, err := svc.DeleteAgentMode(custom.ID)
	if err != nil {
		t.Fatalf("delete mode: %v", err)
	}

	for _, m := range modes {
		if m.ID == custom.ID {
			t.Fatal("expected deleted mode to be removed")
		}
	}
	// Built-in modes should still be present.
	foundGeneral, foundPlan := false, false
	for _, m := range modes {
		if m.ID == AgentModeIDGeneral {
			foundGeneral = true
		}
		if m.ID == AgentModeIDPlan {
			foundPlan = true
		}
	}
	if !foundGeneral || !foundPlan {
		t.Fatalf("expected built-in modes to remain, got general=%v plan=%v", foundGeneral, foundPlan)
	}
}

func TestAgentModesPersistAndRestore(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	workspaceRoot := t.TempDir()

	service := NewSystemServiceWithStorePath(storePath)
	if _, err := service.AddWorkspace(workspaceRoot); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	_, _ = service.CreateAgentMode("Research", "Read and analyze.", []string{"filesystem_read_text"}, []string{"**/*.go"})

	// Verify persistence by reloading.
	reloaded := NewSystemServiceWithStorePath(storePath)
	reloaded.LoadState()
	listed := reloaded.ListAgentModes("")

	var found *AgentMode
	for i, m := range listed {
		if strings.EqualFold(m.Name, "Research") {
			found = &listed[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected Research mode to persist")
	}
	if found.BuiltIn {
		t.Fatal("expected persisted custom mode to not be built-in")
	}
	if len(found.ToolPermissions) != 1 || found.ToolPermissions[0] != "filesystem_read_text" {
		t.Fatalf("unexpected persisted tool permissions: %#v", found.ToolPermissions)
	}
	if len(found.PathPermissions) != 1 || found.PathPermissions[0] != "**/*.go" {
		t.Fatalf("unexpected persisted path permissions: %#v", found.PathPermissions)
	}

	// Verify built-in modes are also present.
	foundGeneral, foundPlan := false, false
	for _, m := range listed {
		if m.ID == AgentModeIDGeneral {
			foundGeneral = true
		}
		if m.ID == AgentModeIDPlan {
			foundPlan = true
		}
	}
	if !foundGeneral || !foundPlan {
		t.Fatal("expected built-in modes to persist")
	}

	// Verify the persisted JSON no longer contains agentModes (they are on disk now).
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode state JSON: %v", err)
	}
	if _, ok := stored["agentModes"]; ok {
		t.Fatal("expected agentModes key to NOT be in persisted state (modes are workspace-specific)")
	}

	// Verify mode exists on disk.
	var workspaceID string
	for _, w := range reloaded.state.Workspaces {
		if w.ID == reloaded.state.ActiveWorkspaceID {
			workspaceID = w.ID
			break
		}
	}
	modesDir := filepath.Join(workspaceRoot, ".echo", workspaceModeDirName(workspaceID))
	children, err := os.ReadDir(modesDir)
	if err != nil {
		t.Fatalf("read modes directory: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 mode on disk, got %d", len(children))
	}
	modeFile := filepath.Join(modesDir, children[0].Name(), "mode.json")
	_, err = os.Stat(modeFile)
	if err != nil {
		t.Fatalf("mode file not found on disk: %v", err)
	}
}

func TestResolveAgentModeReturnsGeneralForEmptyID(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	mode, id := service.resolveAgentMode("")
	if mode.ID != AgentModeIDGeneral || id != AgentModeIDGeneral {
		t.Fatalf("expected general fallback for empty ID, got %s / %s", mode.ID, id)
	}

	mode2, id2 := service.resolveAgentMode("  ")
	if mode2.ID != AgentModeIDGeneral || id2 != AgentModeIDGeneral {
		t.Fatalf("expected general fallback for whitespace ID, got %s / %s", mode2.ID, id2)
	}
}

func TestResolveAgentModeFindsExisting(t *testing.T) {
	svc, _ := newAgentModeTestService(t)
	modes, _ := svc.CreateAgentMode("Research", "", nil, nil)
	var custom AgentMode
	for _, m := range modes {
		if !m.BuiltIn {
			custom = m
			break
		}
	}

	mode, id := svc.resolveAgentMode(custom.ID)
	if mode.ID != custom.ID || id != custom.ID {
		t.Fatalf("expected resolved custom mode, got %s / %s", mode.ID, id)
	}
}

func TestResolveAgentModeFallsBackToGeneralForUnknown(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	mode, id := service.resolveAgentMode("nonexistent-id")
	if mode.ID != AgentModeIDGeneral || id != AgentModeIDGeneral {
		t.Fatalf("expected general fallback for unknown ID, got %s / %s", mode.ID, id)
	}
}

func TestCloneAgentModesHandlesNil(t *testing.T) {
	result := cloneAgentModes(nil)
	if result == nil {
		t.Fatal("expected non-nil slice from nil input")
	}
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %d", len(result))
	}
}

func TestCloneAgentModesDeepCopies(t *testing.T) {
	src := []AgentMode{
		{ID: "1", Name: "Test", ToolPermissions: []string{"tool-a"}, PathPermissions: []string{"src/**"}},
	}
	dst := cloneAgentModes(src)
	if len(dst) != 1 || dst[0].ID != "1" {
		t.Fatalf("unexpected clone result: %#v", dst)
	}

	src[0].Name = "MUTATED"
	src[0].ToolPermissions[0] = "MUTATED"
	src[0].PathPermissions[0] = "MUTATED"

	if dst[0].Name == "MUTATED" {
		t.Fatal("expected deep copy of name")
	}
	for _, tp := range dst[0].ToolPermissions {
		if tp == "MUTATED" {
			t.Fatal("expected deep copy of tool permissions")
		}
	}
	for _, pp := range dst[0].PathPermissions {
		if pp == "MUTATED" {
			t.Fatal("expected deep copy of path permissions")
		}
	}
}

func TestAgentModeNameExistsIsCaseInsensitive(t *testing.T) {
	modes := []AgentMode{{Name: "Research"}, {Name: "plan"}}
	if !agentModeNameExists(modes, "research") {
		t.Fatal("expected case-insensitive match for research")
	}
	if !agentModeNameExists(modes, "PLAN") {
		t.Fatal("expected case-insensitive match for PLAN")
	}
	if agentModeNameExists(modes, "unknown") {
		t.Fatal("expected no match for unknown")
	}
}

func TestCloneStringsHandlesNil(t *testing.T) {
	result := cloneStrings(nil)
	if result != nil {
		t.Fatalf("expected nil from nil input, got %#v", result)
	}
	src := []string{"a", "b"}
	dst := cloneStrings(src)
	src[0] = "MUTATED"
	if dst[0] == "MUTATED" {
		t.Fatal("expected deep copy")
	}
}
