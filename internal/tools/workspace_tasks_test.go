package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeWorkspaceTasksProvider struct {
	listRequest   WorkspaceTaskListRequest
	createRequest WorkspaceTaskCreateRequest
	convertRequest WorkspaceTaskConvertRequest
}

func (p *fakeWorkspaceTasksProvider) ListWorkspaceTasks(_ context.Context, request WorkspaceTaskListRequest) (WorkspaceTaskListResponse, error) {
	p.listRequest = request
	return WorkspaceTaskListResponse{StoragePath: "project/.echo/tasks.json", Tasks: []WorkspaceTask{{ID: "one", Title: "Review", Priority: "P0"}}}, nil
}

func (p *fakeWorkspaceTasksProvider) CreateWorkspaceTask(_ context.Context, request WorkspaceTaskCreateRequest) (WorkspaceTaskMutationResponse, error) {
	p.createRequest = request
	return WorkspaceTaskMutationResponse{Created: WorkspaceTask{ID: "two", Title: request.Title, Priority: request.Priority}}, nil
}

func (p *fakeWorkspaceTasksProvider) ConvertTaskToKanbanCard(_ context.Context, request WorkspaceTaskConvertRequest) (WorkspaceTaskConversionResponse, error) {
	p.convertRequest = request
	return WorkspaceTaskConversionResponse{
		TaskID:       request.TaskID,
		KanbanCardID: "card-1",
		Task:         &WorkspaceTask{ID: request.TaskID, Title: "Test", Completed: true},
		Tasks:        []WorkspaceTask{{ID: request.TaskID, Title: "Test", Completed: true}},
	}, nil
}

func TestWorkspaceTaskToolsListAndCreate(t *testing.T) {
	provider := &fakeWorkspaceTasksProvider{}
	ctx := ExecutionContext{Context: context.Background(), WorkspaceTasks: provider}

	list := Execute(ctx, "workspace_task_list", json.RawMessage(`{"priority":"p0","includeCompleted":true}`))
	if !list.Success || provider.listRequest.Priority != "P0" || !provider.listRequest.IncludeCompleted {
		t.Fatalf("unexpected list result: %#v request=%#v", list, provider.listRequest)
	}

	create := Execute(ctx, "workspace_task_create", json.RawMessage(`{"title":"  Add task  ","acceptanceCriteria":[" Done ",""]}`))
	if !create.Success {
		t.Fatalf("create failed: %#v", create)
	}
	if provider.createRequest.Title != "Add task" || provider.createRequest.Priority != "P1" ||
		len(provider.createRequest.AcceptanceCriteria) != 1 || provider.createRequest.AcceptanceCriteria[0] != "Done" {
		t.Fatalf("unexpected create request: %#v", provider.createRequest)
	}
}

func TestWorkspaceTaskCreateIsAvailableInPlanMode(t *testing.T) {
	names := map[string]bool{}
	for _, schema := range PlanModeLLMSchema() {
		names[schema.Function.Name] = true
	}
	if !names["workspace_task_list"] || !names["workspace_task_create"] {
		t.Fatalf("expected task tools in plan mode schema, got %#v", names)
	}
	if IsReadOnlyToolName("workspace_task_create") || !IsMutatingToolName("workspace_task_create") || !IsPlanModeToolName("workspace_task_create") {
		t.Fatal("workspace_task_create has incorrect permission classification")
	}
}
