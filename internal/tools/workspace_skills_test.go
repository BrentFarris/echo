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

func TestWorkspaceSkillRecordRequiresDurabilityEvidenceForUpsert(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:         context.Background(),
		WorkspaceSkills: &testWorkspaceSkillsProvider{},
	}, "workspace_skill_record", workspaceSkillTestJSON(t, map[string]any{
		"action":      "upsert",
		"folder":      "echo",
		"name":        "file-database",
		"description": "Reusable file database guidance.",
		"body":        "# File database\n\nValidate cache freshness before using indexed results.",
	}))
	if result.Success || result.Error == nil || result.Error.Code != "skill_durability_required" {
		t.Fatalf("expected missing durability evidence to be rejected, got %#v", result)
	}
}

func TestWorkspaceSkillRecordAcceptsAndNormalizesDurabilityEvidence(t *testing.T) {
	provider := &testWorkspaceSkillsProvider{}
	result := Execute(ExecutionContext{
		Context:         context.Background(),
		WorkspaceSkills: provider,
	}, "workspace_skill_record", workspaceSkillTestJSON(t, map[string]any{
		"action":           "upsert",
		"folder":           "echo",
		"name":             "file-database",
		"description":      "Reusable file database guidance.",
		"body":             "# File database\n\nValidate cache freshness before using indexed results.",
		"durabilityReason": " Cache freshness is a stable invariant not expressed at individual call sites. ",
		"futureTasks": []string{
			" Diagnose stale workspace search results. ",
			"Change file database invalidation behavior.",
			"diagnose stale workspace search results.",
		},
	}))
	if !result.Success {
		t.Fatalf("expected durable skill request to succeed, got %#v", result)
	}
	if provider.recordRequest.DurabilityReason != "Cache freshness is a stable invariant not expressed at individual call sites." {
		t.Fatalf("unexpected durability reason: %q", provider.recordRequest.DurabilityReason)
	}
	if len(provider.recordRequest.FutureTasks) != 2 ||
		provider.recordRequest.FutureTasks[0] != "Diagnose stale workspace search results." ||
		provider.recordRequest.FutureTasks[1] != "Change file database invalidation behavior." {
		t.Fatalf("unexpected normalized future tasks: %#v", provider.recordRequest.FutureTasks)
	}
}

func TestWorkspaceSkillRecordRejectsBugFixRecap(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:         context.Background(),
		WorkspaceSkills: &testWorkspaceSkillsProvider{},
	}, "workspace_skill_record", workspaceSkillTestJSON(t, map[string]any{
		"action":      "upsert",
		"folder":      "echo",
		"name":        "title-screen-mouse-click",
		"description": "Mouse activation behavior during title screen animation.",
		"body": "# Title screen mouse click\n\n## The Bug\n\nClicks were blocked during animation.\n\n" +
			"## Root Cause\n\nA state guard rejected activation.\n\n## The Fix\n\nThe guard was removed.",
		"durabilityReason": "The input behavior may affect future title screen changes.",
		"futureTasks": []string{
			"Change title screen animation.",
			"Modify mouse input handling.",
		},
	}))
	if result.Success || result.Error == nil || result.Error.Code != "skill_not_durable" {
		t.Fatalf("expected bug-fix recap to be rejected, got %#v", result)
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
