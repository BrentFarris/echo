package plugins

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const registryVersion = 1

const maxWorkspaceRecipeBytes = 1 << 20

type Source struct {
	Type         string `json:"type"`
	Repository   string `json:"repository,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Subdirectory string `json:"subdirectory,omitempty"`
	Path         string `json:"path,omitempty"`
	Builtin      string `json:"builtin,omitempty"`
}

type InstalledPlugin struct {
	Manifest            Manifest                              `json:"manifest"`
	Digest              string                                `json:"digest"`
	PackagePath         string                                `json:"packagePath"`
	Source              Source                                `json:"source"`
	ApprovedPermissions []string                              `json:"approvedPermissions,omitempty"`
	ApprovedTools       []string                              `json:"approvedTools,omitempty"`
	GlobalEnabled       bool                                  `json:"globalEnabled,omitempty"`
	WorkspaceEnabled    map[string]bool                       `json:"workspaceEnabled,omitempty"`
	GlobalConfig        map[string]any                        `json:"globalConfig,omitempty"`
	GlobalSecretRefs    map[string]SecretReference            `json:"globalSecretRefs,omitempty"`
	WorkspaceConfig     map[string]map[string]any             `json:"workspaceConfig,omitempty"`
	WorkspaceSecretRefs map[string]map[string]SecretReference `json:"workspaceSecretRefs,omitempty"`
	InstalledAt         time.Time                             `json:"installedAt"`
	UpdatedAt           time.Time                             `json:"updatedAt"`
}

type registryFile struct {
	Version        int                        `json:"version"`
	InstallationID string                     `json:"installationId"`
	Plugins        map[string]InstalledPlugin `json:"plugins"`
	Retained       map[string]InstalledPlugin `json:"retained,omitempty"`
}

type registryStore struct {
	path string
	mu   sync.Mutex
}

func newRegistryStore(path string) *registryStore { return &registryStore{path: path} }

func (s *registryStore) load() (registryFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *registryStore) loadLocked() (registryFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newRegistryFile(), nil
		}
		return registryFile{}, fmt.Errorf("read plugin registry: %w", err)
	}
	var state registryFile
	if err := json.Unmarshal(data, &state); err != nil {
		return registryFile{}, fmt.Errorf("parse plugin registry: %w", err)
	}
	if state.Version != registryVersion {
		return registryFile{}, fmt.Errorf("unsupported plugin registry version %d", state.Version)
	}
	if state.Plugins == nil {
		state.Plugins = map[string]InstalledPlugin{}
	}
	if state.Retained == nil {
		state.Retained = map[string]InstalledPlugin{}
	}
	if !strings.HasPrefix(state.InstallationID, "installation-") || !validOpaqueID(state.InstallationID) {
		return registryFile{}, fmt.Errorf("plugin registry installation identity is invalid")
	}
	if err := validateRegistryEntries(state.Plugins, false); err != nil {
		return registryFile{}, err
	}
	if err := validateRegistryEntries(state.Retained, true); err != nil {
		return registryFile{}, err
	}
	return state, nil
}

func validateRegistryEntries(entries map[string]InstalledPlugin, retained bool) error {
	for id, installed := range entries {
		if !pluginIDPattern.MatchString(id) || len(id) > 64 || installed.Manifest.ID != id || !validDigest(installed.Digest) {
			return fmt.Errorf("plugin registry contains an invalid plugin record")
		}
		if retained && installed.PackagePath != "" {
			return fmt.Errorf("plugin registry retained record %q unexpectedly references executable code", id)
		}
		for workspaceID := range installed.WorkspaceEnabled {
			if !validOpaqueID(workspaceID) {
				return fmt.Errorf("plugin registry record %q contains an invalid workspace id", id)
			}
		}
		for workspaceID := range installed.WorkspaceConfig {
			if !validOpaqueID(workspaceID) {
				return fmt.Errorf("plugin registry record %q contains an invalid workspace id", id)
			}
		}
		for workspaceID := range installed.WorkspaceSecretRefs {
			if !validOpaqueID(workspaceID) {
				return fmt.Errorf("plugin registry record %q contains an invalid workspace id", id)
			}
		}
	}
	return nil
}

func (s *registryStore) update(update func(*registryFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	return s.saveLocked(state)
}

func (s *registryStore) save(state registryFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(state)
}

func (s *registryStore) saveLocked(state registryFile) error {
	state.Version = registryVersion
	if state.InstallationID == "" {
		state.InstallationID = randomID("installation-")
	}
	if state.Plugins == nil {
		state.Plugins = map[string]InstalledPlugin{}
	}
	if state.Retained == nil {
		state.Retained = map[string]InstalledPlugin{}
	}
	if err := validateRegistryEntries(state.Plugins, false); err != nil {
		return err
	}
	if err := validateRegistryEntries(state.Retained, true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plugin registry: %w", err)
	}
	return writeAtomic(s.path, data, 0o600)
}

type WorkspaceRecipe struct {
	Version int            `json:"version"`
	Plugins []PluginRecipe `json:"plugins"`
}

type PluginRecipe struct {
	ID         string                     `json:"id"`
	Source     Source                     `json:"source"`
	Enabled    bool                       `json:"enabled"`
	Config     map[string]any             `json:"config,omitempty"`
	SecretRefs map[string]SecretReference `json:"secretRefs,omitempty"`
}

type SecretReference struct {
	Source      string `json:"source"`
	Environment string `json:"environment,omitempty"`
}

func loadWorkspaceRecipe(workspacePath string) (WorkspaceRecipe, error) {
	path := filepath.Join(workspacePath, ".echo", "plugins.json")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WorkspaceRecipe{Version: 1, Plugins: []PluginRecipe{}}, nil
		}
		return WorkspaceRecipe{}, fmt.Errorf("read workspace plugin recipe: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxWorkspaceRecipeBytes {
		return WorkspaceRecipe{}, fmt.Errorf("workspace plugin recipe must be a regular file no larger than %d bytes", maxWorkspaceRecipeBytes)
	}
	var recipe WorkspaceRecipe
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return WorkspaceRecipe{}, fmt.Errorf("parse workspace plugin recipe: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return WorkspaceRecipe{}, fmt.Errorf("parse workspace plugin recipe: expected one JSON value")
	}
	if recipe.Version != 1 {
		return WorkspaceRecipe{}, fmt.Errorf("unsupported workspace plugin recipe version %d", recipe.Version)
	}
	if recipe.Plugins == nil {
		recipe.Plugins = []PluginRecipe{}
	}
	if err := validateWorkspaceRecipe(recipe); err != nil {
		return WorkspaceRecipe{}, err
	}
	return recipe, nil
}

func saveWorkspaceRecipe(workspacePath string, recipe WorkspaceRecipe) error {
	recipe.Version = 1
	if recipe.Plugins == nil {
		recipe.Plugins = []PluginRecipe{}
	}
	if err := validateWorkspaceRecipe(recipe); err != nil {
		return err
	}
	sort.SliceStable(recipe.Plugins, func(i, j int) bool { return recipe.Plugins[i].ID < recipe.Plugins[j].ID })
	data, err := json.MarshalIndent(recipe, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace plugin recipe: %w", err)
	}
	return writeAtomic(filepath.Join(workspacePath, ".echo", "plugins.json"), data, 0o644)
}

func validateWorkspaceRecipe(recipe WorkspaceRecipe) error {
	seen := map[string]bool{}
	for index := range recipe.Plugins {
		entry := &recipe.Plugins[index]
		if !pluginIDPattern.MatchString(entry.ID) || len(entry.ID) > 64 || seen[entry.ID] {
			return fmt.Errorf("workspace plugin recipe contains an invalid or duplicate plugin id")
		}
		seen[entry.ID] = true
		entry.Source = normalizeSource(entry.Source)
		switch entry.Source.Type {
		case "github":
			repository, err := normalizeGitHubRepository(entry.Source.Repository)
			_, subdirectoryErr := packagePath(".", entry.Source.Subdirectory)
			if entry.Source.Subdirectory == "" {
				subdirectoryErr = nil
			}
			if err != nil || subdirectoryErr != nil || !validGitCommit(entry.Source.Commit) || entry.Source.Path != "" || entry.Source.Builtin != "" {
				return fmt.Errorf("workspace plugin %q must pin a public GitHub repository and immutable commit", entry.ID)
			}
			entry.Source.Repository = repository
			entry.Source.Ref = ""
		case "builtin":
			if !pluginIDPattern.MatchString(entry.Source.Builtin) || entry.Source.Path != "" || entry.Source.Repository != "" || entry.Source.Commit != "" || entry.Source.Subdirectory != "" {
				return fmt.Errorf("workspace plugin %q has an invalid built-in source", entry.ID)
			}
		default:
			return fmt.Errorf("workspace plugin %q must use a portable GitHub or built-in source", entry.ID)
		}
		for key, reference := range entry.SecretRefs {
			if !viewIDPattern.MatchString(key) {
				return fmt.Errorf("workspace plugin %q contains an invalid secret reference", entry.ID)
			}
			switch reference.Source {
			case "os", "session":
				if reference.Environment != "" {
					return fmt.Errorf("workspace plugin %q contains an invalid secret reference", entry.ID)
				}
			case "environment":
				if !validEnvironmentName(reference.Environment) {
					return fmt.Errorf("workspace plugin %q contains an invalid environment reference", entry.ID)
				}
			default:
				return fmt.Errorf("workspace plugin %q contains an unsupported secret reference", entry.ID)
			}
		}
	}
	return nil
}

type StageRecord struct {
	ID             string     `json:"id"`
	CreatedAt      time.Time  `json:"createdAt"`
	Source         Source     `json:"source"`
	Validation     Validation `json:"validation"`
	PreviousDigest string     `json:"previousDigest,omitempty"`
	Diff           StageDiff  `json:"diff,omitempty"`
	PackagePath    string     `json:"-"`
}

type StageDiff struct {
	PreviousVersion      string   `json:"previousVersion,omitempty"`
	CodeChanged          bool     `json:"codeChanged,omitempty"`
	PermissionsChanged   bool     `json:"permissionsChanged,omitempty"`
	ToolContractsChanged bool     `json:"toolContractsChanged,omitempty"`
	PermissionsAdded     []string `json:"permissionsAdded,omitempty"`
	PermissionsRemoved   []string `json:"permissionsRemoved,omitempty"`
	ToolsAdded           []string `json:"toolsAdded,omitempty"`
	ToolsRemoved         []string `json:"toolsRemoved,omitempty"`
	ViewsAdded           []string `json:"viewsAdded,omitempty"`
	ViewsRemoved         []string `json:"viewsRemoved,omitempty"`
	SettingsAdded        []string `json:"settingsAdded,omitempty"`
	SettingsRemoved      []string `json:"settingsRemoved,omitempty"`
}

func readStage(root, id string) (StageRecord, error) {
	if !validOpaqueID(id) {
		return StageRecord{}, fmt.Errorf("invalid plugin stage id")
	}
	directory := filepath.Join(root, "staging", id)
	data, err := os.ReadFile(filepath.Join(directory, "stage.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StageRecord{}, os.ErrNotExist
		}
		return StageRecord{}, err
	}
	var record StageRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return StageRecord{}, fmt.Errorf("parse plugin stage: %w", err)
	}
	if record.ID != id {
		return StageRecord{}, fmt.Errorf("plugin stage identity mismatch")
	}
	record.PackagePath = filepath.Join(directory, "package")
	return record, nil
}

func writeStage(root string, record StageRecord) error {
	if !validOpaqueID(record.ID) {
		return fmt.Errorf("invalid plugin stage id")
	}
	directory := filepath.Join(root, "staging", record.ID)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, "stage.json"), data, 0o600)
}

func listStages(root string) ([]StageRecord, error) {
	directory := filepath.Join(root, "staging")
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []StageRecord{}, nil
		}
		return nil, err
	}
	stages := make([]StageRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validOpaqueID(entry.Name()) {
			continue
		}
		record, err := readStage(root, entry.Name())
		if err == nil {
			stages = append(stages, record)
		}
	}
	return stages, nil
}

func writeAtomic(path string, data []byte, permissions os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(permissions); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceAtomic(temporaryPath, path)
}

func newRegistryFile() registryFile {
	return registryFile{Version: registryVersion, InstallationID: randomID("installation-"), Plugins: map[string]InstalledPlugin{}, Retained: map[string]InstalledPlugin{}}
}

func randomID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return prefix + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

func validOpaqueID(value string) bool {
	if len(value) < 8 || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func normalizeSource(source Source) Source {
	source.Type = strings.ToLower(strings.TrimSpace(source.Type))
	source.Repository = strings.TrimSpace(source.Repository)
	source.Ref = strings.TrimSpace(source.Ref)
	source.Commit = strings.TrimSpace(source.Commit)
	source.Subdirectory = filepath.ToSlash(strings.Trim(strings.TrimSpace(source.Subdirectory), "/"))
	source.Path = strings.TrimSpace(source.Path)
	source.Builtin = strings.TrimSpace(source.Builtin)
	return source
}
