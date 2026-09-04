package debugconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type ImportWarning struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	ConfigurationName string `json:"configurationName,omitempty"`
	TaskLabel         string `json:"taskLabel,omitempty"`
	Field             string `json:"field,omitempty"`
}

type ImportPreview struct {
	Config   WorkspaceConfig `json:"config"`
	Warnings []ImportWarning `json:"warnings,omitempty"`
}

type vscodeLaunchFile struct {
	Configurations []map[string]any `json:"configurations"`
	Compounds      []struct {
		Name           string   `json:"name"`
		Configurations []string `json:"configurations"`
		StopAll        *bool    `json:"stopAll"`
	} `json:"compounds"`
	Inputs []map[string]any `json:"inputs"`
}

type vscodeTasksFile struct {
	Tasks []map[string]any `json:"tasks"`
}

// PreviewVSCodeImport converts VS Code JSONC into Echo's portable debug
// configuration without writing either source file. Unsupported task and
// adapter constructs are surfaced as warnings so the UI can require review.
func PreviewVSCodeImport(launchJSONC, tasksJSONC []byte, profiles []AdapterProfile) (ImportPreview, error) {
	var source vscodeLaunchFile
	if err := DecodeJSONC(launchJSONC, &source); err != nil {
		return ImportPreview{}, fmt.Errorf("parse .vscode/launch.json: %w", err)
	}
	installed := map[string]bool{}
	for _, profile := range profiles {
		installed[profile.Normalized().ID] = true
	}
	tasks := map[string]map[string]any{}
	var warnings []ImportWarning
	if len(tasksJSONC) > 0 {
		var taskFile vscodeTasksFile
		if err := DecodeJSONC(tasksJSONC, &taskFile); err != nil {
			warnings = append(warnings, ImportWarning{Code: "tasks_parse_failed", Message: err.Error()})
		} else {
			for _, task := range taskFile.Tasks {
				label := stringValue(task["label"])
				if label == "" {
					label = stringValue(task["taskName"])
				}
				if label != "" {
					tasks[label] = task
				}
			}
		}
	}

	config := WorkspaceConfig{Version: CurrentVersion}
	usedIDs := map[string]bool{}
	configurationByName := map[string]string{}
	enabled := map[string]bool{}
	requiresProcessPicker := false
	for index, raw := range source.Configurations {
		name := strings.TrimSpace(stringValue(raw["name"]))
		if name == "" {
			name = fmt.Sprintf("Imported Configuration %d", index+1)
		}
		adapterType := strings.ToLower(strings.TrimSpace(stringValue(raw["type"])))
		profileID, known := importedProfileID(adapterType)
		if !known {
			profileID = uniqueID("unresolved-"+adapterType, usedIDs)
			warnings = append(warnings, ImportWarning{Code: "unknown_adapter", ConfigurationName: name, Field: "type", Message: fmt.Sprintf("VS Code adapter type %q needs an Echo adapter profile", adapterType)})
		} else {
			enabled[profileID] = true
			if !installed[profileID] {
				warnings = append(warnings, ImportWarning{Code: "profile_not_installed", ConfigurationName: name, Message: fmt.Sprintf("Install or create the %q adapter profile before saving", profileID)})
			}
		}
		request := strings.ToLower(strings.TrimSpace(stringValue(raw["request"])))
		if request != "attach" {
			request = "launch"
		}
		id := uniqueID(name, usedIDs)
		arguments := cloneAnyMap(raw)
		delete(arguments, "name")
		delete(arguments, "type")
		delete(arguments, "request")
		preTask := stringValue(arguments["preLaunchTask"])
		postTask := stringValue(arguments["postDebugTask"])
		delete(arguments, "preLaunchTask")
		delete(arguments, "postDebugTask")
		if rewriteKnownProcessPicker(arguments, "${input:__echo_process_picker__}") {
			requiresProcessPicker = true
		}
		entry := Configuration{ID: id, Name: name, AdapterProfileID: profileID, Request: request, Arguments: arguments}
		if preTask != "" {
			entry.PreLaunch = importTask(preTask, tasks, name, &warnings)
		}
		if postTask != "" {
			entry.PostDebug = importTask(postTask, tasks, name, &warnings)
		}
		if containsCommandVariable(arguments) {
			warnings = append(warnings, ImportWarning{Code: "command_variable_unsupported", ConfigurationName: name, Message: "${command:...} variables are not executed by Echo and must be replaced"})
		}
		config.Configurations = append(config.Configurations, entry)
		configurationByName[name] = id
	}

	for _, raw := range source.Compounds {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = "Imported Compound"
		}
		entry := Compound{ID: uniqueID(name, usedIDs), Name: name, StopAll: raw.StopAll == nil || *raw.StopAll}
		for _, configurationName := range raw.Configurations {
			if id := configurationByName[configurationName]; id != "" {
				entry.ConfigurationIDs = append(entry.ConfigurationIDs, id)
			} else {
				warnings = append(warnings, ImportWarning{Code: "compound_member_missing", ConfigurationName: name, Message: fmt.Sprintf("Compound member %q was not found", configurationName)})
			}
		}
		if len(entry.ConfigurationIDs) > 0 {
			config.Compounds = append(config.Compounds, entry)
		}
	}

	for _, raw := range source.Inputs {
		id := slugID(stringValue(raw["id"]))
		if id == "" {
			warnings = append(warnings, ImportWarning{Code: "input_id_missing", Message: "A VS Code input without an id was skipped"})
			continue
		}
		typeName := strings.TrimSpace(stringValue(raw["type"]))
		entry := Input{ID: id, Description: stringValue(raw["description"]), Default: stringValue(raw["default"])}
		switch typeName {
		case "promptString":
			entry.Type = "promptString"
			if boolValue(raw["password"]) {
				entry.Type = "secret"
			}
		case "pickString":
			entry.Type = "pickString"
			entry.Options = importInputOptions(raw["options"])
			if len(entry.Options) == 0 {
				warnings = append(warnings, ImportWarning{Code: "input_options_missing", Field: id, Message: "A pickString input has no usable options"})
				continue
			}
		case "command":
			warnings = append(warnings, ImportWarning{Code: "command_input_unsupported", Field: id, Message: "VS Code command inputs are not executed by Echo"})
			continue
		default:
			warnings = append(warnings, ImportWarning{Code: "input_type_unsupported", Field: id, Message: fmt.Sprintf("Input type %q is not supported", typeName)})
			continue
		}
		config.Inputs = append(config.Inputs, entry)
	}
	if requiresProcessPicker {
		usedInputIDs := map[string]bool{}
		for _, input := range config.Inputs {
			usedInputIDs[input.ID] = true
		}
		processInputID := uniqueID("process", usedInputIDs)
		for index := range config.Configurations {
			replaceImportedVariable(config.Configurations[index].Arguments, "${input:__echo_process_picker__}", "${input:"+processInputID+"}")
		}
		config.Inputs = append(config.Inputs, Input{ID: processInputID, Type: "pickProcess", Description: "Select a process from the workspace runtime"})
	}

	for id := range enabled {
		config.EnabledAdapterProfileIDs = append(config.EnabledAdapterProfileIDs, id)
	}
	sort.Strings(config.EnabledAdapterProfileIDs)
	config = config.Normalized()
	if err := config.validateStructure(false); err != nil {
		// Unresolved adapters deliberately remain reviewable, but all other
		// structural problems indicate an importer bug or unusable source.
		return ImportPreview{}, err
	}
	return ImportPreview{Config: config, Warnings: warnings}, nil
}

