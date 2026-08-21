package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOwnedPluginRegistrationFilteringAndDisposal(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(ToolFunc{Meta: Metadata{Name: "core_test", Parameters: Schema{"type": "object"}}, Run: func(ExecutionContext, json.RawMessage) (any, error) { return "core", nil }}); err != nil {
		t.Fatal(err)
	}
	dispose, err := registry.RegisterOwned("sample-plugin", ToolFunc{Meta: Metadata{Name: "sample_plugin_run", Parameters: Schema{"type": "object"}}, Run: func(ExecutionContext, json.RawMessage) (any, error) { return "plugin", nil }})
	if err != nil {
		t.Fatal(err)
	}
	registry.SetPluginPolicy(func(pluginID, toolName, workspaceID string) bool {
		return pluginID == "sample-plugin" && toolName == "sample_plugin_run" && workspaceID == "workspace-1"
	})
	general := registry.ChatLLMSchemaForScopes(nil, ChatSchemaOptions{WorkspaceID: "workspace-1"})
	if len(general) != 2 {
		t.Fatalf("expected core and plugin tools, got %#v", general)
	}
	plan := registry.ChatLLMSchemaForScopes(nil, ChatSchemaOptions{WorkspaceID: "workspace-1", PlanMode: true})
	if len(plan) != 1 || plan[0].Function.Name != "core_test" {
		t.Fatalf("plan exposed plugin tools: %#v", plan)
	}
	if result := registry.Execute(ExecutionContext{Context: context.Background(), WorkspaceID: "workspace-1"}, "sample_plugin_run", json.RawMessage(`{}`)); !result.Success || result.Output != "plugin" {
		t.Fatalf("plugin execution failed: %#v", result)
	}
	if result := registry.Execute(ExecutionContext{Context: context.Background(), WorkspaceID: "other"}, "sample_plugin_run", json.RawMessage(`{}`)); result.Success || result.Error.Code != "tool_not_active" {
		t.Fatalf("activation recheck failed: %#v", result)
	}
	dispose()
	if result := registry.Execute(ExecutionContext{Context: context.Background(), WorkspaceID: "workspace-1"}, "sample_plugin_run", json.RawMessage(`{}`)); result.Error == nil || result.Error.Code != "tool_not_found" {
		t.Fatalf("disposed tool remained: %#v", result)
	}
}

func TestRestrictedModeMustExplicitlyIncludePluginTool(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.RegisterOwned("sample-plugin", ToolFunc{Meta: Metadata{Name: "sample_plugin_run", Parameters: Schema{"type": "object"}}, Run: func(ExecutionContext, json.RawMessage) (any, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	registry.SetPluginPolicy(func(string, string, string) bool { return true })
	denied := NewToolScopeChecker([]ToolPermission{{Name: "some_other_tool"}})
	if schema := registry.ChatLLMSchemaForScopes(denied, ChatSchemaOptions{}); len(schema) != 0 {
		t.Fatalf("restricted mode exposed plugin: %#v", schema)
	}
	allowed := NewToolScopeChecker([]ToolPermission{{Name: "sample_plugin_run"}})
	if schema := registry.ChatLLMSchemaForScopes(allowed, ChatSchemaOptions{}); len(schema) != 1 {
		t.Fatalf("explicit plugin permission was ignored: %#v", schema)
	}
	if schema := registry.ResearchLLMSchemaForScopes(allowed); len(schema) != 0 {
		t.Fatalf("research worker exposed plugin: %#v", schema)
	}
}
