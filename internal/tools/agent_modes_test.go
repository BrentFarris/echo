package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type recordingAgentModeProvider struct {
	request AgentModeCreationRequest
}

func (p *recordingAgentModeProvider) CreateAgentMode(_ context.Context, request AgentModeCreationRequest) (AgentModeCreationResult, error) {
	p.request = request
	return AgentModeCreationResult{ID: "mode-1", Name: request.Name, Prompt: request.Prompt}, nil
}

func TestCreateAgentModeUsesHostProvider(t *testing.T) {
	provider := &recordingAgentModeProvider{}
	result := Execute(ExecutionContext{Context: context.Background(), AgentModes: provider}, "create_agent_mode", json.RawMessage(`{
		"name":"  Reviewer  ",
		"prompt":"  Review changes.  ",
		"permissions":{"filesystem_read_text":["src/**"]}
	}`))
	if !result.Success {
		t.Fatalf("create failed: %+v", result.Error)
	}
	if provider.request.Name != "Reviewer" || provider.request.Prompt != "Review changes." {
		t.Fatalf("request was not normalized: %+v", provider.request)
	}
	if len(provider.request.Permissions["filesystem_read_text"]) != 1 {
		t.Fatalf("permissions were not forwarded: %+v", provider.request.Permissions)
	}
	created := result.Output.(AgentModeCreationResult)
	if created.ID != "mode-1" || created.Name != "Reviewer" {
		t.Fatalf("unexpected result: %+v", created)
	}
}

func TestCreateAgentModeRejectsMissingNameAndProvider(t *testing.T) {
	missingName := Execute(ExecutionContext{Context: context.Background(), AgentModes: &recordingAgentModeProvider{}}, "create_agent_mode", json.RawMessage(`{"name":" "}`))
	if missingName.Success || missingName.Error == nil || missingName.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments, got %+v", missingName)
	}
	missingProvider := Execute(ExecutionContext{Context: context.Background()}, "create_agent_mode", json.RawMessage(`{"name":"Reviewer"}`))
	if missingProvider.Success || missingProvider.Error == nil || missingProvider.Error.Code != "agent_modes_unavailable" {
		t.Fatalf("expected agent_modes_unavailable, got %+v", missingProvider)
	}
}

func TestCreateAgentModeSelfRegisters(t *testing.T) {
	for _, tool := range Registered() {
		if tool.Metadata().Name == "create_agent_mode" {
			return
		}
	}
	t.Fatal("create_agent_mode was not registered")
}
