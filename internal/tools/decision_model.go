package tools

import (
	"encoding/json"
	"fmt"

	"github.com/brent/echo/internal/decision"
)

// decision_model asks the configured Decision Model — a fast probabilistic
// endpoint — one or more typed questions about a piece of state and returns a
// probability distribution for each possible outcome. Use it for routing,
// triage, and classification decisions that do not need a full LLM turn.
func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name: "decision_model",
			Description: "Ask the configured Decision Model (a fast probabilistic classifier) typed questions about a piece of state and get a probability for each possible outcome. " +
				"Use it for quick routing, triage, urgency, or yes/no judgments instead of a full reasoning turn. " +
				"Question types: \"choice\" (pick one option from a criteria map of label -> description), \"noul\" (yes/no probability), " +
				"and \"score\" (weighted score over an ordered legend array). Fails with decision_model_disabled when no endpoint is configured.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"state", "questions"},
				"properties": map[string]any{
					"state": map[string]any{
						"type":        "string",
						"description": "The text to classify, e.g. the user's message or the content being judged.",
					},
					"questions": map[string]any{
						"type": "array",
						"description": "Questions to answer in one call. Each entry needs a unique name, a type, and instructions.",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []any{"name", "type", "instructions"},
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Unique key for the question; the answer is returned under this name.",
								},
								"type": map[string]any{
									"type":        "string",
									"enum":        []any{"choice", "noul", "score"},
									"description": "choice = pick one option; noul = yes/no probability; score = weighted score over an ordered legend.",
								},
								"instructions": map[string]any{
									"type":        "string",
									"description": "What the decision model should determine.",
								},
								"criteria": map[string]any{
									"description": "For choice: object mapping option label to description. For score: array of ordered legend entries. Ignored for noul.",
									"oneOf": []any{
										map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
										map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
									},
								},
							},
						},
					},
				},
			},
		},
		Run: decisionModelTool,
	})
}

type decisionModelArgs struct {
	State     string                 `json:"state"`
	Questions []decisionModelQuestion `json:"questions"`
}

type decisionModelQuestion struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

func decisionModelTool(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	if !ctx.DecisionModel.Enabled() {
		return nil, SafeError{Code: "decision_model_disabled", Message: "no decision model endpoint is configured in settings"}
	}
	var args decisionModelArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.State == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "state is required"}
	}
	if len(args.Questions) == 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "at least one question is required"}
	}

	builder := ctx.DecisionModel.Ask(ctx.context(), args.State)
	seen := map[string]bool{}
	for _, question := range args.Questions {
		if question.Name == "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "every question needs a name"}
		}
		if seen[question.Name] {
			return nil, SafeError{Code: "invalid_arguments", Message: "duplicate question name " + question.Name}
		}
		seen[question.Name] = true
		switch question.Type {
		case string(decision.KindChoice):
			criteria, err := decodeChoiceCriteria(question.Criteria)
			if err != nil {
				return nil, SafeError{Code: "invalid_arguments", Message: question.Name + ": " + err.Error()}
			}
			builder = builder.Choice(question.Name, question.Instructions, criteria)
		case string(decision.KindNoul):
			builder = builder.Noul(question.Name, question.Instructions)
		case string(decision.KindScore):
			legend, err := decodeScoreLegend(question.Criteria)
			if err != nil {
				return nil, SafeError{Code: "invalid_arguments", Message: question.Name + ": " + err.Error()}
			}
			builder = builder.Score(question.Name, question.Instructions, legend)
		default:
			return nil, SafeError{Code: "invalid_arguments", Message: question.Name + ": type must be choice, noul, or score"}
		}
	}

	response, err := builder.Do()
	if err != nil {
		if ctxErr := ctx.context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, SafeError{Code: "decision_model_failed", Message: err.Error()}
	}
	return response, nil
}

func decodeChoiceCriteria(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("choice questions need a criteria object mapping option label to description")
	}
	var criteria map[string]string
	if err := json.Unmarshal(raw, &criteria); err != nil {
		return nil, fmt.Errorf("choice criteria must be an object mapping option label to description")
	}
	if len(criteria) == 0 {
		return nil, fmt.Errorf("choice criteria must contain at least one option")
	}
	return criteria, nil
}

func decodeScoreLegend(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("score questions need a criteria array of ordered legend entries")
	}
	var legend []string
	if err := json.Unmarshal(raw, &legend); err != nil {
		return nil, fmt.Errorf("score criteria must be an array of ordered legend entries")
	}
	if len(legend) == 0 {
		return nil, fmt.Errorf("score criteria must contain at least one legend entry")
	}
	return legend, nil
}
