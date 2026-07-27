package tools

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	workspaceSkillMinFutureTasks           = 2
	workspaceSkillMaxFutureTasks           = 4
	workspaceSkillMaxDurabilityReasonRunes = 500
	workspaceSkillMaxFutureTaskRunes       = 200
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
			Description: "Complete the required learning checkpoint after workspace changes. Default to action skip for one-off bug fixes, patch summaries, and knowledge already captured by code, tests, or docs. Use action upsert only for stable project guidance reusable across multiple distinct future tasks; include durabilityReason and futureTasks, prefer an existing broad skill, and pass its revision when updating it.",
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
						"description": "For upsert, the complete Markdown body without YAML frontmatter. Write generalized guidance, not sections recounting the current bug, root cause, fix, changed lines, or before/after patch.",
					},
					"durabilityReason": map[string]any{
						"type":        "string",
						"description": "Required for upsert. Explain why this knowledge is stable, not already adequately captured by code/tests/docs, and useful beyond the current task.",
					},
					"futureTasks": map[string]any{
						"type":        "array",
						"description": "Required for upsert. Two to four distinct future task examples, different from the current task, that would benefit from this skill.",
						"minItems":    workspaceSkillMinFutureTasks,
						"maxItems":    workspaceSkillMaxFutureTasks,
						"items":       map[string]any{"type": "string"},
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
	request.DurabilityReason = strings.TrimSpace(request.DurabilityReason)
	request.ExpectedRevision = strings.TrimSpace(request.ExpectedRevision)
	request.FutureTasks = normalizeWorkspaceSkillFutureTasks(request.FutureTasks)
	if request.Action != "upsert" && request.Action != "skip" {
		return nil, SafeError{Code: "invalid_arguments", Message: "action must be upsert or skip"}
	}
	if request.Action == "skip" && request.Reason == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "reason is required when action is skip"}
	}
	if request.Action == "upsert" {
		if err := validateWorkspaceSkillDurability(request); err != nil {
			return nil, err
		}
	}
	if ctx.WorkspaceSkills == nil {
		return nil, SafeError{Code: "workspace_skills_unavailable", Message: "workspace skills are not available in this context"}
	}
	return ctx.WorkspaceSkills.RecordWorkspaceSkill(ctx.context(), request)
}

func validateWorkspaceSkillDurability(request WorkspaceSkillRecordRequest) error {
	if request.DurabilityReason == "" {
		return SafeError{
			Code:    "skill_durability_required",
			Message: "durabilityReason is required for upsert; use skip when the knowledge is specific to the current change",
		}
	}
	if utf8.RuneCountInString(request.DurabilityReason) > workspaceSkillMaxDurabilityReasonRunes {
		return SafeError{
			Code:    "invalid_arguments",
			Message: "durabilityReason must not exceed 500 characters",
		}
	}
	if len(request.FutureTasks) < workspaceSkillMinFutureTasks || len(request.FutureTasks) > workspaceSkillMaxFutureTasks {
		return SafeError{
			Code:    "skill_durability_required",
			Message: "futureTasks must contain two to four distinct future tasks for upsert; otherwise use skip",
		}
	}
	for _, task := range request.FutureTasks {
		if utf8.RuneCountInString(task) > workspaceSkillMaxFutureTaskRunes {
			return SafeError{
				Code:    "invalid_arguments",
				Message: "each futureTasks entry must not exceed 200 characters",
			}
		}
	}
	if workspaceSkillBodyLooksLikeTaskRecap(request.Body) {
		return SafeError{
			Code:    "skill_not_durable",
			Message: "skill body reads like a bug or patch recap; rewrite it as generalized reusable guidance or use skip",
		}
	}
	return nil
}

func normalizeWorkspaceSkillFutureTasks(values []string) []string {
	seen := make(map[string]bool, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, value)
	}
	return normalized
}

func workspaceSkillBodyLooksLikeTaskRecap(body string) bool {
	recapHeadings := map[string]bool{
		"bug":            true,
		"the bug":        true,
		"root cause":     true,
		"fix":            true,
		"the fix":        true,
		"changes made":   true,
		"changed files":  true,
		"implementation": true,
	}
	markers := 0
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		heading := strings.TrimSpace(line)
		if !strings.HasPrefix(heading, "#") {
			continue
		}
		heading = strings.TrimSpace(strings.TrimLeft(heading, "#"))
		heading = strings.TrimSuffix(strings.ToLower(heading), ":")
		if recapHeadings[heading] {
			markers++
		}
	}
	return markers >= 2
}
