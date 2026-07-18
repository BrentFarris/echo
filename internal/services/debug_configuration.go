package services

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const debugVariableExpansionLimit = 10

var debugVariablePattern = regexp.MustCompile(`\$\{([^{}]+)\}`)

type debugVariableContext struct {
	workspace   Workspace
	firstFolder string
	currentFile string
}

func prepareDebugConfiguration(workspace Workspace, raw map[string]any, currentFile string) (map[string]any, error) {
	if len(workspace.Folders) == 0 {
		return nil, fmt.Errorf("workspace has no folders")
	}
	firstFolder, err := workspaceFolderAbsolutePath(workspace.Folders[0])
	if err != nil || workspace.Folders[0].Missing {
		return nil, fmt.Errorf("the first workspace folder is unavailable")
	}
	resolvedFile := ""
	if strings.TrimSpace(currentFile) != "" {
		resolvedFile, err = resolveDebugWorkspacePath(workspace, currentFile, firstFolder)
		if err != nil {
			// External/untitled editor tabs are not debug launch inputs. If the
			// configuration actually references ${file}, expansion below reports
			// the more useful "requires an open workspace file" error.
			resolvedFile = ""
		}
	}
	ctx := debugVariableContext{workspace: workspace, firstFolder: firstFolder, currentFile: resolvedFile}
	expanded, err := expandDebugValue(ctx, cloneDebugMap(raw), 0)
	if err != nil {
		return nil, err
	}
	config, ok := expanded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("debug configuration must be an object")
	}

	adapterType := debugString(config, "type")
	if adapterType == "" {
		return nil, fmt.Errorf("debug configuration type is required")
	}
	if request := strings.ToLower(debugString(config, "request")); request != "launch" {
		if request == "" {
			return nil, fmt.Errorf("debug configuration request is required")
		}
		return nil, fmt.Errorf("debug request %q is not supported yet", request)
	}
	if strings.EqualFold(adapterType, "go") {
		mode := strings.ToLower(debugString(config, "mode"))
		if mode == "" {
			mode = "debug"
			config["mode"] = mode
		}
		if mode != "debug" && mode != "test" && mode != "exec" {
			return nil, fmt.Errorf("unsupported Go debug mode %q", mode)
		}
	}

	base := firstFolder
	if cwd := debugString(config, "cwd"); cwd != "" {
		cwd, err = resolveDebugWorkspacePath(workspace, cwd, firstFolder)
		if err != nil {
			return nil, fmt.Errorf("debug cwd: %w", err)
		}
		config["cwd"] = cwd
		base = cwd
	} else {
		config["cwd"] = firstFolder
	}
	for _, field := range []string{"program", "dlvCwd", "output"} {
		value := debugString(config, field)
		if value == "" {
			continue
		}
		resolved, err := resolveDebugWorkspacePath(workspace, value, base)
		if err != nil {
			return nil, fmt.Errorf("debug %s: %w", field, err)
		}
		config[field] = resolved
	}
	if debugString(config, "program") == "" {
		return nil, fmt.Errorf("debug program is required")
	}
	if strings.EqualFold(adapterType, "go") && debugString(config, "mode") != "exec" && debugString(config, "dlvCwd") == "" {
		if moduleRoot := findGoDebugModuleRoot(debugString(config, "program"), debugString(config, "cwd"), workspace); moduleRoot != "" {
			// cwd is the working directory of the debugged program. Delve itself
			// must start inside the module that owns program so `go build` can
			// resolve go.mod/go.work, even when cwd is a repository parent.
			config["dlvCwd"] = moduleRoot
		}
	}

	// Executable selection belongs exclusively to the adapter registry. Ignore
	// common command override spellings instead of allowing workspace JSON to
	// execute an arbitrary debugger.
	for _, key := range []string{"adapter", "adapterCommand", "debugAdapter", "dlvPath", "dlvToolPath", "host", "port"} {
		delete(config, key)
	}
	return config, nil
}

