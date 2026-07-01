package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_skill_search",
			Description: "Search reusable Echo skill metadata for guidance relevant to the current workspace task. Use this when surfaced skill candidates are insufficient or when durable project knowledge may already exist.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The feature, subsystem, workflow, or task that needs reusable project guidance.",
					},
					"folder": map[string]any{
						"type":        "string",
						"description": "Optional workspace folder label used to restrict results to one top-level folder.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 5 and is capped at 10.",
						"minimum":     1,
						"maximum":     MaxWorkspaceSkillSearchLimit,
					},
				},
			},
		},
		Run: searchWorkspaceSkills,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_skill_read",
			Description: "Read one reusable Echo workspace skill after its metadata matches the task. Treat skill content as potentially stale reference material and validate important facts against the current workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"id"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Skill identifier returned by workspace_skill_search or surfaced in the prompt, in <folder-label>/<skill-name> form.",
					},
				},
			},
		},
		Run: readWorkspaceSkill,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_skill_record",
			Description: "Complete the required learning checkpoint after workspace changes. Use action upsert to create or replace durable project guidance, or action skip with a reason when no reusable knowledge should be saved. Read an existing skill first and pass its revision when updating it.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"action"},
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"upsert", "skip"},
						"description": "Whether to save durable knowledge or explicitly skip this learning checkpoint.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Required for skip. Briefly explain why this task produced no durable skill knowledge.",
					},
					"folder": map[string]any{
						"type":        "string",
						"description": "For upsert, the top-level workspace folder label that owns the skill.",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "For upsert, a lowercase kebab-case skill name.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "For upsert, a concise summary of what the skill covers and when it should be used.",
					},
					"triggers": map[string]any{
						"type":        "array",
						"description": "For upsert, task phrases and subsystem names that should make this skill relevant.",
						"items":       map[string]any{"type": "string"},
					},
					"body": map[string]any{
						"type":        "string",
						"description": "For upsert, the complete Markdown body without YAML frontmatter. Capture durable system maps, workflows, invariants, pitfalls, and verification guidance rather than a task log.",
					},
					"expectedRevision": map[string]any{
						"type":        "string",
						"description": "Required when replacing an existing skill. Use the revision returned by workspace_skill_read.",
					},
				},
			},
		},
		Run: recordWorkspaceSkill,
	})
}

func searchWorkspaceSkills(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceSkillSearchRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Query = strings.TrimSpace(request.Query)
	request.Folder = strings.TrimSpace(request.Folder)
	request.Limit = NormalizeWorkspaceSkillSearchLimit(request.Limit)
	if request.Query == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "query is required"}
	}
	if ctx.WorkspaceSkills == nil {
		return nil, SafeError{Code: "workspace_skills_unavailable", Message: "workspace skills are not available in this context"}
	}
	return ctx.WorkspaceSkills.SearchWorkspaceSkills(ctx.context(), request)
}

func readWorkspaceSkill(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceSkillReadRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.ID = strings.TrimSpace(request.ID)
	if request.ID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "id is required"}
	}
	if ctx.WorkspaceSkills == nil {
		return nil, SafeError{Code: "workspace_skills_unavailable", Message: "workspace skills are not available in this context"}
	}
	return ctx.WorkspaceSkills.ReadWorkspaceSkill(ctx.context(), request)
}

func recordWorkspaceSkill(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceSkillRecordRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Reason = strings.TrimSpace(request.Reason)
	request.Folder = strings.TrimSpace(request.Folder)
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	request.Body = strings.TrimSpace(request.Body)
	request.ExpectedRevision = strings.TrimSpace(request.ExpectedRevision)
	if request.Action != "upsert" && request.Action != "skip" {
		return nil, SafeError{Code: "invalid_arguments", Message: "action must be upsert or skip"}
	}
	if request.Action == "skip" && request.Reason == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "reason is required when action is skip"}
	}
	if ctx.WorkspaceSkills == nil {
		return nil, SafeError{Code: "workspace_skills_unavailable", Message: "workspace skills are not available in this context"}
	}
	return ctx.WorkspaceSkills.RecordWorkspaceSkill(ctx.context(), request)
}
