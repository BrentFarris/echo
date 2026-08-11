package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AskUserQuestionsToolName is the plan-mode-only tool that lets the model ask
// the user structured clarifying questions before finalizing a plan. The
// actual execution is handled by SystemService (it must pause the chat turn for
// interactive answers), so this registration only exposes the schema and name.
const AskUserQuestionsToolName = "ask_user_questions"

// maxPlanQuestionsPerCall and maxPlanQuestionOptions bound the structured
// question payload the model may emit in a single ask_user_questions call.
const (
	MaxPlanQuestionsPerCall  = 3
	MaxPlanQuestionOptions   = 3
	maxPlanQuestionIDLen     = 64
	maxPlanQuestionTextLen   = 500
	maxPlanQuestionOptionLen = 200
	MaxPlanQuestionRounds    = 2
	MaxPlanAnswerTextLen     = 2000
)

// PlanQuestionRequest is the wire format the model supplies in the
// ask_user_questions tool arguments. Services parse this into its Wails-bound
// PlanQuestionSet type for the UI.
type PlanQuestionRequest struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// AskUserQuestionsArgs is the top-level tool arguments shape.
type AskUserQuestionsArgs struct {
	Questions []PlanQuestionRequest `json:"questions"`
}

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        AskUserQuestionsToolName,
			Description: "Ask the user 1-3 structured clarifying questions (each with up to 3 suggested options; free-text is always available) when high-impact ambiguities in scope, target files, approach, constraints, or priorities cannot be resolved by reading the workspace. Answers arrive as the tool result and the user can skip. Plan mode only.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"questions"},
				"properties": map[string]any{
					"questions": map[string]any{
						"type":        "array",
						"description": "1-3 clarifying questions to ask the user.",
						"minItems":    1,
						"maxItems":    MaxPlanQuestionsPerCall,
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []any{"id", "question"},
							"properties": map[string]any{
								"id": map[string]any{
									"type":        "string",
									"description": "Short stable identifier for this question (lowercase kebab-case).",
								},
								"question": map[string]any{
									"type":        "string",
									"description": "The clarifying question, concise and self-contained.",
								},
								"options": map[string]any{
									"type":        "array",
									"description": "Optional suggested answers. Omit or leave empty for a free-text-only question.",
									"maxItems":    MaxPlanQuestionOptions,
									"items": map[string]any{
										"type": "string",
									},
								},
							},
						},
					},
				},
			},
		},
		Run: func(_ ExecutionContext, _ json.RawMessage) (any, error) {
			return nil, fmt.Errorf("%s is handled by the plan chat session; it cannot be invoked directly", AskUserQuestionsToolName)
		},
	})
}

// ParseAskUserQuestionsArgs validates and parses the raw tool arguments into
// the request shape. It returns a user-facing error when the payload is
// malformed or violates the question constraints.
func ParseAskUserQuestionsArgs(raw json.RawMessage) (AskUserQuestionsArgs, error) {
	var args AskUserQuestionsArgs
	if len(strings.TrimSpace(string(raw))) == 0 {
		return args, fmt.Errorf("ask_user_questions requires a non-empty questions array")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("ask_user_questions received invalid arguments: %v", err)
	}
	if len(args.Questions) == 0 {
		return args, fmt.Errorf("ask_user_questions requires at least one question")
	}
	if len(args.Questions) > MaxPlanQuestionsPerCall {
		return args, fmt.Errorf("ask_user_questions supports at most %d questions per call", MaxPlanQuestionsPerCall)
	}
	seen := make(map[string]bool, len(args.Questions))
	for _, question := range args.Questions {
		id := strings.TrimSpace(question.ID)
		if id == "" {
			return args, fmt.Errorf("each question requires a non-empty id")
		}
		if len(id) > maxPlanQuestionIDLen {
			return args, fmt.Errorf("question id %q exceeds %d characters", id, maxPlanQuestionIDLen)
		}
		if seen[id] {
			return args, fmt.Errorf("duplicate question id %q", id)
		}
		seen[id] = true
		text := strings.TrimSpace(question.Question)
		if text == "" {
			return args, fmt.Errorf("question %q is missing its text", id)
		}
		if len(text) > maxPlanQuestionTextLen {
			return args, fmt.Errorf("question %q exceeds %d characters", id, maxPlanQuestionTextLen)
		}
		if len(question.Options) > MaxPlanQuestionOptions {
			return args, fmt.Errorf("question %q has more than %d options", id, MaxPlanQuestionOptions)
		}
		for _, option := range question.Options {
			if len(strings.TrimSpace(option)) > maxPlanQuestionOptionLen {
				return args, fmt.Errorf("question %q has an option exceeding %d characters", id, maxPlanQuestionOptionLen)
			}
		}
	}
	return args, nil
}