func findGoDebugModuleRoot(program string, cwd string, workspace Workspace) string {
	starts := []string{program, cwd}
	for _, start := range starts {
		start = strings.TrimSpace(start)
		if start == "" {
			continue
		}
		if info, err := os.Stat(start); err == nil && !info.IsDir() {
			start = filepath.Dir(start)
		} else if err != nil && filepath.Ext(start) != "" {
			start = filepath.Dir(start)
		}
		for directory := filepath.Clean(start); debugPathWithinWorkspace(workspace, directory); directory = filepath.Dir(directory) {
			for _, marker := range []string{"go.work", "go.mod"} {
				if info, err := os.Stat(filepath.Join(directory, marker)); err == nil && info.Mode().IsRegular() {
					return directory
				}
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	return ""
}

func expandDebugValue(ctx debugVariableContext, value any, depth int) (any, error) {
	if depth > debugVariableExpansionLimit {
		return nil, fmt.Errorf("debug variable expansion exceeded %d passes", debugVariableExpansionLimit)
	}
	switch typed := value.(type) {
	case string:
		return expandDebugString(ctx, typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			value, err := expandDebugValue(ctx, typed[i], depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			value, err := expandDebugValue(ctx, child, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, nil
	default:
		return value, nil
	}
}

func expandDebugString(ctx debugVariableContext, input string) (string, error) {
	value := input
	for pass := 0; pass < debugVariableExpansionLimit; pass++ {
		matches := debugVariablePattern.FindAllStringSubmatchIndex(value, -1)
		if len(matches) == 0 {
			return value, nil
		}
		var result strings.Builder
		last := 0
		for _, match := range matches {
			result.WriteString(value[last:match[0]])
			name := value[match[2]:match[3]]
			replacement, err := debugVariableValue(ctx, name)
			if err != nil {
				return "", err
			}
			result.WriteString(replacement)
			last = match[1]
		}
		result.WriteString(value[last:])
		next := result.String()
		if next == value {
			return "", fmt.Errorf("debug variable expansion is recursive in %q", input)
		}
		value = next
	}
	if debugVariablePattern.MatchString(value) {
		return "", fmt.Errorf("debug variable expansion exceeded %d passes", debugVariableExpansionLimit)
	}
	return value, nil
}

func debugVariableValue(ctx debugVariableContext, variable string) (string, error) {
	variable = strings.TrimSpace(variable)
	switch variable {
	case "workspaceFolder":
		return ctx.firstFolder, nil
	case "file":
		if ctx.currentFile == "" {
			return "", fmt.Errorf("${file} requires an open workspace file")
		}
		return ctx.currentFile, nil
	case "fileBasename":
		if ctx.currentFile == "" {
			return "", fmt.Errorf("${fileBasename} requires an open workspace file")
		}
		return filepath.Base(ctx.currentFile), nil
	case "fileBasenameNoExtension":
		if ctx.currentFile == "" {
			return "", fmt.Errorf("${fileBasenameNoExtension} requires an open workspace file")
		}
		base := filepath.Base(ctx.currentFile)
		return strings.TrimSuffix(base, filepath.Ext(base)), nil
	case "fileDirname":
		if ctx.currentFile == "" {
			return "", fmt.Errorf("${fileDirname} requires an open workspace file")
		}
		return filepath.Dir(ctx.currentFile), nil
	case "fileExtname":
		if ctx.currentFile == "" {
			return "", fmt.Errorf("${fileExtname} requires an open workspace file")
		}
		return filepath.Ext(ctx.currentFile), nil
	case "pathSeparator":
		return string(filepath.Separator), nil
	}
	if strings.HasPrefix(variable, "workspaceFolder:") {
		label := strings.TrimSpace(strings.TrimPrefix(variable, "workspaceFolder:"))
		folder, ok := workspaceFolderByLabel(ctx.workspace, label)
		if !ok || folder.Missing {
			return "", fmt.Errorf("workspace folder %q is unavailable", label)
		}
		return workspaceFolderAbsolutePath(folder)
	}
	if strings.HasPrefix(variable, "env:") {
		return os.Getenv(strings.TrimSpace(strings.TrimPrefix(variable, "env:"))), nil
	}
	if strings.HasPrefix(variable, "command:") || strings.HasPrefix(variable, "input:") {
		return "", fmt.Errorf("debug variable ${%s} is not supported", variable)
	}
	return "", fmt.Errorf("unknown debug variable ${%s}", variable)
}

func resolveDebugWorkspacePath(workspace Workspace, input string, base string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	var candidate string
	if filepath.IsAbs(value) {
		candidate = filepath.Clean(value)
	} else if label, _ := splitWorkspaceLabeledPath(value); label != "" {
		if _, ok := workspaceFolderByLabel(workspace, label); ok {
			resolved, err := resolveWorkspaceServicePath(workspace, value)
			if err != nil {
				// Nonexistent output files cannot be resolved by EvalSymlinks in the
				// regular editor helper, so fall through to lexical resolution.
				folder, _ := workspaceFolderByLabel(workspace, label)
				root, rootErr := workspaceFolderAbsolutePath(folder)
				if rootErr != nil {
					return "", err
				}
				_, relative := splitWorkspaceLabeledPath(value)
				candidate = filepath.Join(root, relative)
			} else {
				candidate = resolved
			}
		} else {
			candidate = filepath.Join(base, filepath.FromSlash(value))
		}
	} else {
		candidate = filepath.Join(base, filepath.FromSlash(value))
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	if !debugPathWithinWorkspace(workspace, candidate) {
		return "", fmt.Errorf("path is outside the workspace")
	}
	return filepath.Clean(candidate), nil
}

func debugPathWithinWorkspace(workspace Workspace, candidate string) bool {
	realCandidate := debugRealPath(candidate)
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		root, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}
		root = debugRealPath(root)
		relative, err := filepath.Rel(root, realCandidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

// debugRealPath resolves symlinks for the longest existing ancestor. This
// keeps a not-yet-created output path from escaping through a symlinked parent.
func debugRealPath(path string) string {
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func cloneDebugMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func debugString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return strings.TrimSpace(value)
}
