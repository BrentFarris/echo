package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type testSkillsProvider struct {
	search WorkspaceSkillSearchRequest
	read   WorkspaceSkillReadRequest
	record WorkspaceSkillRecordRequest
}

func (p *testSkillsProvider) SearchWorkspaceSkills(_ context.Context, request WorkspaceSkillSearchRequest) (WorkspaceSkillSearchResponse, error) {
	p.search = request
	return WorkspaceSkillSearchResponse{Query: request.Query}, nil
}

func (p *testSkillsProvider) ReadWorkspaceSkill(_ context.Context, request WorkspaceSkillReadRequest) (WorkspaceSkill, error) {
	p.read = request
	return WorkspaceSkill{WorkspaceSkillSummary: WorkspaceSkillSummary{ID: request.ID}}, nil
}

func (p *testSkillsProvider) RecordWorkspaceSkill(_ context.Context, request WorkspaceSkillRecordRequest) (WorkspaceSkillRecordResponse, error) {
	p.record = request
	return WorkspaceSkillRecordResponse{Action: request.Action}, nil
}

func TestWorkspaceSkillToolsUseProvider(t *testing.T) {
	provider := &testSkillsProvider{}
	ctx := ExecutionContext{Context: context.Background(), WorkspaceSkills: provider}
	search := Execute(ctx, "workspace_skill_search", skillJSON(t, map[string]any{"query": " chat streaming ", "limit": 99}))
	if !search.Success || provider.search.Query != "chat streaming" || provider.search.Limit != MaxWorkspaceSkillSearchLimit {
		t.Fatalf("unexpected search: result=%#v request=%#v", search, provider.search)
	}
	read := Execute(ctx, "workspace_skill_read", skillJSON(t, map[string]any{"id": " workspace/chat-streaming "}))
	if !read.Success || provider.read.ID != "workspace/chat-streaming" {
		t.Fatalf("unexpected read: result=%#v request=%#v", read, provider.read)
	}
	record := Execute(ctx, "workspace_skill_record", skillJSON(t, map[string]any{"action": "skip", "reason": " already documented "}))
	if !record.Success || provider.record.Reason != "already documented" {
		t.Fatalf("unexpected record: result=%#v request=%#v", record, provider.record)
	}
}

func TestWorkspaceSkillRecordValidatesDurability(t *testing.T) {
	ctx := ExecutionContext{Context: context.Background(), WorkspaceSkills: &testSkillsProvider{}}
	missing := Execute(ctx, "workspace_skill_record", skillJSON(t, map[string]any{"action": "upsert", "body": "# Guidance"}))
	if missing.Success || missing.Error == nil || missing.Error.Code != "skill_durability_required" {
		t.Fatalf("expected durability error, got %#v", missing)
	}
	recap := Execute(ctx, "workspace_skill_record", skillJSON(t, map[string]any{
		"action": "upsert", "body": "# Topic\n\n## The Bug\nX\n## Root Cause\nY\n## The Fix\nZ",
		"durabilityReason": "Stable project behavior.", "futureTasks": []string{"First future task", "Second future task"},
	}))
	if recap.Success || recap.Error == nil || recap.Error.Code != "skill_not_durable" {
		t.Fatalf("expected recap rejection, got %#v", recap)
	}
}

func TestWorkspaceSkillRecordNormalizesFutureTasks(t *testing.T) {
	provider := &testSkillsProvider{}
	result := Execute(ExecutionContext{Context: context.Background(), WorkspaceSkills: provider}, "workspace_skill_record", skillJSON(t, map[string]any{
		"action": "upsert", "body": "# Durable guidance", "durabilityReason": " Reusable invariant. ",
		"futureTasks": []string{" First task ", "Second task", "first task"},
	}))
	if !result.Success || provider.record.DurabilityReason != "Reusable invariant." || len(provider.record.FutureTasks) != 2 {
		t.Fatalf("unexpected normalized record: result=%#v request=%#v", result, provider.record)
	}
}

func skillJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
