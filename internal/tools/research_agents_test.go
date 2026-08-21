package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

type recordingResearchCoordinator struct {
	spawned  []ResearchAgentSpec
	sentID   string
	sent     string
	waitFor  string
	timeout  time.Duration
	canceled []string
}

func (r *recordingResearchCoordinator) SpawnResearchAgents(_ context.Context, specs []ResearchAgentSpec) ([]ResearchAgentSnapshot, error) {
	r.spawned = append([]ResearchAgentSpec(nil), specs...)
	return []ResearchAgentSnapshot{{ID: "agent-1", Name: specs[0].Name, Status: "queued"}}, nil
}

func (r *recordingResearchCoordinator) SendResearchAgentMessage(_ context.Context, id, message string) (ResearchAgentSnapshot, error) {
	r.sentID, r.sent = id, message
	return ResearchAgentSnapshot{ID: id, Status: "queued"}, nil
}

func (r *recordingResearchCoordinator) WaitResearchAgents(_ context.Context, _ []string, waitFor string, timeout time.Duration) (ResearchAgentWaitResult, error) {
	r.waitFor, r.timeout = waitFor, timeout
	return ResearchAgentWaitResult{ConditionMet: true}, nil
}

func (r *recordingResearchCoordinator) CancelResearchAgents(_ context.Context, ids []string) ([]ResearchAgentSnapshot, error) {
	r.canceled = append([]string(nil), ids...)
	return []ResearchAgentSnapshot{}, nil
}

func TestResearchToolsForwardValidatedArguments(t *testing.T) {
	coordinator := &recordingResearchCoordinator{}
	ctx := ExecutionContext{ResearchAgents: coordinator}

	spawn := Execute(ctx, ResearchAgentsSpawnToolName, json.RawMessage(`{"agents":[{"name":" Docs ","task":" Check the docs "}]}`))
	if !spawn.Success || len(coordinator.spawned) != 1 || coordinator.spawned[0].Name != "Docs" || coordinator.spawned[0].Task != "Check the docs" {
		t.Fatalf("unexpected spawn result: %#v %#v", spawn, coordinator.spawned)
	}
	send := Execute(ctx, ResearchAgentSendToolName, json.RawMessage(`{"agentId":"agent-1","message":"Verify it"}`))
	if !send.Success || coordinator.sentID != "agent-1" || coordinator.sent != "Verify it" {
		t.Fatalf("unexpected send result: %#v", send)
	}
	wait := Execute(ctx, ResearchAgentsWaitToolName, json.RawMessage(`{"waitFor":"any","timeoutSeconds":7}`))
	if !wait.Success || coordinator.waitFor != "any" || coordinator.timeout != 7*time.Second {
		t.Fatalf("unexpected wait result: %#v", wait)
	}
	cancel := Execute(ctx, ResearchAgentsCancelToolName, json.RawMessage(`{"agentIds":["agent-1"]}`))
	if !cancel.Success || len(coordinator.canceled) != 1 || coordinator.canceled[0] != "agent-1" {
		t.Fatalf("unexpected cancel result: %#v", cancel)
	}
}

func TestResearchToolSchemasAreScopedByRole(t *testing.T) {
	parentScopes := NewToolScopeChecker([]ToolPermission{
		{Name: ResearchAgentsSpawnToolName}, {Name: ResearchAgentSendToolName},
		{Name: ResearchAgentsWaitToolName}, {Name: ResearchAgentsCancelToolName},
		{Name: "filesystem_read_text"}, {Name: "filesystem_edit_text"},
	})
	for _, schema := range ChatLLMSchemaForScopes(parentScopes, ChatSchemaOptions{}) {
		if IsResearchAgentToolName(schema.Function.Name) {
			t.Fatalf("disabled parent schema exposed %s", schema.Function.Name)
		}
	}
	parentNames := schemaNames(ChatLLMSchemaForScopes(parentScopes, ChatSchemaOptions{ResearchEnabled: true}))
	for _, name := range []string{ResearchAgentsSpawnToolName, ResearchAgentSendToolName, ResearchAgentsWaitToolName, ResearchAgentsCancelToolName} {
		if !parentNames[name] {
			t.Fatalf("enabled parent schema omitted %s", name)
		}
	}

	workerNames := schemaNames(ResearchLLMSchemaForScopes(parentScopes))
	if !workerNames["filesystem_read_text"] {
		t.Fatal("worker schema omitted an allowed read tool")
	}
	if workerNames["filesystem_edit_text"] {
		t.Fatal("worker schema exposed a mutating tool")
	}
	for name := range workerNames {
		if IsResearchAgentToolName(name) {
			t.Fatalf("worker schema exposed recursive orchestration tool %s", name)
		}
	}
}

func TestResearchToolsFailSafelyWithoutCoordinator(t *testing.T) {
	result := Execute(ExecutionContext{}, ResearchAgentsSpawnToolName, json.RawMessage(`{"agents":[{"task":"inspect"}]}`))
	if result.Success || result.Error == nil || result.Error.Code != "research_agents_unavailable" {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}

func schemaNames(schema []llm.Tool) map[string]bool {
	names := make(map[string]bool, len(schema))
	for _, tool := range schema {
		names[tool.Function.Name] = true
	}
	return names
}
