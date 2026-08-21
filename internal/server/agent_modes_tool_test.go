package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/tools"
)

func TestAgentModeToolProviderPersistsPerToolPermissions(t *testing.T) {
	workspace := t.TempDir()
	provider := agentModeToolProvider{manager: agentmodes.NewManager(), workspacePath: workspace}
	created, err := provider.CreateAgentMode(context.Background(), tools.AgentModeCreationRequest{
		Name:   "Reviewer",
		Prompt: "Review changes.",
		Permissions: map[string][]string{
			"filesystem_read_text": {"src/**"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	permission := created.Permissions["filesystem_read_text"]
	if created.ID == "" || permission.Name != "filesystem_read_text" || len(permission.Paths) != 1 || permission.Paths[0] != "src/**" {
		t.Fatalf("unexpected created mode: %+v", created)
	}
	reloaded, err := agentmodes.NewManager().List(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 3 || reloaded[2].ID != created.ID {
		t.Fatalf("mode did not persist: %+v", reloaded)
	}
}

func TestAgentModeToolProviderConvertsLegacyPermissions(t *testing.T) {
	provider := agentModeToolProvider{manager: agentmodes.NewManager(), workspacePath: t.TempDir()}
	created, err := provider.CreateAgentMode(context.Background(), tools.AgentModeCreationRequest{
		Name: "Legacy", ToolPermissions: []string{"filesystem_read_text", "filesystem_list"}, PathPermissions: []string{"src/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Prompt != "" || len(created.Permissions) != 2 || created.Permissions["filesystem_list"].Paths[0] != "src/**" {
		t.Fatalf("legacy permissions were not converted: %+v", created)
	}
}

func TestChatToolContextCreatesWorkspaceAgentMode(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "mode-tool-context")
	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab, _, err := parent.resolveTab("")
	if err != nil {
		t.Fatal(err)
	}
	result := tools.Execute(tab.toolContext(context.Background(), tools.NewToolScopeChecker(nil), nil, nil), "create_agent_mode", json.RawMessage(`{
		"name":"Tool-created reviewer",
		"prompt":"Review changes.",
		"permissions":{"filesystem_read_text":[]}
	}`))
	if !result.Success {
		t.Fatalf("create_agent_mode failed through chat context: %+v", result.Error)
	}
	modes, err := server.modes.List(workspace.MainPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) != 3 || modes[2].Name != "Tool-created reviewer" {
		t.Fatalf("tool-created mode did not persist: %+v", modes)
	}
}
