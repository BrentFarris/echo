// Package workspaces manages Echo workspaces. A workspace is a named set of
// folders (paths on the server machine) that Echo operates on. The first
// (main) folder owns a hidden .echo directory that stores the workspace
// settings (workspace.json) and an optional icon (icon.<ext>).
//
// The workspace list itself lives in the shared Echo app data file (echo.json)
// alongside the application settings; see internal/appdata.
package workspaces

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/lspconfig"
)

// EchoDirName is the name of the hidden directory Echo creates in a
// workspace's main folder.
const EchoDirName = ".echo"

// Workspace is the resolved shape returned to runtime consumers and the
// frontend. Its paths are always absolute even when workspace.json is portable.
type Workspace struct {
	ID                          string                      `json:"id"`
	Name                        string                      `json:"name"`
	MainPath                    string                      `json:"mainPath"`
	IconExt                     string                      `json:"iconExt,omitempty"`
	Folders                     []string                    `json:"folders,omitempty"`
	SearchParentGitRepositories bool                        `json:"searchParentGitRepositories,omitempty"`
	LanguageServers             lspconfig.WorkspaceConfig   `json:"languageServers,omitempty"`
	Debug                       debugconfig.WorkspaceConfig `json:"debug,omitempty"`
	Sandbox                     SandboxConfig               `json:"sandbox"`
	sandboxConfigured           bool
}

const (
	DefaultSandboxCPULimit           = 4
	DefaultSandboxMemoryMiB          = 6144
	DefaultSandboxIdleTimeoutMinutes = 30
	MinSandboxCPULimit               = 1
	MaxSandboxCPULimit               = 16
	MinSandboxMemoryMiB              = 4096
	MaxSandboxMemoryMiB              = 32768
	MaxSandboxIdleTimeoutMinutes     = 1440
)

// SandboxConfig is the portable, workspace-owned Linux sandbox policy. A
// missing sandbox object is equivalent to the normalized disabled defaults.
type SandboxConfig struct {
	Enabled            bool `json:"enabled"`
	CPULimit           int  `json:"cpuLimit"`
	MemoryMiB          int  `json:"memoryMiB"`
	IdleTimeoutMinutes int  `json:"idleTimeoutMinutes"`
}

