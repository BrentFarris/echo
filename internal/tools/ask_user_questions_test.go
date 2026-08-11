package tools

import (
	"encoding/json"
	"testing"
)

func planQuestionArgs(t *testing.T, args map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return data
}

func TestParseAskUserQuestionsArgs(t *testing.T) {
	valid := map[string]any{
		"questions": []map[string]any{
			{"id": "scope", "question": "Which scope should we cover?", "options": []string{"Core", "Extended"}},
			{"id": "lang", "question": "Preferred language?"},
		},
	}
	args, err := ParseAskUserQuestionsArgs(planQuestionArgs(t, valid))
	if err != nil {
		t.Fatalf("unexpected error for valid args: %v", err)
	}
	if len(args.Questions) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(args.Questions))
	}
	if args.Questions[0].ID != "scope" || len(args.Questions[0].Options) != 2 {
		t.Fatalf("unexpected parsed question: %#v", args.Questions[0])
	}

	missing := []struct {
		name string
		args map[string]any
	}{
		{"empty payload", map[string]any{}},
		{"no questions", map[string]any{"questions": []any{}}},
		{"too many", map[string]any{"questions": []any{
			map[string]any{"id": "q1", "question": "a"},
			map[string]any{"id": "q2", "question": "b"},
			map[string]any{"id": "q3", "question": "c"},
			map[string]any{"id": "q4", "question": "d"},
		}}},
		{"missing id", map[string]any{"questions": []any{map[string]any{"question": "x"}}}},
		{"missing question text", map[string]any{"questions": []any{map[string]any{"id": "q1"}}}},
		{"duplicate id", map[string]any{"questions": []any{
			map[string]any{"id": "q1", "question": "a"},
			map[string]any{"id": "q1", "question": "b"},
		}}},
		{"too many options", map[string]any{"questions": []any{map[string]any{
			"id": "q1", "question": "a", "options": []string{"1", "2", "3", "4"},
		}}}},
		{"blank id", map[string]any{"questions": []any{map[string]any{"id": "  ", "question": "x"}}}},
	}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAskUserQuestionsArgs(planQuestionArgs(t, tc.args)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestAskUserQuestionsPlanModeOnly(t *testing.T) {
	if !IsPlanModeToolName(AskUserQuestionsToolName) {
		t.Fatalf("expected %s to be a plan-mode tool", AskUserQuestionsToolName)
	}
	planSchema := PlanModeDirectLLMSchema()
	foundPlan := false
	for _, tool := range planSchema {
		if tool.Function.Name == AskUserQuestionsToolName {
			foundPlan = true
			break
		}
	}
	if !foundPlan {
		t.Fatalf("expected %s in PlanModeDirectLLMSchema", AskUserQuestionsToolName)
	}
	for _, tool := range LLMSchema() {
		if tool.Function.Name == AskUserQuestionsToolName {
			t.Fatalf("expected %s to be excluded from LLMSchema", AskUserQuestionsToolName)
		}
	}
	for _, tool := range ChatLLMSchema() {
		if tool.Function.Name == AskUserQuestionsToolName {
			t.Fatalf("expected %s to be excluded from ChatLLMSchema", AskUserQuestionsToolName)
		}
	}
}

func TestAskUserQuestionsCannotRunDirectly(t *testing.T) {
	result := Execute(ExecutionContext{}, AskUserQuestionsToolName, json.RawMessage(`{"questions":[]}`))
	if result.Success {
		t.Fatalf("expected standalone execution to fail")
	}
	if result.Error == nil || result.Error.Message == "" {
		t.Fatalf("expected a surfaced error message")
	}
}
