package debugger

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/brent/echo/internal/debugconfig"
)

// prepareLaunchArguments translates configuration values owned by an editor
// extension into the standard values understood by the underlying adapter.
// vscode-go accepts "auto", but Delve's DAP server only accepts concrete modes.
func prepareLaunchArguments(profile debugconfig.AdapterProfile, request string, arguments map[string]any) {
	if !strings.EqualFold(profile.AdapterID, "go") || !strings.EqualFold(request, "launch") {
		return
	}
	mode, _ := arguments["mode"].(string)
	if !strings.EqualFold(strings.TrimSpace(mode), "auto") {
		return
	}
	program, _ := arguments["program"].(string)
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(program)), "_test.go") {
		arguments["mode"] = "test"
	} else {
		arguments["mode"] = "debug"
	}
}

// launchAdapterWorkingDirectory keeps Delve's build command inside the Go
// module containing the selected program. The launch argument named cwd is
// deliberately left alone because it controls the debuggee's runtime cwd.
func launchAdapterWorkingDirectory(profile debugconfig.AdapterProfile, request string, arguments map[string]any, options debugconfig.ExpandOptions) string {
	if !strings.EqualFold(profile.AdapterID, "go") || !strings.EqualFold(request, "launch") {
		return options.WorkspaceFolder
	}
	mode, _ := arguments["mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" && mode != "debug" && mode != "test" {
		return options.WorkspaceFolder
	}
	program, _ := arguments["program"].(string)
	program = strings.TrimSpace(program)
	if program == "" {
		return options.WorkspaceFolder
	}
	if options.SlashPaths {
		if !path.IsAbs(program) {
			program = path.Join(options.WorkspaceFolder, program)
		}
		if strings.EqualFold(path.Ext(program), ".go") {
			program = path.Dir(program)
		}
		return path.Clean(program)
	}
	if !filepath.IsAbs(program) {
		program = filepath.Join(options.WorkspaceFolder, program)
	}
	if strings.EqualFold(filepath.Ext(program), ".go") {
		program = filepath.Dir(program)
	}
	return filepath.Clean(program)
}
