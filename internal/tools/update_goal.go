package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UpdateGoalToolName is a Goal-mode-only orchestration tool. The chat host
// intercepts it so an ordinary assistant response cannot accidentally end a
// durable goal.
const UpdateGoalToolName = "update_goal"

const MaxGoalReasonLen = 4000

type UpdateGoalArgs struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        UpdateGoalToolName,
			Description: "Mark the current durable goal complete or blocked. Use only after verifying completion, or when progress genuinely requires user input or an external state change. This must be the only tool call in the assistant step. Goal mode only.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"status", "reason"},
				"properties": map[string]any{
					"status": map[string]any{
						"type":        "string",
						"enum":        []any{"complete", "blocked"},
						"description": "Use complete only when the objective and its verification criteria are satisfied; use blocked only for a genuine external blocker.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Concise completion evidence or the precise blocker and required next action.",
					},
				},
			},
		},
		Run: func(_ ExecutionContext, _ json.RawMessage) (any, error) {
			return nil, fmt.Errorf("%s is handled by the Goal chat session; it cannot be invoked directly", UpdateGoalToolName)
		},
	})
}

func ParseUpdateGoalArgs(raw json.RawMessage) (UpdateGoalArgs, error) {
	var args UpdateGoalArgs
	if len(strings.TrimSpace(string(raw))) == 0 {
		return args, fmt.Errorf("update_goal requires status and reason")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("update_goal received invalid arguments: %v", err)
	}
	args.Status = strings.TrimSpace(args.Status)
	args.Reason = strings.TrimSpace(args.Reason)
	if args.Status != "complete" && args.Status != "blocked" {
		return args, fmt.Errorf("update_goal status must be complete or blocked")
	}
	if args.Reason == "" {
		return args, fmt.Errorf("update_goal requires a non-empty reason")
	}
	if len(args.Reason) > MaxGoalReasonLen {
		return args, fmt.Errorf("update_goal reason exceeds %d characters", MaxGoalReasonLen)
	}
	return args, nil
}
