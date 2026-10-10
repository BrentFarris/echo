package agentmodes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestManagerCRUDPersistsWorkspaceModes(t *testing.T) {
	workspace := t.TempDir()
	manager := NewManager()
	modes, err := manager.Create(workspace, Mode{
		Name:   "Reviewer",
		Prompt: "Review changes and report risks.",
		Permissions: map[string]tools.ToolPermission{
			"filesystem_read_text": {Paths: []string{"src/**"}},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(modes) != 5 || modes[4].ID == "" {
		t.Fatalf("unexpected modes after create: %+v", modes)
	}
	createdID := modes[4].ID

	reloaded, err := NewManager().List(workspace)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	permission := reloaded[4].Permissions["filesystem_read_text"]
	if permission.Name != "filesystem_read_text" || len(permission.Paths) != 1 || permission.Paths[0] != "src/**" {
		t.Fatalf("permission did not persist: %+v", permission)
	}

	modes, err = manager.Update(workspace, createdID, Mode{Name: "QA Reviewer", Prompt: "Find regressions."})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if modes[4].Name != "QA Reviewer" {
		t.Fatalf("unexpected updated mode: %+v", modes[3])
	}
	if _, err := os.Stat(filepath.Join(workspace, ".echo", fileName)); err != nil {
		t.Fatalf("expected persisted mode file: %v", err)
	}

	modes, err = manager.Delete(workspace, createdID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(modes) != 4 {
		t.Fatalf("expected only built-ins after delete, got %+v", modes)
	}
}

func TestManagerModesAreWorkspaceScoped(t *testing.T) {
	manager := NewManager()
	first := t.TempDir()
	second := t.TempDir()
	if _, err := manager.Create(first, Mode{Name: "First only", Prompt: "First workspace."}); err != nil {
		t.Fatal(err)
	}
	modes, err := manager.List(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 4 {
		t.Fatalf("mode leaked to another workspace: %+v", modes)
	}
}

func TestResolveUnknownFallsBackToGeneral(t *testing.T) {
	mode, err := NewManager().Resolve(t.TempDir(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if mode.ID != GeneralID || mode.Name != "General" {
		t.Fatalf("unexpected fallback: %+v", mode)
	}
}

func TestManagerAllowsPermissionsOnlyMode(t *testing.T) {
	modes, err := NewManager().Create(t.TempDir(), Mode{
		Name: "Read files",
		Permissions: map[string]tools.ToolPermission{
			"filesystem_read_text": {Name: "filesystem_read_text"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 5 || modes[4].Prompt != "" {
		t.Fatalf("unexpected mode: %+v", modes)
	}
}

func TestGoalModeIsThirdImmutableBuiltIn(t *testing.T) {
	defaults := Defaults()
	if len(defaults) != 4 || defaults[2].ID != GoalID || defaults[2].Name != "Goal" || !defaults[2].BuiltIn {
		t.Fatalf("unexpected built-in modes: %+v", defaults)
	}
	manager := NewManager()
	if _, err := manager.Update(t.TempDir(), GoalID, Mode{Name: "Changed"}); err == nil {
		t.Fatal("expected Goal mode update to be rejected")
	}
	if _, err := manager.Delete(t.TempDir(), GoalID); err == nil {
		t.Fatal("expected Goal mode deletion to be rejected")
	}
}

func TestPairProgrammingIsFourthImmutableBuiltIn(t *testing.T) {
	defaults := Defaults()
	if len(defaults) != 4 || defaults[3].ID != PairProgrammingID || defaults[3].Name != "Pair Programming" || !defaults[3].BuiltIn {
		t.Fatalf("unexpected built-in modes: %+v", defaults)
	}
	if defaults[3].Prompt == "" || !strings.Contains(defaults[3].Prompt, "ask_user_questions") {
		t.Fatalf("Pair Programming prompt must explain the question system: %q", defaults[3].Prompt)
	}
	if len(defaults[3].Permissions) != 0 {
		t.Fatalf("Pair Programming must be an allow-all mode like General: %+v", defaults[3].Permissions)
	}
	manager := NewManager()
	if _, err := manager.Update(t.TempDir(), PairProgrammingID, Mode{Name: "Changed"}); err == nil {
		t.Fatal("expected Pair Programming mode update to be rejected")
	}
	if _, err := manager.Delete(t.TempDir(), PairProgrammingID); err == nil {
		t.Fatal("expected Pair Programming mode deletion to be rejected")
	}
}

func TestDefaultModePersistsAndResets(t *testing.T) {
	workspace := t.TempDir()
	manager := NewManager()

	// Unset defaults resolve to General.
	defaultID, err := manager.Default(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if defaultID != GeneralID {
		t.Fatalf("expected general fallback, got %q", defaultID)
	}

	modes, err := manager.Create(workspace, Mode{Name: "Reviewer", Prompt: "Review changes."})
	if err != nil {
		t.Fatal(err)
	}
	id := modes[4].ID

	// Setting the default persists it.
	defaultID, err = manager.SetDefault(workspace, id)
	if err != nil {
		t.Fatal(err)
	}
	if defaultID != id {
		t.Fatalf("expected %q, got %q", id, defaultID)
	}
	if reloaded, err := NewManager().Default(workspace); err != nil || reloaded != id {
		t.Fatalf("default did not persist: %q %v", reloaded, err)
	}

	// Unknown IDs are rejected.
	if _, err := manager.SetDefault(workspace, "missing"); err == nil {
		t.Fatal("expected unknown mode to be rejected")
	}

	// Explicitly choosing General clears the stored setting.
	if defaultID, err = manager.SetDefault(workspace, GeneralID); err != nil || defaultID != GeneralID {
		t.Fatalf("expected general, got %q %v", defaultID, err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".echo", fileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "defaultModeId") {
		t.Fatalf("expected explicit General to clear the setting: %s", data)
	}

	// Deleting the default mode falls back to General.
	if _, err := manager.SetDefault(workspace, id); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete(workspace, id); err != nil {
		t.Fatal(err)
	}
	if defaultID, err = manager.Default(workspace); err != nil || defaultID != GeneralID {
		t.Fatalf("expected general after delete, got %q %v", defaultID, err)
	}
}

func TestLoadMigratesLegacyBareArrayFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".echo", fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `[{"id":"mode-legacy","name":"Legacy","prompt":"Old format.","builtIn":true}]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	modes, err := manager.List(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 5 || modes[4].ID != "mode-legacy" || modes[4].BuiltIn {
		t.Fatalf("legacy modes not loaded: %+v", modes)
	}
	if defaultID, err := manager.Default(workspace); err != nil || defaultID != GeneralID {
		t.Fatalf("expected general for legacy file, got %q %v", defaultID, err)
	}
}

func TestPlanModeAllowsGitInspection(t *testing.T) {
	plan := Defaults()[1]
	if _, ok := plan.Permissions["git_inspect"]; !ok {
		t.Fatal("Plan mode must expose the read-only git_inspect tool")
	}
	if _, ok := plan.Permissions["fossil_inspect"]; !ok {
		t.Fatal("Plan mode must expose the read-only fossil_inspect tool")
	}
	if _, ok := plan.Permissions["create_agent_mode"]; ok {
		t.Fatal("Plan mode must not expose create_agent_mode")
	}
	for _, name := range []string{
		"research_agents_spawn", "research_agent_send", "research_agents_wait", "research_agents_cancel",
	} {
		if _, ok := plan.Permissions[name]; !ok {
			t.Fatalf("Plan mode must expose %s", name)
		}
	}
}
