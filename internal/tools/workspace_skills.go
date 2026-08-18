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
	Register(ToolFunc{Meta: Metadata{
		Name:        "workspace_skill_search",
		Description: "Search reusable Echo workspace skill metadata for guidance relevant to the current task.",
		Parameters: Schema{"type": "object", "additionalProperties": false, "required": []any{"query"}, "properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "The feature, subsystem, workflow, or task that needs reusable project guidance."},
			"folder": map[string]any{"type": "string", "description": "Optional workspace folder label used to restrict results."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": MaxWorkspaceSkillSearchLimit, "description": "Maximum results. Defaults to 5 and is capped at 10."},
		}},
	}, Run: searchWorkspaceSkills})

	Register(ToolFunc{Meta: Metadata{
		Name:        "workspace_skill_read",
		Description: "Read one reusable Echo workspace skill after its metadata matches the task. Validate important facts against the current workspace.",
		Parameters: Schema{"type": "object", "additionalProperties": false, "required": []any{"id"}, "properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "Skill ID in <folder-label>/<skill-name> form."},
		}},
	}, Run: readWorkspaceSkill})

	Register(ToolFunc{Meta: Metadata{
		Name:        "workspace_skill_record",
		Description: "Optionally save durable project guidance reusable across multiple future tasks, or skip when there is nothing durable to record. Prefer updating an existing broad skill after reading it.",
		Parameters: Schema{"type": "object", "additionalProperties": false, "required": []any{"action"}, "properties": map[string]any{
			"action":           map[string]any{"type": "string", "enum": []any{"upsert", "skip"}},
			"reason":           map[string]any{"type": "string", "description": "Required for skip."},
			"folder":           map[string]any{"type": "string", "description": "Workspace folder label for upsert."},
			"name":             map[string]any{"type": "string", "description": "Lowercase kebab-case skill name."},
			"description":      map[string]any{"type": "string", "description": "What the skill covers and when it applies."},
			"triggers":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"body":             map[string]any{"type": "string", "description": "Complete Markdown body without YAML frontmatter; write generalized guidance rather than a patch recap."},
			"durabilityReason": map[string]any{"type": "string", "description": "Required for upsert; explain why the knowledge is stable and reusable."},
			"futureTasks":      map[string]any{"type": "array", "minItems": workspaceSkillMinFutureTasks, "maxItems": workspaceSkillMaxFutureTasks, "items": map[string]any{"type": "string"}},
			"expectedRevision": map[string]any{"type": "string", "description": "Required when replacing an existing skill."},
		}},
	}, Run: recordWorkspaceSkill})
}

func searchWorkspaceSkills(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
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
		if request.DurabilityReason == "" || len(request.FutureTasks) < workspaceSkillMinFutureTasks || len(request.FutureTasks) > workspaceSkillMaxFutureTasks {
			return nil, SafeError{Code: "skill_durability_required", Message: "upsert requires a durabilityReason and two to four distinct futureTasks"}
		}
		if utf8.RuneCountInString(request.DurabilityReason) > workspaceSkillMaxDurabilityReasonRunes {
			return nil, SafeError{Code: "invalid_arguments", Message: "durabilityReason must not exceed 500 characters"}
		}
		for _, task := range request.FutureTasks {
			if utf8.RuneCountInString(task) > workspaceSkillMaxFutureTaskRunes {
				return nil, SafeError{Code: "invalid_arguments", Message: "each futureTasks entry must not exceed 200 characters"}
			}
		}
		if workspaceSkillBodyLooksLikeTaskRecap(request.Body) {
			return nil, SafeError{Code: "skill_not_durable", Message: "skill body reads like a bug or patch recap; rewrite it as generalized reusable guidance or use skip"}
		}
	}
	if ctx.WorkspaceSkills == nil {
		return nil, SafeError{Code: "workspace_skills_unavailable", Message: "workspace skills are not available in this context"}
	}
	return ctx.WorkspaceSkills.RecordWorkspaceSkill(ctx.context(), request)
}

func normalizeWorkspaceSkillFutureTasks(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func workspaceSkillBodyLooksLikeTaskRecap(body string) bool {
	recap := map[string]bool{"bug": true, "the bug": true, "root cause": true, "fix": true, "the fix": true, "changes made": true, "changed files": true, "implementation": true}
	markers := 0
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		heading := strings.TrimSpace(line)
		if !strings.HasPrefix(heading, "#") {
			continue
		}
		heading = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strings.TrimLeft(heading, "#"))), ":")
		if recap[heading] {
			markers++
		}
	}
	return markers >= 2
}
