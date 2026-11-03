package domain

import "encoding/json"

// SlashCommand is an owner-created shortcut, not a model instruction or tool.
type SlashCommand struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Builtin     bool               `json:"builtin"`
	Enabled     bool               `json:"enabled"`
	Action      SlashCommandAction `json:"action"`
}

type SlashCommandAction struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Position  string          `json:"position,omitempty"`
	PluginID  string          `json:"pluginId,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	ViewID    string          `json:"viewId,omitempty"`
	Feature   string          `json:"feature,omitempty"`
	BotID     string          `json:"botId,omitempty"`
}

const PlanCommandText = "\n\n You are to plan this request out, do not make any changes until the plan is approved"

func BuiltinSlashCommands() []SlashCommand {
	return []SlashCommand{{ID: "plan", Name: "plan", Description: "Append an instruction to plan before making changes", Builtin: true, Enabled: true,
		Action: SlashCommandAction{Type: "text", Position: "append", Text: PlanCommandText}}}
}
