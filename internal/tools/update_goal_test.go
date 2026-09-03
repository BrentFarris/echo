package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestParseUpdateGoalArgs(t *testing.T) {
	args, err := ParseUpdateGoalArgs(json.RawMessage(`{"status":" complete ","reason":" tests pass "}`))
	if err != nil || args.Status != "complete" || args.Reason != "tests pass" {
		t.Fatalf("unexpected parsed arguments: %#v (%v)", args, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"status":"done","reason":"finished"}`,
		`{"status":"blocked","reason":" "}`,
		`{"status":"complete","reason":"` + strings.Repeat("x", MaxGoalReasonLen+1) + `"}`,
	} {
		if _, err := ParseUpdateGoalArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected invalid arguments to fail: %s", raw)
		}
	}
}

func TestUpdateGoalOnlyAppearsInGoalChatSchema(t *testing.T) {
	scopes := NewToolScopeChecker([]ToolPermission{{Name: UpdateGoalToolName}})
	for _, schema := range [][]llm.Tool{
		LLMSchemaForScopes(scopes),
		ChatLLMSchemaForScopes(scopes, ChatSchemaOptions{PlanMode: true}),
	} {
		for _, tool := range schema {
			if tool.Function.Name == UpdateGoalToolName {
				t.Fatalf("%s leaked outside Goal mode", UpdateGoalToolName)
			}
		}
	}
	found := false
	for _, tool := range ChatLLMSchemaForScopes(scopes, ChatSchemaOptions{GoalMode: true}) {
		found = found || tool.Function.Name == UpdateGoalToolName
	}
	if !found {
		t.Fatalf("%s missing from Goal-mode chat schema", UpdateGoalToolName)
	}

	general := ChatLLMSchemaForScopes(nil, ChatSchemaOptions{})
	goal := ChatLLMSchemaForScopes(nil, ChatSchemaOptions{GoalMode: true})
	if len(goal) != len(general)+1 {
		t.Fatalf("Goal mode should add exactly one tool to General: general=%d goal=%d", len(general), len(goal))
	}
	generalNames := make(map[string]bool, len(general))
	for _, tool := range general {
		generalNames[tool.Function.Name] = true
	}
	for _, tool := range goal {
		if tool.Function.Name != UpdateGoalToolName && !generalNames[tool.Function.Name] {
			t.Fatalf("Goal mode exposed non-General tool %q", tool.Function.Name)
		}
	}
}

func TestUpdateGoalCannotExecuteDirectly(t *testing.T) {
	result := Execute(ExecutionContext{}, UpdateGoalToolName, json.RawMessage(`{"status":"complete","reason":"done"}`))
	if result.Success || result.Error == nil || result.Error.Message == "" {
		t.Fatalf("expected direct execution to fail safely, got %#v", result)
	}
}
