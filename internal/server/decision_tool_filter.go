// Decision-model tool filtering (Phase 4). When the DecisionModelToolFiltering
// setting is enabled and a decision-model endpoint is configured, Echo asks
// the decision model which tool capability categories the user's prompt
// actually needs and removes the rest from the tool schema before the request
// is sent. This keeps large, irrelevant tool definitions out of the context
// window without changing what the agent mode permits: execution-time scope
// checks are untouched, and a model that still calls a filtered tool gets a
// soft per-turn error instead of a hard failure.
//
// Everything lives in this file so the chat pipeline only sees a single
// classification call plus two pure helpers. The feature fails open in every
// failure path: disabled, unconfigured endpoint, timeout, endpoint error, or a
// missing answer all leave the full schema in place.
package server

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

const (
	// decisionToolFilterTimeout bounds the per-prompt filtering query so a
	// slow or hung decision model never delays the chat turn noticeably.
	// Fail-open: on timeout the unfiltered schema is kept.
	decisionToolFilterTimeout = 2 * time.Second
	// decisionToolFilterStateLimit caps the prompt text sent as state so the
	// decision-model call stays cheap even for very long prompts.
	decisionToolFilterStateLimit = 2000
	// decisionToolFilterKeepThreshold is the minimum noul probability that a
	// category needs to stay available. Below it the category is filtered out
	// for the turn; a missing answer keeps the category (fail-open).
	decisionToolFilterKeepThreshold = 0.5
)

// decisionToolCategories maps every classifiable core tool name to a capability
// category. Tools that are absent here (and anything registered later, such as
// plugin tools) are never filtered — an unknown tool must stay available.
var decisionToolCategories = map[string]string{
	// Reading and searching workspace files.
	"filesystem_list":             "filesystem_read",
	"filesystem_read_text":        "filesystem_read",
	"filesystem_read_image":       "filesystem_read",
	"filesystem_read_video":       "filesystem_read",
	"filesystem_stat":             "filesystem_read",
	"filesystem_search_text":      "filesystem_read",
	"filesystem_search_workspace": "filesystem_read",
	// Mutating workspace files.
	"filesystem_create_text": "filesystem_write",
	"filesystem_edit_text":   "filesystem_write",
	"filesystem_delete_file": "filesystem_write",
	"filesystem_move":        "filesystem_write",
	// Process execution.
	"shell_command": "shell",
	"restart":       "shell",
	// Source control inspection.
	"git_inspect":              "source_control",
	"fossil_inspect":           "source_control",
	tools.SourceControlInspectToolName: "source_control",
	// Web access.
	"web_search": "web",
	"web_fetch":  "web",
	// Research-agent orchestration.
	tools.ResearchAgentsSpawnToolName:  "research",
	tools.ResearchAgentSendToolName:    "research",
	tools.ResearchAgentsWaitToolName:   "research",
	tools.ResearchAgentsCancelToolName: "research",
	// Media generation.
	"comfyui_generate":       "media",
	"comfyui_generate_video": "media",
	"save_image":             "media",
	"save_video":             "media",
	// Sandboxed GUI automation (canonical and legacy names).
	"ui_observe":       "gui",
	"ui_act":           "gui",
	"ui_verify":        "gui",
	"ui_locate":        "gui",
	"desktop_control":  "gui",
	"browser_open":     "gui",
	"browser_snapshot": "gui",
	"browser_click":    "gui",
	"browser_type":     "gui",
	"browser_select":   "gui",
	"browser_press":    "gui",
	"browser_scroll":   "gui",
	"browser_tabs":     "gui",
	"browser_wait":     "gui",
	// Echo plugin authoring.
	"echo_plugin_scaffold": "plugins",
	"echo_plugin_validate": "plugins",
	"echo_plugin_status":   "plugins",
	"echo_plugin_stage":    "plugins",
	// Workspace skills.
	"workspace_skill_search": "skills",
	"workspace_skill_read":   "skills",
	"workspace_skill_record": "skills",
	// Agent-mode authoring.
	"create_agent_mode": "agent_modes",
}

// decisionToolFilterAlwaysKeep lists tools that are never filtered out. They
// are either part of the conversation protocol itself (clarifying questions,
// goal status) or the escape hatch back to the decision model.
var decisionToolFilterAlwaysKeep = map[string]bool{
	tools.AskUserQuestionsToolName: true,
	tools.UpdateGoalToolName:       true,
	tools.DecisionModelToolName:    true,
}

