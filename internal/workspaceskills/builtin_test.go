package workspaceskills

import (
	"context"
	"strings"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestBuiltinPluginSkillIsSearchableAndReadOnly(t *testing.T) {
	service := New(nil)
	result, err := service.SearchWorkspaceSkills(context.Background(), tools.WorkspaceSkillSearchRequest{Query: "build an Echo plugin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) == 0 || result.Skills[0].ID != "builtin/echo-plugins" {
		t.Fatalf("built-in plugin skill not found: %#v", result)
	}
	skill, err := service.ReadWorkspaceSkill(context.Background(), tools.WorkspaceSkillReadRequest{ID: "builtin/echo-plugins"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(skill.Body, "echo_plugin_stage") || skill.Folder != "builtin" {
		t.Fatalf("unexpected built-in skill: %#v", skill)
	}
	_, err = service.RecordWorkspaceSkill(context.Background(), tools.WorkspaceSkillRecordRequest{Action: "upsert", Folder: "builtin", Name: "echo-plugins", Description: "replace", Body: "replace"})
	if err == nil {
		t.Fatal("built-in skill was writable")
	}
}

func TestWorkspaceNamedBuiltinCannotShadowPluginAuthoringSkill(t *testing.T) {
	root := t.TempDir()
	service := New([]tools.WorkspaceRoot{{ID: "root-1", Label: "builtin", Path: root}})
	_, err := service.Upsert(context.Background(), tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "builtin", Name: "echo-plugins", Description: "shadow", Body: "shadow",
	})
	if err == nil || !strings.Contains(err.Error(), "read-only built-in") {
		t.Fatalf("expected the built-in namespace to remain read-only, got %v", err)
	}
	skill, err := service.ReadWorkspaceSkill(context.Background(), tools.WorkspaceSkillReadRequest{ID: "builtin/echo-plugins"})
	if err != nil || !strings.Contains(skill.Body, "echo_plugin_stage") {
		t.Fatalf("built-in skill was shadowed: %#v, %v", skill, err)
	}
}
