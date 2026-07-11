package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	workspaceDebugVersion          = "0.2.0"
	implicitGoDebugConfiguration   = "Go: Launch Package"
	workspaceDebugSettingsMaxBytes = 1024 * 1024
)

var emptyWorkspaceDebugJSON = json.RawMessage(`{"version":"0.2.0","configurations":[]}`)

// DebugConfigurationSummary is the adapter-neutral portion of a launch
// configuration needed by configuration pickers. Adapter-specific properties
// remain in the JSON document and are not discarded when it is saved.
type DebugConfigurationSummary struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Request string `json:"request"`
}

// WorkspaceDebugSettings is the editor-facing view of the debug section in
// the canonical (first-folder) .echo/workspace.json file.
type WorkspaceDebugSettings struct {
	WorkspaceID           string                      `json:"workspaceId"`
	StoragePath           string                      `json:"storagePath"`
	Revision              string                      `json:"revision"`
	SelectedConfiguration string                      `json:"selectedConfiguration"`
	Configurations        []DebugConfigurationSummary `json:"configurations"`
	JSON                  string                      `json:"json"`
	Implicit              bool                        `json:"implicit"`
}

type WorkspaceDebugSettingsInput struct {
	JSON             string `json:"json"`
	ExpectedRevision string `json:"expectedRevision"`
}

type workspaceDebugDocument struct {
	Raw            json.RawMessage
	Configurations []workspaceDebugConfiguration
}

type workspaceDebugConfiguration struct {
	Name    string
	Type    string
	Request string
	Raw     json.RawMessage
}

type workspaceDebugLocation struct {
	Root string
	Path string
}

// LoadWorkspaceDebugSettings reads, validates, and formats only the debug
// section. Other workspace.json fields are intentionally opaque to this API.
func (s *SystemService) LoadWorkspaceDebugSettings(workspaceID string) (WorkspaceDebugSettings, error) {
	workspace, err := s.workspaceByID(strings.TrimSpace(workspaceID))
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}

	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	location, state, document, err := loadWorkspaceDebugState(workspace)
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}
	return workspaceDebugSettingsView(workspace, location, state.Debug, document), nil
}

// SaveWorkspaceDebugSettings atomically replaces only the debug section. Its
// revision is derived only from that section, so task-tag or Git metadata
// updates do not create false edit conflicts.
func (s *SystemService) SaveWorkspaceDebugSettings(workspaceID string, input WorkspaceDebugSettingsInput) (WorkspaceDebugSettings, error) {
	workspace, err := s.workspaceByID(strings.TrimSpace(workspaceID))
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}
	if len(input.JSON) > workspaceDebugSettingsMaxBytes {
		return WorkspaceDebugSettings{}, fmt.Errorf("debug settings are larger than the 1 MiB limit")
	}
	if strings.TrimSpace(input.JSON) == "" {
		return WorkspaceDebugSettings{}, fmt.Errorf("debug settings JSON is required")
	}
	document, err := parseWorkspaceDebugDocument([]byte(input.JSON))
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}

	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	location, state, _, err := loadWorkspaceDebugState(workspace)
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}
	expected := strings.TrimSpace(input.ExpectedRevision)
	if expected == "" {
		return WorkspaceDebugSettings{}, fmt.Errorf("expected debug settings revision is required")
	}
	if expected != workspaceDebugRevision(state.Debug) {
		return WorkspaceDebugSettings{}, fmt.Errorf("debug settings changed; reload them and try again")
	}

	state.Debug = append(json.RawMessage(nil), document.Raw...)
	if err := writeWorkspaceStateFileAt(location.Root, location.Path, state); err != nil {
		return WorkspaceDebugSettings{}, fmt.Errorf("save debug settings: %w", err)
	}
	return workspaceDebugSettingsView(workspace, location, state.Debug, document), nil
}