func (config *SandboxConfig) UnmarshalJSON(data []byte) error {
	var wire struct {
		Enabled            *bool `json:"enabled"`
		CPULimit           *int  `json:"cpuLimit"`
		MemoryMiB          *int  `json:"memoryMiB"`
		IdleTimeoutMinutes *int  `json:"idleTimeoutMinutes"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*config = DefaultSandboxConfig()
	if wire.Enabled != nil {
		config.Enabled = *wire.Enabled
	}
	if wire.CPULimit != nil {
		config.CPULimit = *wire.CPULimit
	}
	if wire.MemoryMiB != nil {
		config.MemoryMiB = *wire.MemoryMiB
	}
	if wire.IdleTimeoutMinutes != nil {
		config.IdleTimeoutMinutes = *wire.IdleTimeoutMinutes
	}
	return nil
}

// DefaultSandboxConfig returns the disabled v1 sandbox defaults.
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		CPULimit:           DefaultSandboxCPULimit,
		MemoryMiB:          DefaultSandboxMemoryMiB,
		IdleTimeoutMinutes: DefaultSandboxIdleTimeoutMinutes,
	}
}

// NormalizeSandboxConfig fills omitted CPU and memory values and validates the
// public resource limits. Zero idle timeout intentionally means never stop.
func NormalizeSandboxConfig(config SandboxConfig) (SandboxConfig, error) {
	if config.CPULimit == 0 {
		config.CPULimit = DefaultSandboxCPULimit
	}
	if config.MemoryMiB == 0 {
		config.MemoryMiB = DefaultSandboxMemoryMiB
	}
	if config.CPULimit < MinSandboxCPULimit || config.CPULimit > MaxSandboxCPULimit {
		return SandboxConfig{}, fmt.Errorf("cpuLimit must be between %d and %d", MinSandboxCPULimit, MaxSandboxCPULimit)
	}
	if config.MemoryMiB < MinSandboxMemoryMiB || config.MemoryMiB > MaxSandboxMemoryMiB {
		return SandboxConfig{}, fmt.Errorf("memoryMiB must be between %d and %d", MinSandboxMemoryMiB, MaxSandboxMemoryMiB)
	}
	if config.IdleTimeoutMinutes < 0 || config.IdleTimeoutMinutes > MaxSandboxIdleTimeoutMinutes {
		return SandboxConfig{}, fmt.Errorf("idleTimeoutMinutes must be between 0 and %d", MaxSandboxIdleTimeoutMinutes)
	}
	return config, nil
}

// CreateRequest is the payload accepted by the create-workspace endpoint.
type CreateRequest struct {
	Name     string   `json:"name"`
	MainPath string   `json:"mainPath"`
	Folders  []string `json:"folders"`
	Icon     *Icon    `json:"icon,omitempty"`
}

// UpdateRequest is the editable subset of a workspace. The main folder is
// deliberately immutable because it owns the workspace's .echo directory.
// Folders contains additional folders only; the main folder is always kept as
// the first resolved workspace root.
type UpdateRequest struct {
	Name       string   `json:"name"`
	Folders    []string `json:"folders"`
	Icon       *Icon    `json:"icon,omitempty"`
	RemoveIcon bool     `json:"removeIcon,omitempty"`
}

// Icon carries an uploaded workspace icon image. Data is the raw file bytes and
// Ext is the detected extension (without a leading dot), e.g. "png".
type Icon struct {
	Data []byte `json:"data"`
	Ext  string `json:"ext"`
}

// workspaceFile is the on-disk shape of .echo/workspace.json.
type workspaceFile struct {
	Name                        string                      `json:"name"`
	MainPath                    string                      `json:"mainPath"`
	Folders                     []string                    `json:"folders"`
	SearchParentGitRepositories bool                        `json:"searchParentGitRepositories,omitempty"`
	LanguageServers             lspconfig.WorkspaceConfig   `json:"languageServers,omitempty"`
	Debug                       debugconfig.WorkspaceConfig `json:"debug,omitempty"`
	Sandbox                     *SandboxConfig              `json:"sandbox,omitempty"`
}

const (
	ConfigMalformed       = "invalid_workspace_config"
	ConfigMissing         = "workspace_config_missing"
	ConfigMainMismatch    = "workspace_main_mismatch"
	ConfigMainUnavailable = "workspace_main_unavailable"
)

var ErrWorkspaceNotFound = errors.New("workspace not found")

// ConfigError reports a stable workspace-configuration failure code while
// retaining the underlying filesystem or JSON error for logs and tests.
type ConfigError struct {
	Code    string
	Message string
	Cause   error
}

func (e *ConfigError) Error() string { return e.Message }
func (e *ConfigError) Unwrap() error { return e.Cause }

// Manager uses shared app data as the registration/locator store and each
// main folder's workspace.json as the authoritative workspace configuration.
type Manager struct {
	data *appdata.Store
}

// NewManager creates a Manager backed by the given app data store path.
func NewManager(path string) *Manager {
	return &Manager{data: appdata.NewStore(path)}
}

// NewManagerWithData creates a workspace manager that shares the same
// transactional app-data store as settings and authentication.
func NewManagerWithData(data *appdata.Store) *Manager {
	return &Manager{data: data}
}

// List returns all registered workspaces.
func (m *Manager) List() ([]Workspace, error) {
	f, err := m.data.Load()
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(f.Workspaces))
	missing := make(map[string]struct{})
	for _, w := range f.Workspaces {
		workspace, resolveErr := resolveRegisteredWorkspace(w)
		if resolveErr != nil {
			if isMissingWorkspaceConfig(resolveErr) {
				missing[w.ID] = struct{}{}
				continue
			}
			// Keep the workspace picker usable so a moved workspace can be
			// rebound. Operational lookups still return the configuration error.
			workspace = workspaceFromRegistration(w)
		}
		out = append(out, workspace)
	}
	if len(missing) > 0 {
		if err := m.pruneMissingWorkspaceConfigs(missing); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pruneMissingWorkspaceConfigs removes registrations whose main folder still
// exists but no longer owns workspace.json. Each candidate is resolved again
// inside the app-data transaction so a concurrently restored or rebound
// workspace is never removed from echo.json.
func (m *Manager) pruneMissingWorkspaceConfigs(candidates map[string]struct{}) error {
	return m.data.Update(func(f *appdata.File) error {
		kept := make([]appdata.Workspace, 0, len(f.Workspaces))
		for _, registration := range f.Workspaces {
			if _, candidate := candidates[registration.ID]; !candidate {
				kept = append(kept, registration)
				continue
			}
			if _, err := resolveRegisteredWorkspace(registration); !isMissingWorkspaceConfig(err) {
				kept = append(kept, registration)
				continue
			}
			if f.ActiveWorkspaceID == registration.ID {
				f.ActiveWorkspaceID = ""
			}
		}
		f.Workspaces = kept
		return nil
	})
}

func isMissingWorkspaceConfig(err error) bool {
	var configErr *ConfigError
	return errors.As(err, &configErr) && configErr.Code == ConfigMissing
}

// Create registers a new workspace or opens/rebinds an existing portable
// workspace. New configurations require every requested folder to exist;
// existing configurations retain temporarily unavailable additional folders.
func (m *Manager) Create(req CreateRequest) (Workspace, error) {
	requestedMain := strings.TrimSpace(req.MainPath)
	if requestedMain == "" {
		return Workspace{}, fmt.Errorf("main folder path is required")
	}
	mainPath, err := absolutePath("", requestedMain)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve main folder path: %w", err)
	}
	if err := requireDirectory(mainPath, ConfigMainUnavailable, "main workspace folder is unavailable"); err != nil {
		return Workspace{}, err
	}
	echoDir := filepath.Join(mainPath, EchoDirName)

	loadedFile, fileExists, err := readWorkspaceFile(echoDir)
	if err != nil {
		return Workspace{}, err
	}

	workspace := Workspace{MainPath: mainPath, Sandbox: DefaultSandboxConfig()}
	if fileExists {
		workspace, err = workspaceFromFile("", "", echoDir, mainPath, loadedFile)
		if err != nil {
			return Workspace{}, err
		}
	} else {
		workspace.Name = strings.TrimSpace(req.Name)
		if workspace.Name == "" {
			return Workspace{}, fmt.Errorf("workspace name is required")
		}
		workspace.Folders = []string{mainPath}
		for _, requested := range req.Folders {
			requested = strings.TrimSpace(requested)
			if requested == "" {
				continue
			}
			folder, resolveErr := absolutePath("", requested)
			if resolveErr != nil {
				return Workspace{}, fmt.Errorf("resolve workspace folder %q: %w", requested, resolveErr)
			}
			workspace.Folders = append(workspace.Folders, folder)
		}
		workspace.Folders = append([]string{mainPath}, cleanFolders(workspace.Folders[1:], mainPath)...)
		for _, folder := range workspace.Folders {
			if err := requireDirectory(folder, "", fmt.Sprintf("path %q is not accessible", folder)); err != nil {
				return Workspace{}, err
			}
		}
	}

	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create .echo folder: %w", err)
	}
	workspace.IconExt = detectIconExt(echoDir)
	if req.Icon != nil && len(req.Icon.Data) > 0 {
		ext := sanitizeExt(req.Icon.Ext)
		if ext == "" {
			return Workspace{}, fmt.Errorf("icon has an unsupported file extension")
		}
		if err := os.WriteFile(filepath.Join(echoDir, "icon."+ext), req.Icon.Data, 0o644); err != nil {
			return Workspace{}, fmt.Errorf("write icon: %w", err)
		}
		workspace.IconExt = ext
	}

	registrations, err := m.data.Load()
	if err != nil {
		return Workspace{}, err
	}
	registrationIndex, err := registrationForWorkspace(registrations.Workspaces, workspace, fileExists)
	if err != nil {
		return Workspace{}, err
	}
	if registrationIndex >= 0 {
		workspace.ID = registrations.Workspaces[registrationIndex].ID
	} else {
		workspace.ID = newID()
	}

	// Adding/rebinding is an explicit normalization point for legacy absolute
	// configs. Routine List/Get reads never rewrite the workspace file.
	if err := writeWorkspaceFile(echoDir, workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	if err := m.register(workspace, fileExists); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// Update changes the user-editable workspace fields while preserving the main
// folder and all unrelated workspace-owned configuration.
func (m *Manager) Update(id string, req UpdateRequest) (Workspace, error) {
	if req.Icon != nil && req.RemoveIcon {
		return Workspace{}, fmt.Errorf("icon and removeIcon cannot be used together")
	}
	workspace, ok, err := m.Get(id)
	if err != nil {
		return Workspace{}, err
	}
	if !ok {
		return Workspace{}, workspaceNotFound(id)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Workspace{}, fmt.Errorf("workspace name is required")
	}

	folders := []string{workspace.MainPath}
	for _, requested := range req.Folders {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			continue
		}
		folder, resolveErr := absolutePath("", requested)
		if resolveErr != nil {
			return Workspace{}, fmt.Errorf("resolve workspace folder %q: %w", requested, resolveErr)
		}
		if err := requireDirectory(folder, "", fmt.Sprintf("path %q is not accessible", folder)); err != nil {
			return Workspace{}, err
		}
		folders = append(folders, folder)
	}
	workspace.Name = name
	workspace.Folders = append([]string{workspace.MainPath}, cleanFolders(folders[1:], workspace.MainPath)...)

	registrations, err := m.data.Load()
	if err != nil {
		return Workspace{}, err
	}
	for _, candidate := range registrations.Workspaces {
		if candidate.ID == id {
			continue
		}
		candidateName := strings.TrimSpace(candidate.Name)
		if resolved, resolveErr := resolveRegisteredWorkspace(candidate); resolveErr == nil {
			candidateName = strings.TrimSpace(resolved.Name)
		}
		if strings.EqualFold(candidateName, name) {
			return Workspace{}, fmt.Errorf("a workspace named %q already exists", name)
		}
	}

	echoDir := filepath.Join(workspace.MainPath, EchoDirName)
	if req.RemoveIcon {
		if err := removeWorkspaceIcons(echoDir, ""); err != nil {
			return Workspace{}, err
		}
		workspace.IconExt = ""
	} else if req.Icon != nil {
		ext := sanitizeExt(req.Icon.Ext)
		if ext == "" || len(req.Icon.Data) == 0 {
			return Workspace{}, fmt.Errorf("icon has an unsupported file extension or no data")
		}
		temporary, err := os.CreateTemp(echoDir, ".icon-*")
		if err != nil {
			return Workspace{}, fmt.Errorf("create icon temp file: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err := temporary.Write(req.Icon.Data); err != nil {
			temporary.Close()
			return Workspace{}, fmt.Errorf("write icon temp file: %w", err)
		}
		if err := temporary.Chmod(0o644); err != nil {
			temporary.Close()
			return Workspace{}, fmt.Errorf("chmod icon temp file: %w", err)
		}
		if err := temporary.Close(); err != nil {
			return Workspace{}, fmt.Errorf("close icon temp file: %w", err)
		}
		targetPath := filepath.Join(echoDir, "icon."+ext)
		if err := os.Rename(temporaryPath, targetPath); err != nil {
			return Workspace{}, fmt.Errorf("replace icon: %w", err)
		}
		if err := removeWorkspaceIcons(echoDir, targetPath); err != nil {
			return Workspace{}, err
		}
		workspace.IconExt = ext
	}

	if err := writeWorkspaceFile(echoDir, workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	if err := m.data.Update(func(f *appdata.File) error {
		for _, candidate := range f.Workspaces {
			if candidate.ID != id && strings.EqualFold(strings.TrimSpace(candidate.Name), name) {
				return fmt.Errorf("a workspace named %q already exists", name)
			}
		}
		for index := range f.Workspaces {
			if f.Workspaces[index].ID == id {
				f.Workspaces[index] = workspaceRegistration(workspace)
				return nil
			}
		}
		return workspaceNotFound(id)
	}); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// Unregister removes a workspace from this machine without touching its main
// folder or .echo directory. If the active workspace is removed, the next
// resolvable registration is selected in list order, wrapping at the end.
func (m *Manager) Unregister(id string) (string, error) {
	activeID := ""
	err := m.data.Update(func(f *appdata.File) error {
		index := -1
		for candidateIndex := range f.Workspaces {
			if f.Workspaces[candidateIndex].ID == id {
				index = candidateIndex
				break
			}
		}
		if index < 0 {
			return workspaceNotFound(id)
		}
		f.Workspaces = append(f.Workspaces[:index], f.Workspaces[index+1:]...)
		if f.ActiveWorkspaceID != id {
			activeID = f.ActiveWorkspaceID
			return nil
		}
		f.ActiveWorkspaceID = ""
		for offset := 0; offset < len(f.Workspaces); offset++ {
			candidate := f.Workspaces[(index+offset)%len(f.Workspaces)]
			if _, resolveErr := resolveRegisteredWorkspace(candidate); resolveErr == nil {
				f.ActiveWorkspaceID = candidate.ID
				break
			}
		}
		activeID = f.ActiveWorkspaceID
		return nil
	})
	return activeID, err
}

// register adds or rebinds a workspace in shared app data, preserving all
// workspace-ID keyed state such as active selection and saved commands.
func (m *Manager) register(ws Workspace, allowRebind bool) error {
	return m.data.Update(func(f *appdata.File) error {
		index, err := registrationForWorkspace(f.Workspaces, ws, allowRebind)
		if err != nil {
			return err
		}
		stored := workspaceRegistration(ws)
		if index >= 0 {
			stored.ID = f.Workspaces[index].ID
			f.Workspaces[index] = stored
			return nil
		}
		f.Workspaces = append(f.Workspaces, stored)
		return nil
	})
}

// IconPath returns the path to a workspace's icon file, or "" when the
// workspace has no icon. The extension is auto-detected from the stored
// IconExt; if that is empty it scans the .echo directory for an icon.* file.
func (m *Manager) IconPath(id string) (string, error) {
	ws, ok, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if ws.IconExt != "" {
		return filepath.Join(ws.MainPath, EchoDirName, "icon."+ws.IconExt), nil
	}
	// Fall back to scanning for any icon.* file.
	matches, err := filepath.Glob(filepath.Join(ws.MainPath, EchoDirName, "icon.*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	return matches[0], nil
}

// Active returns the currently active workspace, or ok=false when none is set
// or the stored id no longer matches a registered workspace.
func (m *Manager) Active() (Workspace, bool, error) {
	f, err := m.data.Load()
	if err != nil {
		return Workspace{}, false, err
	}
	if f.ActiveWorkspaceID == "" {
		return Workspace{}, false, nil
	}
	for _, w := range f.Workspaces {
		if w.ID == f.ActiveWorkspaceID {
			workspace, resolveErr := resolveRegisteredWorkspace(w)
			return workspace, resolveErr == nil, resolveErr
		}
	}
	return Workspace{}, false, nil
}

// Get returns a registered workspace by id. Unlike Active, it does not depend
// on the process-wide last-opened workspace and is therefore safe for
// per-client chat sessions.
func (m *Manager) Get(id string) (Workspace, bool, error) {
	w, ok, err := m.find(id)
	if err != nil || !ok {
		return Workspace{}, ok, err
	}
	workspace, err := resolveRegisteredWorkspace(w)
	return workspace, err == nil, err
}

// ActiveID returns the last-opened registration ID without resolving its
// workspace-owned config. This keeps the workspace picker available when a
// moved or malformed workspace needs to be rebound.
func (m *Manager) ActiveID() (string, error) {
	f, err := m.data.Load()
	if err != nil {
		return "", err
	}
	return f.ActiveWorkspaceID, nil
}

// SetSearchParentGitRepositories updates the workspace-scoped parent
// repository discovery preference in both the shared app data and the
// workspace-owned settings file.
func (m *Manager) SetSearchParentGitRepositories(id string, enabled bool) (Workspace, error) {
	workspace, ok, err := m.Get(id)
	if err != nil {
		return Workspace{}, err
	}
	if !ok {
		return Workspace{}, fmt.Errorf("workspace %q not found", id)
	}
	workspace.SearchParentGitRepositories = enabled
	if err := writeWorkspaceFile(filepath.Join(workspace.MainPath, EchoDirName), workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	if err := m.data.Update(func(f *appdata.File) error {
		for index := range f.Workspaces {
			if f.Workspaces[index].ID == id {
				f.Workspaces[index] = workspaceRegistration(workspace)
				return nil
			}
		}
		return fmt.Errorf("workspace %q not found", id)
	}); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// SetLanguageServerConfig updates the portable language-server selection and
// workspace overrides without copying machine-global profiles into the project.
func (m *Manager) SetLanguageServerConfig(id string, config lspconfig.WorkspaceConfig) (Workspace, error) {
	workspace, ok, err := m.Get(id)
	if err != nil {
		return Workspace{}, err
	}
	if !ok {
		return Workspace{}, fmt.Errorf("workspace %q not found", id)
	}
	workspace.LanguageServers = config.Normalized()
	if err := writeWorkspaceFile(filepath.Join(workspace.MainPath, EchoDirName), workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// SetDebugConfig validates and persists the portable workspace debugger
// configuration without copying machine-local adapter profiles into the repo.
func (m *Manager) SetDebugConfig(id string, config debugconfig.WorkspaceConfig) (Workspace, error) {
	workspace, ok, err := m.Get(id)
	if err != nil {
		return Workspace{}, err
	}
	if !ok {
		return Workspace{}, fmt.Errorf("workspace %q not found", id)
	}
	config = config.Normalized()
	if err := config.ValidateStructure(); err != nil {
		return Workspace{}, err
	}
	workspace.Debug = config
	if err := writeWorkspaceFile(filepath.Join(workspace.MainPath, EchoDirName), workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// SetSandboxConfig validates and persists the portable sandbox configuration.
// Host-specific runtime state and credentials deliberately live elsewhere.
func (m *Manager) SetSandboxConfig(id string, config SandboxConfig) (Workspace, error) {
	workspace, ok, err := m.Get(id)
	if err != nil {
		return Workspace{}, err
	}
	if !ok {
		return Workspace{}, fmt.Errorf("workspace %q not found", id)
	}
	normalized, err := NormalizeSandboxConfig(config)
	if err != nil {
		return Workspace{}, err
	}
	workspace.Sandbox = normalized
	workspace.sandboxConfigured = true
	if err := writeWorkspaceFile(filepath.Join(workspace.MainPath, EchoDirName), workspaceFileFromWorkspace(workspace)); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// SetActive records the given workspace id as the active (last opened)
// workspace, preserving settings and the workspace list.
func (m *Manager) SetActive(id string) error {
	if _, ok, err := m.Get(id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("workspace %q not found", id)
	}
	return m.data.Update(func(f *appdata.File) error {
		for _, w := range f.Workspaces {
			if w.ID == id {
				f.ActiveWorkspaceID = id
				return nil
			}
		}
		return fmt.Errorf("workspace %q not found", id)
	})
}

func workspaceFromRegistration(w appdata.Workspace) Workspace {
	mainPath, _ := absolutePath("", w.MainPath)
	folders := make([]string, 0, len(w.Folders)+1)
	if mainPath != "" {
		folders = append(folders, mainPath)
	}
	for _, folder := range w.Folders {
		if absolute, err := absolutePath("", folder); err == nil {
			folders = append(folders, absolute)
		}
	}
	if mainPath != "" {
		folders = append([]string{mainPath}, cleanFolders(folders[1:], mainPath)...)
	}
	return Workspace{
		ID: w.ID, Name: w.Name, MainPath: mainPath, IconExt: w.IconExt, Folders: folders,
		SearchParentGitRepositories: w.SearchParentGitRepositories,
		Sandbox:                     DefaultSandboxConfig(),
	}
}

func resolveRegisteredWorkspace(w appdata.Workspace) (Workspace, error) {
	mainPath, err := absolutePath("", w.MainPath)
	if err != nil {
		return Workspace{}, &ConfigError{Code: ConfigMainUnavailable, Message: "main workspace folder is unavailable", Cause: err}
	}
	if err := requireDirectory(mainPath, ConfigMainUnavailable, "main workspace folder is unavailable"); err != nil {
		return Workspace{}, err
	}
	echoDir := filepath.Join(mainPath, EchoDirName)
	wf, exists, err := readWorkspaceFile(echoDir)
	if err != nil {
		return Workspace{}, err
	}
	if !exists {
		return Workspace{}, &ConfigError{Code: ConfigMissing, Message: "workspace config is missing", Cause: os.ErrNotExist}
	}
	return workspaceFromFile(w.ID, w.IconExt, echoDir, mainPath, wf)
}

func workspaceFromFile(id, iconExt, echoDir, expectedMain string, wf workspaceFile) (Workspace, error) {
	name := strings.TrimSpace(wf.Name)
	if name == "" {
		return Workspace{}, &ConfigError{Code: ConfigMalformed, Message: "workspace config name is required"}
	}
	mainPath, err := absolutePath(echoDir, wf.MainPath)
	if err != nil || strings.TrimSpace(wf.MainPath) == "" {
		return Workspace{}, &ConfigError{Code: ConfigMalformed, Message: "workspace config mainPath is invalid", Cause: err}
	}
	if folderIdentity(mainPath) != folderIdentity(expectedMain) {
		return Workspace{}, &ConfigError{Code: ConfigMainMismatch, Message: "workspace config mainPath does not identify the folder that owns .echo"}
	}
	folders := []string{expectedMain}
	for _, configured := range wf.Folders {
		configured = strings.TrimSpace(configured)
		if configured == "" {
			continue
		}
		folder, resolveErr := absolutePath(echoDir, configured)
		if resolveErr != nil {
			return Workspace{}, &ConfigError{Code: ConfigMalformed, Message: fmt.Sprintf("workspace folder path %q is invalid", configured), Cause: resolveErr}
		}
		folders = append(folders, folder)
	}
	folders = append([]string{expectedMain}, cleanFolders(folders[1:], expectedMain)...)
	sandboxConfig := DefaultSandboxConfig()
	sandboxConfigured := wf.Sandbox != nil
	if sandboxConfigured {
		sandboxConfig, err = NormalizeSandboxConfig(*wf.Sandbox)
		if err != nil {
			return Workspace{}, &ConfigError{Code: ConfigMalformed, Message: "workspace sandbox configuration is invalid", Cause: err}
		}
	}
	debugConfig := wf.Debug.Normalized()
	if err := debugConfig.ValidateStructure(); err != nil {
		return Workspace{}, &ConfigError{Code: ConfigMalformed, Message: "workspace debugger configuration is invalid", Cause: err}
	}
	return Workspace{
		ID: id, Name: name, MainPath: expectedMain, IconExt: iconExt, Folders: folders,
		SearchParentGitRepositories: wf.SearchParentGitRepositories,
		LanguageServers:             wf.LanguageServers.Normalized(),
		Debug:                       debugConfig,
		Sandbox:                     sandboxConfig,
		sandboxConfigured:           sandboxConfigured,
	}, nil
}

func readWorkspaceFile(echoDir string) (workspaceFile, bool, error) {
	path := filepath.Join(echoDir, "workspace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return workspaceFile{}, false, nil
		}
		return workspaceFile{}, false, &ConfigError{Code: ConfigMalformed, Message: "read workspace config", Cause: err}
	}
	var wf workspaceFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return workspaceFile{}, true, &ConfigError{Code: ConfigMalformed, Message: "parse workspace config", Cause: err}
	}
	return wf, true, nil
}

func registrationForWorkspace(existing []appdata.Workspace, workspace Workspace, allowRebind bool) (int, error) {
	pathIndex, nameIndex := -1, -1
	for index, candidate := range existing {
		candidateName := candidate.Name
		if resolved, err := resolveRegisteredWorkspace(candidate); err == nil {
			candidateName = resolved.Name
		}
		if strings.TrimSpace(candidate.MainPath) != "" && folderIdentity(candidate.MainPath) == folderIdentity(workspace.MainPath) {
			pathIndex = index
		}
		if strings.EqualFold(strings.TrimSpace(candidateName), workspace.Name) {
			nameIndex = index
		}
	}
	if pathIndex >= 0 && nameIndex >= 0 && pathIndex != nameIndex {
		return -1, fmt.Errorf("workspace path and name belong to different registrations")
	}
	matched := pathIndex
	if matched < 0 {
		matched = nameIndex
	}
	if matched >= 0 && !allowRebind {
		if nameIndex >= 0 {
			return -1, fmt.Errorf("a workspace named %q already exists", workspace.Name)
		}
		return -1, fmt.Errorf("workspace folder %q is already registered", workspace.MainPath)
	}
	return matched, nil
}

func workspaceRegistration(ws Workspace) appdata.Workspace {
	return appdata.Workspace{
		ID: ws.ID, Name: ws.Name, MainPath: ws.MainPath, IconExt: ws.IconExt,
		Folders:                     append([]string(nil), ws.Folders...),
		SearchParentGitRepositories: ws.SearchParentGitRepositories,
	}
}

func workspaceFileFromWorkspace(ws Workspace) workspaceFile {
	wf := workspaceFile{
		Name: ws.Name, MainPath: ws.MainPath, Folders: append([]string(nil), ws.Folders...),
		SearchParentGitRepositories: ws.SearchParentGitRepositories,
		LanguageServers:             ws.LanguageServers.Normalized(),
		Debug:                       ws.Debug.Normalized(),
	}
	if ws.sandboxConfigured {
		config := ws.Sandbox
		wf.Sandbox = &config
	}
	return wf
}

func absolutePath(base, value string) (string, error) {
	value = filepath.FromSlash(strings.TrimSpace(value))
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(value) && base != "" {
		value = filepath.Join(base, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func requireDirectory(path, code, message string) error {
	info, err := os.Stat(path)
	if err != nil {
		if code != "" {
			return &ConfigError{Code: code, Message: message, Cause: err}
		}
		return fmt.Errorf("%s: %w", message, err)
	}
	if !info.IsDir() {
		if code != "" {
			return &ConfigError{Code: code, Message: message, Cause: fmt.Errorf("not a directory")}
		}
		return fmt.Errorf("%s: not a folder", message)
	}
	return nil
}

func detectIconExt(echoDir string) string {
	matches, _ := filepath.Glob(filepath.Join(echoDir, "icon.*"))
	for _, match := range matches {
		if ext := sanitizeExt(filepath.Ext(match)); ext != "" {
			return ext
		}
	}
	return ""
}

func removeWorkspaceIcons(echoDir, keep string) error {
	matches, err := filepath.Glob(filepath.Join(echoDir, "icon.*"))
	if err != nil {
		return fmt.Errorf("find workspace icons: %w", err)
	}
	for _, match := range matches {
		if keep != "" && filepath.Clean(match) == filepath.Clean(keep) {
			continue
		}
		if sanitizeExt(filepath.Ext(match)) == "" {
			continue
		}
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove workspace icon: %w", err)
		}
	}
	return nil
}

func (m *Manager) find(id string) (appdata.Workspace, bool, error) {
	f, err := m.data.Load()
	if err != nil {
		return appdata.Workspace{}, false, err
	}
	for _, w := range f.Workspaces {
		if w.ID == id {
			return w, true, nil
		}
	}
	return appdata.Workspace{}, false, nil
}

func workspaceNotFound(id string) error {
	return fmt.Errorf("%w: %q", ErrWorkspaceNotFound, id)
}

// writeWorkspaceFile writes the workspace settings JSON atomically.
func writeWorkspaceFile(echoDir string, wf workspaceFile) error {
	portable := wf
	portable.MainPath = portableWorkspacePath(echoDir, wf.MainPath)
	portable.Folders = make([]string, 0, len(wf.Folders))
	for _, folder := range wf.Folders {
		portable.Folders = append(portable.Folders, portableWorkspacePath(echoDir, folder))
	}
	data, err := json.MarshalIndent(portable, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace file: %w", err)
	}
	path := filepath.Join(echoDir, "workspace.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write workspace file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename workspace file: %w", err)
	}
	return nil
}

func portableWorkspacePath(echoDir, folder string) string {
	configured := strings.TrimSpace(folder)
	folder, err := absolutePath("", configured)
	if err != nil {
		return filepath.Clean(configured)
	}
	if relative, relErr := filepath.Rel(echoDir, folder); relErr == nil {
		relative = filepath.ToSlash(relative)
		if relative == ".." {
			return "../"
		}
		return relative
	}
	return filepath.Clean(folder)
}

// cleanFolders trims whitespace and drops empty/duplicate entries, excluding
// the main path.
func cleanFolders(folders []string, mainPath string) []string {
	seen := map[string]bool{folderIdentity(mainPath): true}
	var out []string
	for _, f := range folders {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		identity := folderIdentity(f)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		out = append(out, f)
	}
	return out
}

func folderIdentity(folder string) string {
	folder = filepath.Clean(strings.TrimSpace(folder))
	if absolute, err := filepath.Abs(folder); err == nil {
		folder = absolute
	}
	if real, err := filepath.EvalSymlinks(folder); err == nil {
		folder = real
	}
	if runtime.GOOS == "windows" {
		folder = strings.ToLower(folder)
	}
	return folder
}

// sanitizeExt normalizes an icon extension to a safe lowercase value without a
// leading dot, or returns "" if it is unsupported.
func sanitizeExt(ext string) string {
	ext = strings.TrimSpace(ext)
	ext = strings.TrimPrefix(ext, ".")
	ext = strings.ToLower(ext)
	switch ext {
	case "png", "gif", "jpeg", "jpg", "webp", "bmp", "svg", "ico":
		return ext
	}
	return ""
}

// newID returns a short unique identifier for a workspace.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ws-%d", os.Getpid())
	}
	return "ws-" + hex.EncodeToString(b[:])
}
