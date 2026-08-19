package tools

import (
	"encoding/json"
)

func init() {
	Register(ToolFunc{Meta: Metadata{
		Name: "echo_plugin_scaffold", Description: "Create a reviewable Echo plugin source skeleton inside the active workspace. This does not build, install, approve, enable, or run the plugin.",
		Parameters: Schema{"type": "object", "properties": map[string]any{
			"path":     map[string]any{"type": "string", "description": labeledPathSchemaHint + " The destination must be a new or empty directory."},
			"template": map[string]any{"type": "string", "enum": []any{"ui-only", "tool-only", "hybrid"}},
			"id":       map[string]any{"type": "string", "description": "Lowercase kebab-case plugin ID."},
			"name":     map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
		}, "required": []any{"path", "template", "id", "name"}, "additionalProperties": false},
	}, Run: scaffoldEchoPlugin})
	Register(ToolFunc{Meta: Metadata{
		Name: "echo_plugin_validate", Description: "Statically validate an Echo plugin package without running it, reporting compatibility, permissions, contributions, and its content digest.",
		Parameters: pluginPackagePathSchema(),
	}, Run: validateEchoPlugin})
	Register(ToolFunc{Meta: Metadata{
		Name: "echo_plugin_status", Description: "Inspect installed, staged, missing, conflicting, and effective Echo plugin state for the active workspace. Secret values are never returned.",
		Parameters: Schema{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}, Run: echoPluginStatus})
	Register(ToolFunc{Meta: Metadata{
		Name: "echo_plugin_stage", Description: "Snapshot and stage a workspace Echo plugin for owner review. Returns a real pending approval record but cannot approve, enable, execute, or install it.",
		Parameters: pluginPackagePathSchema(),
	}, Run: stageEchoPlugin})
}

func pluginPackagePathSchema() Schema {
	return Schema{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": labeledPathSchemaHint + " The path must identify the plugin package directory."},
	}, "required": []any{"path"}, "additionalProperties": false}
}

func scaffoldEchoPlugin(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if ctx.PluginAuthoring == nil {
		return nil, SafeError{Code: "plugin_authoring_unavailable", Message: "Echo plugin authoring is unavailable"}
	}
	var request PluginScaffoldRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, err
	}
	return ctx.PluginAuthoring.ScaffoldPlugin(ctx.context(), request)
}

func validateEchoPlugin(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if ctx.PluginAuthoring == nil {
		return nil, SafeError{Code: "plugin_authoring_unavailable", Message: "Echo plugin authoring is unavailable"}
	}
	var request PluginPackageRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, err
	}
	return ctx.PluginAuthoring.ValidatePlugin(ctx.context(), request)
}

func echoPluginStatus(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if ctx.PluginAuthoring == nil {
		return nil, SafeError{Code: "plugin_authoring_unavailable", Message: "Echo plugin authoring is unavailable"}
	}
	var request struct{}
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, err
	}
	return ctx.PluginAuthoring.PluginStatus(ctx.context())
}

func stageEchoPlugin(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if ctx.PluginAuthoring == nil {
		return nil, SafeError{Code: "plugin_authoring_unavailable", Message: "Echo plugin authoring is unavailable"}
	}
	var request PluginPackageRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, err
	}
	return ctx.PluginAuthoring.StagePlugin(ctx.context(), request)
}
