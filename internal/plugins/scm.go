package plugins

import "github.com/brent/echo/internal/domain"

func sourceControlPlugins() []domain.PluginManifest {
	var out []domain.PluginManifest
	for _, spec := range []struct{ id, name, description string }{
		{"git", "Git", "Git repositories: status, diff, log, add, commit, branch, fetch, pull and push. Enable Search Parent Repositories in Source Control when bot folders are inside a shared checkout. These Git tools then have read/write access to the entire discovered repository, including sibling folders and the shared index. Arguments and working directories retain native Git semantics; use Git pathspecs to select your intended files. Commits can include all staged changes in that repository. An omitted directory uses the first bot folder; relative directories are resolved against that folder. The repository root and its other subdirectories can also be used."},
		{"fossil", "Fossil", "Fossil repositories: status, diff, timeline, add, commit, branch and sync. Run from the checkout or a subdirectory. The checkout's linked repository database may also live in its immediate parent folder; other locations must be inside the bot's folders."},
		{"p4", "P4 (Perforce)", "Perforce workspaces: info, clients, client, opened, status, diff, sync, edit, reopen, reconcile, changelists and submit. Configure server/user in P4 settings. Select a workspace with the client parameter on any P4 tool; it overrides the saved client for that call. Use clients to discover workspaces and client with args [-o, workspace] to inspect their mappings. Use edit to check out files before modifying them; edit/reopen args -c selects a changelist, not a workspace."},
	} {
		properties := map[string]any{"executable": S("CLI executable path (blank discovers it on PATH)"), "toolchains": A("Additional read/execute runtime folders"), "internet": B("Grant Internet access to this plugin's workspace-bound commands"), "privateNetwork": B("Grant private-network connections/listeners to this plugin's workspace-bound commands")}
		switch spec.id {
		case "git":
			properties["userName"] = S("Commit author name")
			properties["userEmail"] = S("Commit author email")
			properties["hostStatus"] = B("Windows: run Git status with your account's full permissions to avoid sandbox setup. Repository helpers also receive your filesystem and network access.")
		case "fossil":
			properties["user"] = S("Fossil user name")
		case "p4":
			properties["port"] = S("P4 server, for example ssl:perforce.example.com:1666")
			properties["user"] = S("P4 user")
			properties["client"] = S("P4 client workspace")
			properties["charset"] = S("Optional P4 character set")
			properties["trustFile"] = S("Optional P4 trust file inside the bot's folders")
		}
		args := map[string]any{"command": S("CLI subcommand, such as status, diff, log, help or version"), "args": A("Arguments as separate strings; no shell quoting, pipes or redirects"), "directory": S("Working directory; defaults to the first bot folder"), "input": S("Optional standard input, for example a Perforce form with -i or a Git patch"), "timeoutSeconds": N("Optional timeout in seconds; omitted or 0 runs until exit or cancellation")}
		if spec.id == "p4" {
			args["client"] = S("P4 workspace for this call; passed as global -c before the subcommand. Blank uses the saved P4 client. Does not change saved settings or P4CLIENT. To save a default, Echo can use plugin_configure with id p4 and config.client.")
		}
		description := spec.description + " Runs the installed CLI directly with streamed output and workspace containment, independently of Shell. Use version to check installation. Interactive editors/prompts are unavailable; supply messages/forms explicitly. Remote access from bound bots needs the owner's network grant. Never put passwords or tickets in tool arguments."
		m := domain.PluginManifest{ID: spec.id, Name: spec.name, Settings: Schema(properties), Tools: []domain.Tool{tool(spec.id+"_command", description, spec.id+".run", args, "command")}}
		for _, command := range []string{"status", "diff"} {
			params := map[string]any{"args": A("Additional subcommand arguments"), "directory": S("Working directory"), "timeoutSeconds": N("Optional timeout in seconds; omitted or 0 runs until exit or cancellation")}
			if spec.id == "p4" {
				params["client"] = args["client"]
			}
			description := "Run " + spec.name + " " + command + " in a workspace. Output is streamed; include paths/options in args."
			if spec.id == "git" {
				description += " Honors Source Control's Search Parent Repositories setting and has access to the entire discovered checkout. Arguments and paths follow native Git semantics. Use pathspecs to select files; relative directories are resolved against the first bot folder."
			}
			m.Tools = append(m.Tools, tool(spec.id+"_"+command, description, spec.id+".run", params))
		}
		out = append(out, m)
	}
	return out
}