// SetWorkspaceSelectedDebugConfiguration stores the selection in Echo's local
// user state rather than in the tracked workspace file.
func (s *SystemService) SetWorkspaceSelectedDebugConfiguration(workspaceID string, name string) (WorkspaceDebugSettings, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	workspace, err := s.workspaceByID(workspaceID)
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}
	name = strings.TrimSpace(name)

	s.taskMu.Lock()
	location, state, document, err := loadWorkspaceDebugState(workspace)
	if err == nil && name != "" && !workspaceDebugConfigurationExists(document, name) {
		if name != implicitGoDebugConfiguration || !workspaceHasImplicitGoDebugConfiguration(workspace) || len(document.Configurations) != 0 {
			err = fmt.Errorf("debug configuration %q was not found", name)
		}
	}
	s.taskMu.Unlock()
	if err != nil {
		return WorkspaceDebugSettings{}, err
	}

	s.mu.Lock()
	found := false
	for i := range s.state.Workspaces {
		if s.state.Workspaces[i].ID != workspaceID {
			continue
		}
		s.state.Workspaces[i].SelectedDebugConfiguration = name
		workspace = s.state.Workspaces[i]
		found = true
		break
	}
	if !found {
		s.mu.Unlock()
		return WorkspaceDebugSettings{}, fmt.Errorf("workspace was not found")
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return WorkspaceDebugSettings{}, err
	}
	s.mu.Unlock()

	return workspaceDebugSettingsView(workspace, location, state.Debug, document), nil
}

// resolveWorkspaceDebugConfiguration returns an explicit raw configuration or
// the built-in single-folder Go launch. Variable expansion and adapter-specific
// validation belong to the DAP layer.
func resolveWorkspaceDebugConfiguration(workspace Workspace, name string, currentFile string) (map[string]any, error) {
	_ = currentFile
	_, _, document, err := loadWorkspaceDebugState(workspace)
	if err != nil {
		return nil, err
	}

	requestedName := strings.TrimSpace(name)
	if requestedName == "" {
		requestedName = strings.TrimSpace(workspace.SelectedDebugConfiguration)
	}
	if requestedName != "" {
		for _, configuration := range document.Configurations {
			if configuration.Name == requestedName {
				return decodeWorkspaceDebugConfiguration(configuration.Raw)
			}
		}
		// A stale locally-persisted selection should not make F5 unusable. An
		// explicitly requested missing name, however, is a caller error.
		if strings.TrimSpace(name) != "" {
			if requestedName == implicitGoDebugConfiguration && len(document.Configurations) == 0 && workspaceHasImplicitGoDebugConfiguration(workspace) {
				return implicitGoDebugConfigurationMap(), nil
			}
			return nil, fmt.Errorf("debug configuration %q was not found", requestedName)
		}
	}
	if len(document.Configurations) > 0 {
		return decodeWorkspaceDebugConfiguration(document.Configurations[0].Raw)
	}
	if workspaceHasImplicitGoDebugConfiguration(workspace) {
		return implicitGoDebugConfigurationMap(), nil
	}
	return nil, fmt.Errorf("workspace has no debug configurations")
}

func loadWorkspaceDebugState(workspace Workspace) (workspaceDebugLocation, workspaceStateFileData, workspaceDebugDocument, error) {
	location, err := resolveWorkspaceDebugLocation(workspace)
	if err != nil {
		return workspaceDebugLocation{}, workspaceStateFileData{}, workspaceDebugDocument{}, err
	}
	state, err := readWorkspaceStateFileAt(location.Path)
	if err != nil {
		return location, workspaceStateFileData{}, workspaceDebugDocument{}, err
	}
	document, err := parseWorkspaceDebugDocument(state.Debug)
	if err != nil {
		return location, state, workspaceDebugDocument{}, fmt.Errorf("invalid debug settings: %w", err)
	}
	return location, state, document, nil
}

