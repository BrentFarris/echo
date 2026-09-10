package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/tools"
)

type promptSourceControlProvider struct {
	id           string
	repositories []sourcecontrol.Repository
}

func (p promptSourceControlProvider) Descriptor(context.Context, string) sourcecontrol.ProviderDescriptor {
	return sourcecontrol.ProviderDescriptor{ID: p.id, Label: p.id, Available: true}
}

func (p promptSourceControlProvider) Repositories(context.Context, string) ([]sourcecontrol.Repository, error) {
	return append([]sourcecontrol.Repository(nil), p.repositories...), nil
}

func TestChatSelectedAgentModeControlsPromptAndTools(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "mode-chat")
	modes, err := s.modes.Create(workspace.MainPath, agentmodes.Mode{
		Name:   "Reader",
		Prompt: "Read the code carefully and never speculate.",
		Permissions: map[string]tools.ToolPermission{
			"filesystem_read_text": {Name: "filesystem_read_text", Paths: []string{"src/**"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mode := modes[len(modes)-1]
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "mode-request",
		"message": "inspect this", "agentModeId": mode.ID,
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if len(request.Messages) == 0 || request.Messages[0].Role != llm.RoleSystem ||
		!strings.Contains(request.Messages[0].Content, mode.Prompt) ||
		!strings.Contains(request.Messages[0].Content, "mentions @path") {
		t.Fatalf("selected mode prompt missing from request: %+v", request.Messages)
	}
	if len(request.Tools) != 1 || request.Tools[0].Function.Name != "filesystem_read_text" {
		t.Fatalf("selected mode did not filter tools: %+v", request.Tools)
	}

	parent, err := s.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	session := parent.tabs[parent.activeChatID]
	parent.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.transcript.Turns) != 1 || session.transcript.Turns[0].AgentModeID != mode.ID || session.transcript.Turns[0].AgentModeName != mode.Name {
		t.Fatalf("mode metadata not persisted on turn: %+v", session.transcript.Turns)
	}
	for _, message := range session.transcript.Messages {
		if message.Name == "echo-agent-mode" {
			t.Fatal("ephemeral agent mode system prompt leaked into durable history")
		}
	}
}

func TestAgentModeSystemMessageIdentifiesFossilWorkspace(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "fossil-chat")
	s.sourceControl = sourcecontrol.New()
	if err := s.sourceControl.Register(promptSourceControlProvider{
		id: "fossil",
		repositories: []sourcecontrol.Repository{{
			ID: "fossil-repository", ProviderID: "fossil", ProviderLabel: "Fossil", Label: "project", Available: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	message := s.agentModeSystemMessage(workspace, agentmodes.Defaults()[0], "inspect history", false)
	if !strings.Contains(message.Content, "this is a Fossil repository workspace, not a Git repository workspace") {
		t.Fatalf("Fossil workspace identity missing from system message: %q", message.Content)
	}
	if !strings.Contains(message.Content, "Use fossil_inspect") || !strings.Contains(message.Content, "Do not use git_inspect") {
		t.Fatalf("Fossil inspection guidance missing from system message: %q", message.Content)
	}
}

func TestAgentModeSystemMessageDisambiguatesMixedSourceControlWorkspace(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "mixed-chat")
	s.sourceControl = sourcecontrol.New()
	for _, provider := range []promptSourceControlProvider{
		{id: "git", repositories: []sourcecontrol.Repository{{ID: "git-repository", ProviderID: "git", Label: "git-project", Available: true}}},
		{id: "fossil", repositories: []sourcecontrol.Repository{{ID: "fossil-repository", ProviderID: "fossil", Label: "fossil-project", Available: true}}},
	} {
		if err := s.sourceControl.Register(provider); err != nil {
			t.Fatal(err)
		}
	}

	message := s.agentModeSystemMessage(workspace, agentmodes.Defaults()[0], "inspect history", false)
	if !strings.Contains(message.Content, "contains both Fossil and Git repositories") ||
		!strings.Contains(message.Content, "Use git_inspect only for the Git repositories") {
		t.Fatalf("mixed-provider guidance missing from system message: %q", message.Content)
	}
}

func TestAgentModeContextFiltersIncompatibleSourceControlTools(t *testing.T) {
	tests := []struct {
		name           string
		providers      []promptSourceControlProvider
		wantGit        bool
		wantFossil     bool
		wantPromptText string
	}{
		{
			name: "Fossil only",
			providers: []promptSourceControlProvider{{id: "fossil", repositories: []sourcecontrol.Repository{
				{ID: "fossil-repository", ProviderID: "fossil", Label: "project", Available: true},
			}}},
			wantFossil: true, wantPromptText: "this is a Fossil repository workspace",
		},
		{
			name: "Git only",
			providers: []promptSourceControlProvider{{id: "git", repositories: []sourcecontrol.Repository{
				{ID: "git-repository", ProviderID: "git", Label: "project", Available: true},
			}}},
			wantGit: true, wantPromptText: "this is a Git repository workspace",
		},
		{
			name: "mixed",
			providers: []promptSourceControlProvider{
				{id: "git", repositories: []sourcecontrol.Repository{{ID: "git-repository", ProviderID: "git", Label: "git-project", Available: true}}},
				{id: "fossil", repositories: []sourcecontrol.Repository{{ID: "fossil-repository", ProviderID: "fossil", Label: "fossil-project", Available: true}}},
			},
			wantGit: true, wantFossil: true, wantPromptText: "contains both Fossil and Git repositories",
		},
		{name: "unknown", wantGit: true, wantFossil: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			workspace := createChatWorkspace(t, s, "provider-filter")
			s.sourceControl = sourcecontrol.New()
			for _, provider := range test.providers {
				if err := s.sourceControl.Register(provider); err != nil {
					t.Fatal(err)
				}
			}

			message, scopes, _ := s.agentModeContext(workspace, agentmodes.Defaults()[0], "inspect history", false)
			if scopes.HasTool("git_inspect") != test.wantGit || scopes.HasTool(tools.FossilInspectToolName) != test.wantFossil {
				t.Fatalf("source control tool scopes: git=%v fossil=%v, want git=%v fossil=%v", scopes.HasTool("git_inspect"), scopes.HasTool(tools.FossilInspectToolName), test.wantGit, test.wantFossil)
			}
			if test.wantPromptText != "" && !strings.Contains(message.Content, test.wantPromptText) {
				t.Fatalf("source control identity missing from system message: %q", message.Content)
			}

			schema := s.tools.ChatLLMSchemaForScopes(scopes, tools.ChatSchemaOptions{WorkspaceID: workspace.ID})
			if schemaContainsTool(schema, "git_inspect") != test.wantGit || schemaContainsTool(schema, tools.FossilInspectToolName) != test.wantFossil {
				t.Fatalf("source control tool schema: git=%v fossil=%v, want git=%v fossil=%v", schemaContainsTool(schema, "git_inspect"), schemaContainsTool(schema, tools.FossilInspectToolName), test.wantGit, test.wantFossil)
			}
			for name, allowed := range map[string]bool{"git_inspect": test.wantGit, tools.FossilInspectToolName: test.wantFossil} {
				if allowed {
					continue
				}
				result := s.tools.Execute(tools.ExecutionContext{ToolScopes: scopes}, name, json.RawMessage(`{}`))
				if result.Success || result.Error == nil || result.Error.Code != "tool_not_allowed" {
					t.Fatalf("excluded %s execution was not denied: %+v", name, result)
				}
			}
		})
	}
}

func TestResearchToolScopesFilterIncompatibleSourceControlTools(t *testing.T) {
	general := agentmodes.Defaults()[0]
	tests := []struct {
		name       string
		profile    workspaceSourceControlProfile
		wantGit    bool
		wantFossil bool
	}{
		{name: "Fossil only", profile: workspaceSourceControlProfile{hasFossil: true}, wantFossil: true},
		{name: "Git only", profile: workspaceSourceControlProfile{hasGit: true}, wantGit: true},
		{name: "mixed", profile: workspaceSourceControlProfile{hasGit: true, hasFossil: true}, wantGit: true, wantFossil: true},
		{name: "unknown", wantGit: true, wantFossil: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scopes := researchToolScopes(general)
			test.profile.restrictToolScopes(scopes)
			if scopes.HasTool("git_inspect") != test.wantGit || scopes.HasTool(tools.FossilInspectToolName) != test.wantFossil {
				t.Fatalf("research source control scopes: git=%v fossil=%v, want git=%v fossil=%v", scopes.HasTool("git_inspect"), scopes.HasTool(tools.FossilInspectToolName), test.wantGit, test.wantFossil)
			}
		})
	}
}

func schemaContainsTool(schema []llm.Tool, name string) bool {
	for _, tool := range schema {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
