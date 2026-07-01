package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type testWorkspaceSkillsProvider struct {
	searchRequest WorkspaceSkillSearchRequest
	readRequest   WorkspaceSkillReadRequest
	recordRequest WorkspaceSkillRecordRequest
}

func (p *testWorkspaceSkillsProvider) SearchWorkspaceSkills(_ context.Context, request WorkspaceSkillSearchRequest) (WorkspaceSkillSearchResponse, error) {
	p.searchRequest = request
	return WorkspaceSkillSearchResponse{
		Query: request.Query,
		Skills: []WorkspaceSkillSummary{{
			ID:          "echo/file-database",
			Folder:      "echo",
			Name:        "file-database",
			Description: "Workspace file database behavior.",
		}},
	}, nil
}

func (p *testWorkspaceSkillsProvider) ReadWorkspaceSkill(_ context.Context, request WorkspaceSkillReadRequest) (WorkspaceSkill, error) {
	p.readRequest = request
	return WorkspaceSkill{
		WorkspaceSkillSummary: WorkspaceSkillSummary{ID: request.ID, Folder: "echo", Name: "file-database"},
		Body:                  "# File database",
		Revision:              "revision",
	}, nil
}

func (p *testWorkspaceSkillsProvider) RecordWorkspaceSkill(_ context.Context, request WorkspaceSkillRecordRequest) (WorkspaceSkillRecordResponse, error) {
	p.recordRequest = request
	return WorkspaceSkillRecordResponse{Action: request.Action, Reason: request.Reason}, nil
}

func TestWorkspaceSkillToolsUseProvider(t *testing.T) {
	provider := &testWorkspaceSkillsProvider{}
	ctx := ExecutionContext{Context: context.Background(), WorkspaceSkills: provider}

	search := Execute(ctx, "workspace_skill_search", workspaceSkillTestJSON(t, map[string]any{
		"query": " file database ",
		"limit": 100,
	}))
	if !search.Success || provider.searchRequest.Query != "file database" || provider.searchRequest.Limit != MaxWorkspaceSkillSearchLimit {
		t.Fatalf("unexpected search result=%#v request=%#v", search, provider.searchRequest)
	}

	read := Execute(ctx, "workspace_skill_read", workspaceSkillTestJSON(t, map[string]any{
		"id": " echo/file-database ",
	}))
	if !read.Success || provider.readRequest.ID != "echo/file-database" {
		t.Fatalf("unexpected read result=%#v request=%#v", read, provider.readRequest)
	}

	record := Execute(ctx, "workspace_skill_record", workspaceSkillTestJSON(t, map[string]any{
		"action": "skip",
		"reason": " already documented ",
	}))
	if !record.Success || provider.recordRequest.Action != "skip" || provider.recordRequest.Reason != "already documented" {
		t.Fatalf("unexpected record result=%#v request=%#v", record, provider.recordRequest)
	}
}

func TestWorkspaceSkillRecordValidatesSkipReason(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:         context.Background(),
		WorkspaceSkills: &testWorkspaceSkillsProvider{},
	}, "workspace_skill_record", workspaceSkillTestJSON(t, map[string]any{"action": "skip"}))
	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid skip arguments, got %#v", result)
	}
}

func workspaceSkillTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