func resolveWorkspaceDebugLocation(workspace Workspace) (workspaceDebugLocation, error) {
	if len(workspace.Folders) == 0 {
		return workspaceDebugLocation{}, fmt.Errorf("workspace has no configured folder for debug settings")
	}
	folder := workspace.Folders[0]
	if folder.Missing || strings.TrimSpace(folder.Path) == "" {
		return workspaceDebugLocation{}, fmt.Errorf("the first workspace folder is unavailable; debug settings cannot be loaded")
	}
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return workspaceDebugLocation{}, fmt.Errorf("resolve first workspace folder: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return workspaceDebugLocation{}, fmt.Errorf("the first workspace folder is unavailable: %w", err)
	}
	if !info.IsDir() {
		return workspaceDebugLocation{}, fmt.Errorf("the first workspace folder is not a directory")
	}
	cacheRoot := filepath.Join(root, workspaceCacheDirName)
	if cacheInfo, cacheErr := os.Lstat(cacheRoot); cacheErr == nil {
		if cacheInfo.Mode()&os.ModeSymlink != 0 || !cacheInfo.IsDir() {
			return workspaceDebugLocation{}, fmt.Errorf("workspace .echo path must be a directory and not a symlink")
		}
	} else if !os.IsNotExist(cacheErr) {
		return workspaceDebugLocation{}, fmt.Errorf("stat workspace .echo directory: %w", cacheErr)
	}
	return workspaceDebugLocation{Root: root, Path: filepath.Join(cacheRoot, workspaceStateFile)}, nil
}

func parseWorkspaceDebugDocument(data []byte) (workspaceDebugDocument, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = emptyWorkspaceDebugJSON
	}
	if len(data) > workspaceDebugSettingsMaxBytes {
		return workspaceDebugDocument{}, fmt.Errorf("debug settings are larger than the 1 MiB limit")
	}
	var root map[string]json.RawMessage
	if err := decodeSingleJSONValue(data, &root); err != nil {
		return workspaceDebugDocument{}, fmt.Errorf("debug settings must be strict JSON: %w", err)
	}
	if root == nil {
		return workspaceDebugDocument{}, fmt.Errorf("debug settings must be a JSON object")
	}
	var version string
	value, ok := root["version"]
	if !ok || json.Unmarshal(value, &version) != nil || strings.TrimSpace(version) == "" {
		return workspaceDebugDocument{}, fmt.Errorf("debug version must be %q", workspaceDebugVersion)
	}
	if version != workspaceDebugVersion {
		return workspaceDebugDocument{}, fmt.Errorf("unsupported debug version %q; expected %q", version, workspaceDebugVersion)
	}
	var configurationsRaw []json.RawMessage
	value, ok = root["configurations"]
	if !ok || json.Unmarshal(value, &configurationsRaw) != nil || configurationsRaw == nil {
		return workspaceDebugDocument{}, fmt.Errorf("debug configurations must be a JSON array")
	}
	configurations := make([]workspaceDebugConfiguration, 0, len(configurationsRaw))
	seen := make(map[string]bool, len(configurationsRaw))
	for index, raw := range configurationsRaw {
		configuration, err := parseWorkspaceDebugConfiguration(raw)
		if err != nil {
			return workspaceDebugDocument{}, fmt.Errorf("debug configuration %d: %w", index+1, err)
		}
		if seen[configuration.Name] {
			return workspaceDebugDocument{}, fmt.Errorf("debug configuration name %q is duplicated", configuration.Name)
		}
		seen[configuration.Name] = true
		configurations = append(configurations, configuration)
	}
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, data); err != nil {
		return workspaceDebugDocument{}, err
	}
	return workspaceDebugDocument{Raw: append(json.RawMessage(nil), compact.Bytes()...), Configurations: configurations}, nil
}

