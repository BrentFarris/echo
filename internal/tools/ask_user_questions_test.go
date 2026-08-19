package tools

import (
	"encoding/json"
	"testing"
)

func askUserQuestionArgs(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal arguments: %v", err)
	}
	return data
}

func TestParseAskUserQuestionsArgs(t *testing.T) {
	args, err := ParseAskUserQuestionsArgs(askUserQuestionArgs(t, map[string]any{
		"questions": []map[string]any{
			{"id": "scope", "question": "Which scope?", "options": []string{"Core", "Extended"}},
			{"id": "language", "question": "Which language?"},
		},
	}))
	if err != nil {
		t.Fatalf("parse valid arguments: %v", err)
	}
	if len(args.Questions) != 2 || args.Questions[0].ID != "scope" || len(args.Questions[0].Options) != 2 {
		t.Fatalf("unexpected parsed arguments: %#v", args)
	}

	invalid := []any{
		map[string]any{},
		map[string]any{"questions": []any{}},
		map[string]any{"questions": []any{
			map[string]any{"id": "1", "question": "One"},
			map[string]any{"id": "2", "question": "Two"},
			map[string]any{"id": "3", "question": "Three"},
			map[string]any{"id": "4", "question": "Four"},
		}},
		map[string]any{"questions": []any{map[string]any{"question": "Missing ID"}}},
		map[string]any{"questions": []any{map[string]any{"id": "q"}}},
		map[string]any{"questions": []any{
			map[string]any{"id": "q", "question": "One"},
			map[string]any{"id": "q", "question": "Two"},
		}},
		map[string]any{"questions": []any{map[string]any{
			"id": "q", "question": "Too many options", "options": []string{"1", "2", "3", "4"},
		}}},
	}
	for i, value := range invalid {
		if _, err := ParseAskUserQuestionsArgs(askUserQuestionArgs(t, value)); err == nil {
			t.Fatalf("case %d: expected invalid arguments to fail", i)
		}
	}
}

func TestAskUserQuestionsOnlyAppearsInPlanChatSchema(t *testing.T) {
	scopes := NewToolScopeChecker([]ToolPermission{{Name: AskUserQuestionsToolName}})
	for _, tool := range LLMSchemaForScopes(scopes) {
		if tool.Function.Name == AskUserQuestionsToolName {
			t.Fatalf("%s must be excluded from the standard chat schema", AskUserQuestionsToolName)
		}
	}
	found := false
	for _, tool := range ChatLLMSchemaForScopes(scopes, true) {
		found = found || tool.Function.Name == AskUserQuestionsToolName
	}
	if !found {
		t.Fatalf("%s missing from the Plan-mode chat schema", AskUserQuestionsToolName)
	}
}

func TestAskUserQuestionsCannotExecuteDirectly(t *testing.T) {
	result := Execute(ExecutionContext{}, AskUserQuestionsToolName, json.RawMessage(`{"questions":[]}`))
	if result.Success || result.Error == nil || result.Error.Message == "" {
		t.Fatalf("expected direct execution to fail safely, got %#v", result)
	}
}
