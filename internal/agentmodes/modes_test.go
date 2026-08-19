package agentmodes

import (
	"os"
	"path/filepath"
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
	if len(modes) != 3 || modes[2].ID == "" {
		t.Fatalf("unexpected modes after create: %+v", modes)
	}
	createdID := modes[2].ID

	reloaded, err := NewManager().List(workspace)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	permission := reloaded[2].Permissions["filesystem_read_text"]
	if permission.Name != "filesystem_read_text" || len(permission.Paths) != 1 || permission.Paths[0] != "src/**" {
		t.Fatalf("permission did not persist: %+v", permission)
	}

	modes, err = manager.Update(workspace, createdID, Mode{Name: "QA Reviewer", Prompt: "Find regressions."})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if modes[2].Name != "QA Reviewer" {
		t.Fatalf("unexpected updated mode: %+v", modes[2])
	}
	if _, err := os.Stat(filepath.Join(workspace, ".echo", fileName)); err != nil {
		t.Fatalf("expected persisted mode file: %v", err)
	}

	modes, err = manager.Delete(workspace, createdID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(modes) != 2 {
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
	if len(modes) != 2 {
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
	if len(modes) != 3 || modes[2].Prompt != "" {
		t.Fatalf("unexpected mode: %+v", modes)
	}
}

func TestPlanModeAllowsGitInspection(t *testing.T) {
	plan := Defaults()[1]
	if _, ok := plan.Permissions["git_inspect"]; !ok {
		t.Fatal("Plan mode must expose the read-only git_inspect tool")
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