func parseWorkspaceDebugConfiguration(data []byte) (workspaceDebugConfiguration, error) {
	var raw map[string]json.RawMessage
	if err := decodeSingleJSONValue(data, &raw); err != nil || raw == nil {
		return workspaceDebugConfiguration{}, fmt.Errorf("must be a JSON object")
	}
	readRequiredString := func(key string) (string, error) {
		value, ok := raw[key]
		if !ok {
			return "", fmt.Errorf("%s is required", key)
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil || strings.TrimSpace(decoded) == "" {
			return "", fmt.Errorf("%s must be a non-empty string", key)
		}
		return strings.TrimSpace(decoded), nil
	}
	name, err := readRequiredString("name")
	if err != nil {
		return workspaceDebugConfiguration{}, err
	}
	typeName, err := readRequiredString("type")
	if err != nil {
		return workspaceDebugConfiguration{}, err
	}
	request, err := readRequiredString("request")
	if err != nil {
		return workspaceDebugConfiguration{}, err
	}
	return workspaceDebugConfiguration{Name: name, Type: typeName, Request: request, Raw: append(json.RawMessage(nil), data...)}, nil
}

func decodeSingleJSONValue(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func decodeWorkspaceDebugConfiguration(data []byte) (map[string]any, error) {
	var configuration map[string]any
	if err := decodeSingleJSONValue(data, &configuration); err != nil {
		return nil, err
	}
	return configuration, nil
}

func workspaceDebugSettingsView(workspace Workspace, location workspaceDebugLocation, stored json.RawMessage, document workspaceDebugDocument) WorkspaceDebugSettings {
	configurations := make([]DebugConfigurationSummary, 0, len(document.Configurations)+1)
	for _, configuration := range document.Configurations {
		configurations = append(configurations, DebugConfigurationSummary{Name: configuration.Name, Type: configuration.Type, Request: configuration.Request})
	}
	implicit := len(configurations) == 0 && workspaceHasImplicitGoDebugConfiguration(workspace)
	if implicit {
		configurations = append(configurations, DebugConfigurationSummary{Name: implicitGoDebugConfiguration, Type: "go", Request: "launch"})
	}
	selected := effectiveWorkspaceDebugConfiguration(workspace.SelectedDebugConfiguration, configurations)
	formatted := &bytes.Buffer{}
	if err := json.Indent(formatted, document.Raw, "", "  "); err != nil {
		formatted.Write(document.Raw)
	}
	return WorkspaceDebugSettings{
		WorkspaceID:           workspace.ID,
		StoragePath:           location.Path,
		Revision:              workspaceDebugRevision(stored),
		SelectedConfiguration: selected,
		Configurations:        configurations,
		JSON:                  formatted.String(),
		Implicit:              implicit,
	}
}

func effectiveWorkspaceDebugConfiguration(selected string, configurations []DebugConfigurationSummary) string {
	selected = strings.TrimSpace(selected)
	for _, configuration := range configurations {
		if configuration.Name == selected {
			return selected
		}
	}
	if len(configurations) > 0 {
		return configurations[0].Name
	}
	return ""
}

func workspaceDebugConfigurationExists(document workspaceDebugDocument, name string) bool {
	for _, configuration := range document.Configurations {
		if configuration.Name == name {
			return true
		}
	}
	return false
}

func workspaceDebugRevision(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		trimmed = []byte("absent")
	} else {
		var value any
		if decodeSingleJSONValue(trimmed, &value) == nil {
			if canonical, err := json.Marshal(value); err == nil {
				trimmed = canonical
			}
		}
	}
	sum := sha256.Sum256(trimmed)
	return hex.EncodeToString(sum[:])
}

func workspaceHasImplicitGoDebugConfiguration(workspace Workspace) bool {
	if len(workspace.Folders) != 1 || workspace.Folders[0].Missing {
		return false
	}
	root, err := workspaceFolderAbsolutePath(workspace.Folders[0])
	if err != nil {
		return false
	}
	for _, marker := range []string{"go.mod", "go.work"} {
		if info, err := os.Stat(filepath.Join(root, marker)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".go") {
			return true
		}
	}
	return false
}

func implicitGoDebugConfigurationMap() map[string]any {
	return map[string]any{
		"name":    implicitGoDebugConfiguration,
		"type":    "go",
		"request": "launch",
		"mode":    "debug",
		"program": "${workspaceFolder}",
		"cwd":     "${workspaceFolder}",
		"args":    []any{},
		"env":     map[string]any{},
	}
}
