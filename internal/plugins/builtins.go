package plugins

import (
	"github.com/brent/echo/internal/domain"
	"runtime"
	"slices"
)

func Schema(properties map[string]any, required ...string) map[string]any {
	req := []any{}
	for _, s := range required {
		req = append(req, s)
	}
	return map[string]any{"type": "object", "properties": properties, "required": req, "additionalProperties": false}
}
func S(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func N(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}
func B(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
func A(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}
func tool(name, description, cap string, props map[string]any, required ...string) domain.Tool {
	return domain.Tool{Name: name, Description: description, Capability: cap, Parameters: Schema(props, required...)}
}
func Builtins() []domain.PluginManifest {
	shell := "POSIX /bin/sh"
	if runtime.GOOS == "windows" {
		shell = "Windows PowerShell"
	}
	ms := []domain.PluginManifest{
		{ID: "swarm", Name: "Swarm", Scope: "app", Settings: Schema(map[string]any{"name": S("Machine namespace"), "advertiseUrl": S("Reachable Echo HTTP(S) origin (optional)")})},
		{ID: "web-get", Name: "Web Get", Tools: []domain.Tool{tool("web_get", "Read an HTTP(S) URL. Private network targets require an explicit configured grant.", "web.get", map[string]any{"url": S("URL")}, "url")}, Settings: Schema(map[string]any{"allowedOrigins": A("Additional exact origins allowed to use private network destinations")})},
		{ID: "searxng", Name: "SearXNG Search", Tools: []domain.Tool{tool("web_search", "Search using the configured SearXNG instance.", "web.search", map[string]any{"query": S("Search query")}, "query")}, Settings: Schema(map[string]any{"url": S("SearXNG base URL")})},
		{ID: "markdown", Name: "Markdown View", Renderers: []string{"markdown"}},
		{ID: "workspaces", Name: "Workspaces", Scope: "echo", Tools: []domain.Tool{tool("workspace_bind", "Bind a named bot to these existing folders. An empty array explicitly removes its restrictions.", "workspace.bind", map[string]any{"botId": S("Bot id"), "roots": A("Absolute folders")}, "botId", "roots")}},
		{ID: "shell", Name: "Shell", Tools: []domain.Tool{tool("shell_command", "Run native "+shell+". Working directory must be in the bot's scope. Child processes are cancelled with the command.", "process.run", map[string]any{"command": S(shell + " command"), "directory": S("Working directory"), "timeoutSeconds": N("1-300 seconds")}, "command")}, Settings: Schema(map[string]any{"toolchains": A("Additional read/execute toolchain directories granted to scoped native commands")})},
		{ID: "calendar", Name: "Calendar", Tools: []domain.Tool{
			tool("schedule_list", "List this bot's schedules; Echo can list all.", "calendar.list", map[string]any{}),
			tool("schedule_save", "Create or update scheduled work. Use daily HH:MM, weekly MON HH:MM, every N minutes/hours/days, or an RFC3339 instant. Schedule belongs to the current bot unless Echo specifies botId.", "calendar.save", map[string]any{"id": S("Existing schedule id, otherwise new"), "botId": S("Bot id"), "prompt": S("Task to perform"), "recurrence": S("Recurrence"), "timezone": S("IANA timezone"), "paused": B("Pause this schedule")}, "prompt", "recurrence"),
			tool("schedule_delete", "Delete a schedule.", "calendar.delete", map[string]any{"id": S("Schedule id")}, "id"),
			tool("schedule_missed", "List missed jobs requiring the user's selection.", "calendar.missed", map[string]any{}),
		}, Settings: Schema(map[string]any{"timezone": S("Default IANA timezone")})},
		{ID: "hosted-server", Name: "Hosted Server", Scope: "app", Settings: Schema(map[string]any{"remote": B("Allow remote connections; takes effect on restart"), "port": N("Server port"), "certificate": S("Optional TLS certificate PEM path"), "key": S("Optional TLS private key PEM path")})},
		{ID: "plugin-authoring", Name: "Plugin Authoring", Scope: "echo", Tools: []domain.Tool{
			tool("plugin_template", "Get a working UI, renderer, model provider, or Go/WASI backend template and the plugin SDK. Use before authoring a plugin.", "plugin.template", map[string]any{"kind": S("ui, backend, provider, or renderer")}, "kind"),
			tool("plugin_list", "List plugins and their configuration.", "plugin.list", map[string]any{}),
			tool("plugin_install", "Install a UI plugin, or a Go/WASI backend compiled from backendSource. HTML-only installs register an app in Apps; full manifests opt in with app.viewId and an optional image data URL app.icon. HTML runs in an isolated iframe; no scripts are run during installation. UI and backend network access is supported: declare exact HTTP(S)/WS(S) network.origins in the full manifest and web.request or websocket.open/send/receive/close capabilities. HTTP(S) origins also permit remote assets. Use plugin_template for bridge examples. New privileged capabilities or network origins require owner approval.", "plugin.install", map[string]any{"id": S("Lowercase kebab-case id"), "name": S("Plugin name"), "html": S("Self-contained HTML/CSS/JavaScript for a draggable window"), "backendSource": S("Optional complete Go/WASI source using the provided SDK"), "manifest": map[string]any{"type": "object", "description": "Optional complete API 1 manifest, including app registration, views, or backend tools"}}, "id", "name"),
			tool("plugin_configure", "Change enabled state or non-secret settings of a plugin.", "plugin.configure", map[string]any{"id": S("Plugin id"), "enabled": B("Enabled"), "config": map[string]any{"type": "object"}}, "id"),
			tool("plugin_open", "Open or focus a plugin app in its embedded or floating presentation. Defaults to its registered app view, then its first eligible view; renderer views cannot be opened.", "plugin.open", map[string]any{"id": S("Plugin id"), "viewId": S("Optional embedded or floating view id")}, "id"),
			tool("plugin_rollback", "Restore the previous installed version.", "plugin.rollback", map[string]any{"id": S("Plugin id")}, "id"),
		}},
	}
	ms = append(ms, sourceControlPlugins()...)
	ms = append(ms, comfyUIPlugin())
	ms = append(ms, confluencePlugin())
	ms = append(ms, jiraPlugin())
	ms = append(ms, monacoEditorPlugin(), terminalPlugin())
	for i := range ms {
		m := &ms[i]
		m.API = 1
		m.Version = "1.0.0"
		m.Builtin = true
		m.Enabled = m.ID != "swarm" && m.ID != "jira"
		if m.Scope == "" {
			m.Scope = "bot"
		}
		m.Config = map[string]any{}
		if m.Settings == nil {
			m.Settings = Schema(map[string]any{})
		}
		for _, t := range m.Tools {
			if !slices.Contains(m.Capabilities, t.Capability) {
				m.Capabilities = append(m.Capabilities, t.Capability)
			}
		}
	}
	return ms
}
