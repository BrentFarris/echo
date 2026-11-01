package debugconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var variablePattern = regexp.MustCompile(`\$\{([^{}]+)\}`)

type ExpandOptions struct {
	Settings         map[string]any
	Commands         map[string]string
	WorkspaceFolder  string
	WorkspaceFolders map[string]string
	CurrentFile      string
	SelectedText     string
	Inputs           map[string]string
	Environment      map[string]string
	DebugAdapterPort int
	// SlashPaths selects POSIX path operations when values are expanded for a
	// Linux sandbox from a Windows host. filepath follows the Echo host and
	// would otherwise emit backslashes into guest launch arguments.
	SlashPaths bool
	// PreserveDebugAdapterPort leaves the placeholder for a sandbox agent that
	// allocates the guest-only loopback port after the request crosses hosts.
	PreserveDebugAdapterPort bool
}

func ExpandValue(value any, options ExpandOptions) (any, error) {
	switch current := value.(type) {
	case string:
		return ExpandString(current, options)
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			expanded, err := ExpandValue(item, options)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			expanded, err := ExpandValue(item, options)
			if err != nil {
				return nil, err
			}
			result[key] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func ExpandStrings(values []string, options ExpandOptions) ([]string, error) {
	result := make([]string, len(values))
	for index, value := range values {
		expanded, err := ExpandString(value, options)
		if err != nil {
			return nil, err
		}
		result[index] = expanded
	}
	return result, nil
}

func ExpandString(value string, options ExpandOptions) (string, error) {
	var expansionErr error
	result := variablePattern.ReplaceAllStringFunc(value, func(match string) string {
		if expansionErr != nil {
			return match
		}
		key := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		replacement, err := expandVariable(key, options)
		if err != nil {
			expansionErr = err
			return match
		}
		return replacement
	})
	if expansionErr != nil {
		return "", expansionErr
	}
	return result, nil
}

func expandVariable(key string, options ExpandOptions) (string, error) {
	file := options.CurrentFile
	switch key {
	case "workspaceFolderBasename":
		return filepath.Base(options.WorkspaceFolder), nil
	case "fileWorkspaceFolder", "fileWorkspaceFolderBasename":
		if file == "" {
			return "", fmt.Errorf("${%s} requires an active saved file", key)
		}
		folder := ""
		for _, candidate := range options.WorkspaceFolders {
			relative, err := filepath.Rel(candidate, file)
			if candidate != "" && err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative) && len(candidate) > len(folder) {
				folder = candidate
			}
		}
		if folder == "" {
			folder = options.WorkspaceFolder
		}
		if key == "fileWorkspaceFolderBasename" {
			return filepath.Base(folder), nil
		}
		return folder, nil
	case "fileDirnameBasename":
		if file == "" {
			return "", fmt.Errorf("${%s} requires an active saved file", key)
		}
		return filepath.Base(filepath.Dir(file)), nil
	case "userHome":
		return os.UserHomeDir()
	case "relativeFileDirname":
		if file == "" {
			return "", fmt.Errorf("${relativeFileDirname} requires an active saved file")
		}
		relative, err := filepath.Rel(options.WorkspaceFolder, filepath.Dir(file))
		return relative, err
	case "workspaceFolder", "cwd":
		return options.WorkspaceFolder, nil
	case "file":
		if file == "" {
			return "", fmt.Errorf("${file} requires an active saved file")
		}
		return file, nil
	case "fileDirname":
		if file == "" {
			return "", fmt.Errorf("${fileDirname} requires an active saved file")
		}
		if options.SlashPaths {
			return path.Dir(file), nil
		}
		return filepath.Dir(file), nil
	case "fileBasename":
		if file == "" {
			return "", fmt.Errorf("${fileBasename} requires an active saved file")
		}
		if options.SlashPaths {
			return path.Base(file), nil
		}
		return filepath.Base(file), nil
	case "fileBasenameNoExtension":
		if file == "" {
			return "", fmt.Errorf("${fileBasenameNoExtension} requires an active saved file")
		}
		if options.SlashPaths {
			base := path.Base(file)
			return strings.TrimSuffix(base, path.Ext(base)), nil
		}
		base := filepath.Base(file)
		return strings.TrimSuffix(base, filepath.Ext(base)), nil
	case "fileExtname":
		if file == "" {
			return "", fmt.Errorf("${fileExtname} requires an active saved file")
		}
		if options.SlashPaths {
			return path.Ext(file), nil
		}
		return filepath.Ext(file), nil
	case "relativeFile":
		if file == "" {
			return "", fmt.Errorf("${relativeFile} requires an active saved file")
		}
		var relative string
		var err error
		if options.SlashPaths {
			relative, err = filepath.Rel(filepath.FromSlash(options.WorkspaceFolder), filepath.FromSlash(file))
			relative = filepath.ToSlash(relative)
		} else {
			relative, err = filepath.Rel(options.WorkspaceFolder, file)
		}
		if err != nil {
			return "", err
		}
		return relative, nil
	case "pathSeparator", "/":
		if options.SlashPaths {
			return "/", nil
		}
		return string(os.PathSeparator), nil
	case "selectedText":
		return options.SelectedText, nil
	case "debugAdapterPort":
		if options.DebugAdapterPort < 1 {
			if options.PreserveDebugAdapterPort {
				return "${debugAdapterPort}", nil
			}
			return "", fmt.Errorf("${debugAdapterPort} is only available to server adapters")
		}
		return fmt.Sprint(options.DebugAdapterPort), nil
	}
	if strings.HasPrefix(key, "workspaceFolder:") {
		name := strings.TrimSpace(strings.TrimPrefix(key, "workspaceFolder:"))
		if value := options.WorkspaceFolders[name]; value != "" {
			return value, nil
		}
		return "", fmt.Errorf("workspace folder %q was not found", name)
	}
	if strings.HasPrefix(key, "input:") {
		id := strings.TrimSpace(strings.TrimPrefix(key, "input:"))
		value, ok := options.Inputs[id]
		if !ok {
			return "", fmt.Errorf("debug input %q was not supplied", id)
		}
		return value, nil
	}
	if strings.HasPrefix(key, "env:") {
		name := strings.TrimSpace(strings.TrimPrefix(key, "env:"))
		if options.Environment != nil {
			if value, ok := options.Environment[name]; ok {
				return value, nil
			}
		}
		return os.Getenv(name), nil
	}
	if strings.HasPrefix(key, "command:") {
		if value, ok := options.Commands[strings.TrimPrefix(key, "command:")]; ok {
			return value, nil
		}
		return "", fmt.Errorf("unsupported debug command variable ${%s}", key)
	}
	if strings.HasPrefix(key, "config:") {
		name := strings.TrimPrefix(key, "config:")
		if value, ok := configurationValue(options.Settings, name); ok {
			switch v := value.(type) {
			case string:
				return v, nil
			case bool, float64, json.Number:
				return fmt.Sprint(v), nil
			}
		}
		return "", fmt.Errorf("configuration variable %q is missing or not scalar", name)
	}
	return "", fmt.Errorf("unsupported debug variable ${%s}", key)
}

// VS Code setting keys may contain dots themselves (go.delveConfig), while
// a variable can also address a member of that setting object.
func configurationValue(settings map[string]any, name string) (any, bool) {
	if value, ok := settings[name]; ok {
		return value, true
	}
	for split := strings.LastIndex(name, "."); split > 0; split = strings.LastIndex(name[:split], ".") {
		if value, ok := settings[name[:split]]; ok {
			for _, member := range strings.Split(name[split+1:], ".") {
				object, ok := value.(map[string]any)
				if !ok {
					return nil, false
				}
				value, ok = object[member]
				if !ok {
					return nil, false
				}
			}
			return value, true
		}
	}
	return nil, false
}