// decisionToolCategoryDescriptions describes each category for the decision
// model. The wording doubles as the noul question instructions.
var decisionToolCategoryDescriptions = map[string]string{
	"filesystem_read":  "reading, listing, or searching files in the workspace",
	"filesystem_write": "creating, editing, moving, or deleting files in the workspace",
	"shell":            "running shell commands or restarting the application",
	"source_control":   "inspecting git or other source-control status, history, diffs, or annotations",
	"web":              "searching the web or fetching web pages",
	"research":         "spawning or coordinating parallel research agents",
	"media":            "generating or saving images and videos",
	"gui":              "observing or controlling a sandboxed browser or desktop GUI",
	"plugins":          "scaffolding, validating, inspecting, or staging Echo plugins",
	"skills":           "searching, reading, or recording reusable workspace skills",
	"agent_modes":      "creating custom agent modes",
}

// decisionModelToolFilter asks the decision model which tool capability
// categories this prompt needs. The first return maps category -> kept; the
// second is false when filtering should not apply (feature disabled, endpoint
// unconfigured, call failed, or no classifiable tools in the schema) — callers
// then keep the schema untouched.
func (s *Server) decisionModelToolFilter(settings llm.Settings, prompt string, schema []llm.Tool) (map[string]bool, bool) {
	if s == nil || s.decisionModel == nil || !s.decisionModel.Enabled() || !settings.DecisionModelToolFiltering {
		return nil, false
	}
	present := decisionToolPresentCategories(schema)
	if len(present) == 0 {
		// Nothing classifiable to remove; asking would be pure overhead.
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), decisionToolFilterTimeout)
	defer cancel()
	builder := s.decisionModel.Ask(ctx, decisionToolFilterState(prompt))
	for _, category := range present {
		builder = builder.Noul("needs_"+category, "Does completing this prompt require "+decisionToolCategoryDescriptions[category]+"?")
	}
	response, err := builder.Do()
	if err != nil {
		return nil, false
	}
	kept := make(map[string]bool, len(present))
	for _, category := range present {
		answer, ok := response.Answer("needs_" + category)
		// Fail open: a missing or malformed answer keeps the category.
		kept[category] = !ok || answer.Noul >= decisionToolFilterKeepThreshold
	}
	return kept, true
}

// decisionToolPresentCategories returns the sorted set of distinct categories
// represented in the schema, excluding always-kept and unclassifiable tools.
func decisionToolPresentCategories(schema []llm.Tool) []string {
	seen := map[string]bool{}
	for _, tool := range schema {
		name := tool.Function.Name
		if decisionToolFilterAlwaysKeep[name] {
			continue
		}
		if category := decisionToolCategories[name]; category != "" {
			seen[category] = true
		}
	}
	present := make([]string, 0, len(seen))
	for category := range seen {
		present = append(present, category)
	}
	sort.Strings(present)
	return present
}

// filterToolSchemaByCategories returns the subset of schema whose tools belong
// to a kept category. Always-kept and unclassifiable tools survive. A nil kept
// set means filtering is off and the schema is returned unchanged.
func filterToolSchemaByCategories(schema []llm.Tool, kept map[string]bool) []llm.Tool {
	if kept == nil {
		return schema
	}
	filtered := make([]llm.Tool, 0, len(schema))
	for _, tool := range schema {
		name := tool.Function.Name
		if decisionToolFilterAlwaysKeep[name] {
			filtered = append(filtered, tool)
			continue
		}
		category := decisionToolCategories[name]
		if category == "" || kept[category] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// toolFilteredForTurn reports whether a tool call targets a category the
// decision model filtered out for this turn. Filtered calls are answered with
// a soft error so the model can adapt instead of failing the turn.
func toolFilteredForTurn(name string, kept map[string]bool) bool {
	if kept == nil || decisionToolFilterAlwaysKeep[name] {
		return false
	}
	category := decisionToolCategories[name]
	return category != "" && !kept[category]
}

// filteredOutCategories lists the sorted categories present in the schema but
// not kept, for trajectory visibility.
func filteredOutCategories(schema []llm.Tool, kept map[string]bool) []string {
	if kept == nil {
		return nil
	}
	var removed []string
	for _, category := range decisionToolPresentCategories(schema) {
		if !kept[category] {
			removed = append(removed, category)
		}
	}
	return removed
}

// keptCategoryNames returns the sorted names of the kept categories, for
// trajectory and event visibility.
func keptCategoryNames(kept map[string]bool) []string {
	names := make([]string, 0, len(kept))
	for category, isKept := range kept {
		if isKept {
			names = append(names, category)
		}
	}
	sort.Strings(names)
	return names
}

// decisionToolFilterState builds the compact state string sent to the decision
// model. Only the user's prompt is relevant to the filtering decision, and it
// is truncated by runes to keep the call cheap.
func decisionToolFilterState(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if runes := []rune(prompt); len(runes) > decisionToolFilterStateLimit {
		prompt = string(runes[:decisionToolFilterStateLimit])
	}
	return "User prompt:\n" + prompt
}
