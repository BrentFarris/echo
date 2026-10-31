package plugins

import "github.com/brent/echo/internal/domain"

func goPlugin() domain.PluginManifest {
	return domain.PluginManifest{ID: "go", Name: "Go", Settings: Schema(map[string]any{
		"languageServices": B("Enable Go language services (enabled when unset; install gopls explicitly from the editor)"),
		"goplsExecutable":  S("Optional absolute gopls executable path; blank uses the project installation"),
		"executable":       S("Go executable path (blank discovers the installed Go toolchain)"),
		"toolchains":       A("Additional read/execute runtime directories, such as Git or a C compiler"),
		"internet":         B("Grant Internet access to folder-bound Go commands and their children"),
		"privateNetwork":   B("Grant private-network access to folder-bound Go commands and their children"),
		"proxy":            S("Module proxy (blank uses Go's default; unauthenticated proxies only)"),
		"private":          S("GOPRIVATE module patterns (blank uses Go's default)"),
	}), Tools: []domain.Tool{tool("go_command", "Use for Go development: build, test, run, generate, fmt, vet, list, doc, env, mod, work, get, install, tool and version. Runs Go directly, independently of Shell. Select directory inside the bot's folders. Project caches and installed binaries persist under .cache. Downloads require the owner's Go network grant; cached dependencies work offline. Commands and children are cancelled together. go env -w/-u are unavailable: configure Go through plugin settings. Use test with -json for structured test output.", "go.run", map[string]any{
		"command":        S("One Go subcommand, such as build, test, mod or version"),
		"args":           A("Literal arguments; no shell quoting, pipes or redirects"),
		"directory":      S("Working directory; defaults to the first bot folder"),
		"input":          S("Optional standard input"),
		"timeoutSeconds": N("1-300 seconds; defaults to 300"),
	}, "command"), tool("go_inspect", "Inspect saved Go files with gopls: diagnostics, symbols, definition, references or hover. Editor unsaved changes are isolated. This does not replace go build/test. Install gopls explicitly in the editor first. Positions are one-based lines and UTF-16 columns. Results may be incomplete when dependencies are unavailable.", "go.inspect", map[string]any{
		"action":    S("diagnostics, symbols, definition, references or hover"),
		"directory": S("Project working directory inside bot scope"),
		"file":      S("Saved file, relative to directory or absolute inside bot scope"),
		"files":     A("Explicit saved files to diagnose"),
		"query":     S("Workspace symbol query; omit file to search workspace symbols"),
		"line":      N("One-based line"), "column": N("One-based UTF-16 column"),
	}, "action")}}
}
