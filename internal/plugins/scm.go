package plugins

import "github.com/brent/echo/internal/domain"

func sourceControlPlugins() []domain.PluginManifest {
	var out []domain.PluginManifest
	for _, spec := range []struct{ id, name, description string }{
		{"git", "Git", "Git repositories: status, diff, log, add, commit, branch, fetch, pull and push."},
		{"fossil", "Fossil", "Fossil repositories: status, diff, timeline, add, commit, branch and sync. The repository database must also be inside the bot's folders."},
		{"p4", "P4 (Perforce)", "Perforce workspaces: info, opened, status, diff, sync, edit, reconcile, changelists and submit. Configure server, user and client in P4 settings."},
	} {
		properties := map[string]any{"executable": S("CLI executable path (blank discovers it on PATH)"), "toolchains": A("Additional read/execute runtime folders"), "internet": B("Grant Internet access to this plugin's workspace-bound commands"), "privateNetwork": B("Grant private-network connections/listeners to this plugin's workspace-bound commands")}
		switch spec.id {
		case "git":
			properties["userName"] = S("Commit author name")
			properties["userEmail"] = S("Commit author email")
		case "fossil":
			properties["user"] = S("Fossil user name")
		case "p4":
			properties["port"] = S("P4 server, for example ssl:perforce.example.com:1666")
			properties["user"] = S("P4 user")
			properties["client"] = S("P4 client workspace")
			properties["charset"] = S("Optional P4 character set")
			properties["trustFile"] = S("Optional P4 trust file inside the bot's folders")
		}
		args := map[string]any{"command": S("CLI subcommand, such as status, diff, log, help or version"), "args": A("Arguments as separate strings; no shell quoting, pipes or redirects"), "directory": S("Working directory; defaults to the first bot folder"), "input": S("Optional standard input, for example a Perforce form with -i or a Git patch"), "timeoutSeconds": N("1-300 seconds; defaults to 60")}
		description := spec.description + " Runs the installed CLI directly with streamed output and workspace containment, independently of Shell. Use version to check installation. Interactive editors/prompts are unavailable; supply messages/forms explicitly. Remote access from bound bots needs the owner's network grant. Never put passwords or tickets in tool arguments."
		m := domain.PluginManifest{ID: spec.id, Name: spec.name, Settings: Schema(properties), Tools: []domain.Tool{tool(spec.id+"_command", description, spec.id+".run", args, "command")}}
		for _, command := range []string{"status", "diff"} {
			m.Tools = append(m.Tools, tool(spec.id+"_"+command, "Run "+spec.name+" "+command+" in a workspace. Output is streamed; include paths/options in args.", spec.id+".run", map[string]any{"args": A("Additional subcommand arguments"), "directory": S("Working directory"), "timeoutSeconds": N("1-300 seconds")}))
		}
		out = append(out, m)
	}
	return out
}