func importedProfileID(adapterType string) (string, bool) {
	switch adapterType {
	case "go":
		return "delve", true
	case "python", "debugpy":
		return "debugpy", true
	case "node", "node-terminal", "pwa-node", "chrome", "pwa-chrome", "msedge", "pwa-msedge":
		return "js-debug", true
	case "lldb", "codelldb", "cppdbg", "cppvsdbg":
		return "codelldb", true
	default:
		return "", false
	}
}

func importTask(label string, tasks map[string]map[string]any, configurationName string, warnings *[]ImportWarning) *LifecycleHook {
	task, ok := tasks[label]
	if !ok {
		*warnings = append(*warnings, ImportWarning{Code: "task_missing", ConfigurationName: configurationName, TaskLabel: label, Message: "Referenced VS Code task was not found"})
		return nil
	}
	if boolValue(task["isBackground"]) || task["problemMatcher"] != nil || task["dependsOn"] != nil {
		*warnings = append(*warnings, ImportWarning{Code: "task_requires_manual_conversion", ConfigurationName: configurationName, TaskLabel: label, Message: "Background, problem-matcher, and dependent tasks require manual conversion"})
		return nil
	}
	typeName := strings.ToLower(strings.TrimSpace(stringValue(task["type"])))
	hook := &LifecycleHook{TimeoutMS: DefaultHookTimeoutMS}
	switch typeName {
	case "process":
		hook.Command = stringValue(task["command"])
		hook.Args = stringSlice(task["args"])
	case "npm":
		script := stringValue(task["script"])
		if script == "" {
			script = stringValue(task["command"])
		}
		if script == "" {
			*warnings = append(*warnings, ImportWarning{Code: "task_requires_manual_conversion", ConfigurationName: configurationName, TaskLabel: label, Message: "The npm task does not name a script"})
			return nil
		}
		hook.Command = "npm"
		hook.Args = append([]string{"run", script}, stringSlice(task["args"])...)
	default:
		*warnings = append(*warnings, ImportWarning{Code: "task_requires_manual_conversion", ConfigurationName: configurationName, TaskLabel: label, Message: fmt.Sprintf("Task type %q cannot be safely converted to direct argv", typeName)})
		return nil
	}
	if hook.Command == "" {
		*warnings = append(*warnings, ImportWarning{Code: "task_requires_manual_conversion", ConfigurationName: configurationName, TaskLabel: label, Message: "Task command is empty"})
		return nil
	}
	if options, ok := task["options"].(map[string]any); ok {
		hook.Cwd = stringValue(options["cwd"])
		if environment, ok := options["env"].(map[string]any); ok {
			hook.Environment = map[string]string{}
			for key, value := range environment {
				hook.Environment[key] = stringValue(value)
			}
		}
	}
	return hook
}

