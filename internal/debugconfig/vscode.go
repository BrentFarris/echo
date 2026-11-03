package debugconfig

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/editorfs"
	"io/fs"
	"runtime"
	"strings"
)

func ConfigID(root, name string) string {
	return root + ":" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// Read consumes only the two workspace files. A bad root contributes a
// diagnostic without hiding healthy configurations in another root.
func Read(roots []editorfs.Root, read func(editorfs.FileRef) ([]byte, error)) WorkspaceConfig {
	result := WorkspaceConfig{}.Normalized()
	for _, root := range roots {
		if root.BlockedReason != "" {
			continue
		}
		settings := map[string]any{}
		raw, err := read(editorfs.FileRef{RootID: root.ID, Path: ".vscode/settings.json"})
		if err == nil {
			err = DecodeJSONC(raw, &settings)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, editorfs.ErrNotFound) {
			result.Diagnostics = append(result.Diagnostics, root.Label+" settings.json: "+err.Error())
			continue
		}
		raw, err = read(editorfs.FileRef{RootID: root.ID, Path: ".vscode/launch.json"})
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, editorfs.ErrNotFound) {
			continue
		}
		var launch struct {
			Configurations []map[string]any `json:"configurations"`
			Compounds      []map[string]any `json:"compounds"`
			Inputs         []map[string]any `json:"inputs"`
		}
		if err == nil {
			err = DecodeJSONC(raw, &launch)
		}
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, root.Label+" launch.json: "+err.Error())
			continue
		}
		names := map[string]int{}
		for _, entry := range launch.Configurations {
			names[stringValue(entry["name"])]++
		}
		for _, entry := range launch.Configurations {
			name := stringValue(entry["name"])
			args := map[string]any{}
			for k, v := range entry {
				args[k] = v
			}
			platform := map[string]string{"windows": "windows", "darwin": "osx", "linux": "linux"}[runtime.GOOS]
			if override, ok := args[platform].(map[string]any); ok {
				for k, v := range override {
					args[k] = v
				}
			}
			kind, request := stringValue(args["type"]), stringValue(args["request"])
			for _, key := range []string{"windows", "linux", "osx", "name", "type", "request", "presentation"} {
				delete(args, key)
			}
			args = QualifyInputs(args, root.ID).(map[string]any)
			c := Configuration{ID: ConfigID(root.ID, name), Name: name, AdapterProfileID: kind, Request: request, Arguments: args, RootID: root.ID, Folder: root.HostPath, Settings: settings}
			if len(roots) > 1 {
				c.Name = root.Label + ": " + name
			}
			if names[name] > 1 {
				c.Error = "configuration names must be unique within launch.json"
			}
			if err := ValidateConfiguration(c); err != nil {
				c.Error = err.Error()
			}
			result.Configurations = append(result.Configurations, c)
		}
		for _, entry := range launch.Compounds {
			name := stringValue(entry["name"])
			c := Compound{ID: ConfigID(root.ID, "compound:"+name), Name: name}
			c.StopAll, _ = entry["stopAll"].(bool)
			if len(roots) > 1 {
				c.Name = root.Label + ": " + name
			}
			if entry["preLaunchTask"] != nil {
				c.Error = "compound preLaunchTask is unsupported"
			}
			members, _ := entry["configurations"].([]any)
			for _, member := range members {
				memberRoot, memberName := root.ID, stringValue(member)
				if object, ok := member.(map[string]any); ok {
					memberName = stringValue(object["name"])
					folder := stringValue(object["folder"])
					matches := 0
					for _, r := range roots {
						if r.Label == folder || r.ReferenceLabel == folder {
							memberRoot = r.ID
							matches++
						}
					}
					if matches != 1 {
						c.Error = fmt.Sprintf("compound folder %q must identify one bot folder", folder)
					}
				}
				c.ConfigurationIDs = append(c.ConfigurationIDs, ConfigID(memberRoot, memberName))
			}
			result.Compounds = append(result.Compounds, c)
		}
		for _, entry := range launch.Inputs {
			input := Input{ID: root.ID + ":" + stringValue(entry["id"]), Type: stringValue(entry["type"]), Description: stringValue(entry["description"]), Default: stringValue(entry["default"])}
			if entry["password"] == true {
				input.Type = "secret"
			}
			if values, ok := entry["options"].([]any); ok {
				for _, value := range values {
					if o, ok := value.(map[string]any); ok {
						value = o["value"]
					}
					input.Options = append(input.Options, stringValue(value))
				}
			}
			result.Inputs = append(result.Inputs, input)
		}
	}
	return result
}
func QualifyInputs(value any, root string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "${input:", "${input:"+root+":")
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = QualifyInputs(x, root)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			out[k] = QualifyInputs(x, root)
		}
		return out
	default:
		return value
	}
}
