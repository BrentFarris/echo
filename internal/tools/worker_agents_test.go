package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type recordingWorkerCoordinator struct {
	spawned  []WorkerAgentSpec
	sentID   string
	sent     string
	waitFor  string
	timeout  time.Duration
	canceled []string
}

func (r *recordingWorkerCoordinator) SpawnWorkerAgents(_ context.Context, specs []WorkerAgentSpec) ([]ResearchAgentSnapshot, error) {
	r.spawned = append([]WorkerAgentSpec(nil), specs...)
	return []ResearchAgentSnapshot{{ID: "agent-1", Name: specs[0].Name, Status: "queued"}}, nil
}

func (r *recordingWorkerCoordinator) SendWorkerAgentMessage(_ context.Context, id, message string) (ResearchAgentSnapshot, error) {
	r.sentID, r.sent = id, message
	return ResearchAgentSnapshot{ID: id, Status: "queued"}, nil
}

func (r *recordingWorkerCoordinator) WaitWorkerAgents(_ context.Context, _ []string, waitFor string, timeout time.Duration) (ResearchAgentWaitResult, error) {
	r.waitFor, r.timeout = waitFor, timeout
	return ResearchAgentWaitResult{ConditionMet: true}, nil
}

func (r *recordingWorkerCoordinator) CancelWorkerAgents(_ context.Context, ids []string) ([]ResearchAgentSnapshot, error) {
	r.canceled = append([]string(nil), ids...)
	return []ResearchAgentSnapshot{}, nil
}

func TestWorkerToolsForwardValidatedArguments(t *testing.T) {
	coordinator := &recordingWorkerCoordinator{}
	ctx := ExecutionContext{WorkerAgents: coordinator}

	spawn := Execute(ctx, WorkerAgentsSpawnToolName, json.RawMessage(`{"workers":[{"name":" Docs ","task":" Fix the tests "}]}`))
	if !spawn.Success || len(coordinator.spawned) != 1 || coordinator.spawned[0].Name != "Docs" || coordinator.spawned[0].Task != "Fix the tests" {
		t.Fatalf("unexpected spawn result: %#v %#v", spawn, coordinator.spawned)
	}
	send := Execute(ctx, WorkerAgentSendToolName, json.RawMessage(`{"workerId":"agent-1","message":"Also update the docs"}`))
	if !send.Success || coordinator.sentID != "agent-1" || coordinator.sent != "Also update the docs" {
		t.Fatalf("unexpected send result: %#v", send)
	}
	wait := Execute(ctx, WorkerAgentsWaitToolName, json.RawMessage(`{"waitFor":"any","timeoutSeconds":7}`))
	if !wait.Success || coordinator.waitFor != "any" || coordinator.timeout != 7*time.Second {
		t.Fatalf("unexpected wait result: %#v", wait)
	}
	cancel := Execute(ctx, WorkerAgentsCancelToolName, json.RawMessage(`{"workerIds":["agent-1"]}`))
	if !cancel.Success || len(coordinator.canceled) != 1 || coordinator.canceled[0] != "agent-1" {
		t.Fatalf("unexpected cancel result: %#v", cancel)
	}
}

func TestWorkerToolsValidateArguments(t *testing.T) {
	ctx := ExecutionContext{WorkerAgents: &recordingWorkerCoordinator{}}
	empty := Execute(ctx, WorkerAgentsSpawnToolName, json.RawMessage(`{"workers":[]}`))
	if empty.Success || empty.Error == nil || empty.Error.Code != "invalid_arguments" {
		t.Fatalf("empty spawn did not fail safely: %#v", empty)
	}
	missingTask := Execute(ctx, WorkerAgentsSpawnToolName, json.RawMessage(`{"workers":[{"name":"A"}]}`))
	if missingTask.Success || missingTask.Error == nil || missingTask.Error.Code != "invalid_arguments" {
		t.Fatalf("taskless spawn did not fail safely: %#v", missingTask)
	}
	badWait := Execute(ctx, WorkerAgentsWaitToolName, json.RawMessage(`{"waitFor":"sometimes"}`))
	if badWait.Success || badWait.Error == nil || badWait.Error.Code != "invalid_arguments" {
		t.Fatalf("invalid waitFor did not fail safely: %#v", badWait)
	}
}

func TestWorkerToolsFailSafelyWithoutCoordinator(t *testing.T) {
	result := Execute(ExecutionContext{}, WorkerAgentsSpawnToolName, json.RawMessage(`{"workers":[{"task":"implement"}]}`))
	if result.Success || result.Error == nil || result.Error.Code != "worker_agents_unavailable" {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}

func TestWorkerAgentToolsAreHiddenWithoutWorkersEnabled(t *testing.T) {
	scopes := NewToolScopeChecker([]ToolPermission{
		{Name: WorkerAgentsSpawnToolName}, {Name: WorkerAgentSendToolName},
		{Name: WorkerAgentsWaitToolName}, {Name: WorkerAgentsCancelToolName},
	})
	for _, schema := range ChatLLMSchemaForScopes(scopes, ChatSchemaOptions{}) {
		if IsWorkerAgentToolName(schema.Function.Name) {
			t.Fatalf("disabled parent schema exposed %s", schema.Function.Name)
		}
	}
	names := schemaNames(ChatLLMSchemaForScopes(scopes, ChatSchemaOptions{WorkersEnabled: true}))
	for _, name := range []string{WorkerAgentsSpawnToolName, WorkerAgentSendToolName, WorkerAgentsWaitToolName, WorkerAgentsCancelToolName} {
		if !names[name] {
			t.Fatalf("enabled parent schema omitted %s", name)
		}
	}
}

func TestWorkerChildSchemaIsAllowlisted(t *testing.T) {
	scopes := NewToolScopeChecker([]ToolPermission{
		{Name: "filesystem_read_text"}, {Name: "filesystem_edit_text"},
		{Name: "shell_command"}, {Name: "create_agent_mode"},
		{Name: ResearchAgentsSpawnToolName},
	})
	names := schemaNames(WorkerLLMSchemaForScopes(scopes))
	for _, name := range []string{"filesystem_read_text", "filesystem_edit_text", "shell_command"} {
		if !names[name] {
			t.Fatalf("worker child schema omitted implementation tool %s", name)
		}
	}
	for _, name := range []string{"create_agent_mode", ResearchAgentsSpawnToolName, WorkerAgentsSpawnToolName, "ask_user_questions", "update_goal"} {
		if names[name] {
			t.Fatalf("worker child schema exposed forbidden tool %s", name)
		}
	}
}