func uniqueID(value string, used map[string]bool) string {
	base := slugID(value)
	if base == "" {
		base = "debug"
	}
	id := base
	for suffix := 2; used[id]; suffix++ {
		addition := "-" + strconv.Itoa(suffix)
		trimmed := base
		if len(trimmed)+len(addition) > 64 {
			trimmed = trimmed[:64-len(addition)]
		}
		id = trimmed + addition
	}
	used[id] = true
	return id
}

func slugID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	separator := false
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) || current == '.' || current == '_' {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			separator = false
			result.WriteRune(current)
		} else {
			separator = true
		}
		if result.Len() >= 64 {
			break
		}
	}
	return strings.Trim(result.String(), "-._")
}

func cloneAnyMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := stringValue(value); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func importInputOptions(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			value = object["value"]
		}
		if text := stringValue(value); text != "" {
			result = append(result, text)
		}
	}
	return result
}

var commandVariablePattern = regexp.MustCompile(`\$\{command:[^}]+\}`)

func containsCommandVariable(value any) bool {
	switch typed := value.(type) {
	case string:
		return commandVariablePattern.MatchString(typed)
	case []any:
		for _, item := range typed {
			if containsCommandVariable(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsCommandVariable(item) {
				return true
			}
		}
	}
	return false
}

func rewriteKnownProcessPicker(value any, replacement string) bool {
	changed := false
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if text, ok := item.(string); ok {
				next := strings.ReplaceAll(text, "${command:pickProcess}", replacement)
				next = strings.ReplaceAll(next, "${command:pickRemoteProcess}", replacement)
				if next != text {
					current[key] = next
					changed = true
				}
			} else if rewriteKnownProcessPicker(item, replacement) {
				changed = true
			}
		}
	case []any:
		for index, item := range current {
			if text, ok := item.(string); ok {
				next := strings.ReplaceAll(text, "${command:pickProcess}", replacement)
				next = strings.ReplaceAll(next, "${command:pickRemoteProcess}", replacement)
				if next != text {
					current[index] = next
					changed = true
				}
			} else if rewriteKnownProcessPicker(item, replacement) {
				changed = true
			}
		}
	}
	return changed
}

func replaceImportedVariable(value any, oldValue, newValue string) {
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if text, ok := item.(string); ok {
				current[key] = strings.ReplaceAll(text, oldValue, newValue)
			} else {
				replaceImportedVariable(item, oldValue, newValue)
			}
		}
	case []any:
		for index, item := range current {
			if text, ok := item.(string); ok {
				current[index] = strings.ReplaceAll(text, oldValue, newValue)
			} else {
				replaceImportedVariable(item, oldValue, newValue)
			}
		}
	}
}
