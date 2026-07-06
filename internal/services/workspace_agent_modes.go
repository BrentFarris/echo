package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brent/echo/internal/tools"
	"github.com/google/uuid"
)

const workspaceModeFileName = "mode.json"

// workspaceModeFile is the on-disk representation of a user-defined agent mode.
type workspaceModeFile struct {
	ID              string                          `json:"id"`
	Name            string                          `json:"name"`
	Prompt          string                          `json:"prompt"`
	Permissions     map[string]tools.ToolPermission `json:"permissions,omitempty"`
	ToolPermissions []string                        `json:"toolPermissions,omitempty"` // deprecated
	PathPermissions []string                        `json:"pathPermissions,omitempty"` // deprecated
}

// workspaceModeExistingRoot returns the root directory for agent modes in a folder.
func workspaceModeExistingRoot(workspaceID string, folder WorkspaceFolder) (string, error) {
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", err
	}
	cacheRoot := filepath.Join(root, workspaceCacheDirName)
	modesDirName := workspaceModeDirName(workspaceID)
	modesRoot := filepath.Join(cacheRoot, modesDirName)
	info, err := os.Lstat(modesRoot)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("workspace modes directory %s is not a valid directory", modesRoot)
	}
	return modesRoot, nil
}

// workspaceModeExistingPath returns the path to an existing mode's JSON file.
func workspaceModeExistingPath(workspaceID string, folder WorkspaceFolder, modeID string) (string, error) {
	root, err := workspaceModeExistingRoot(workspaceID, folder)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, modeID)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("workspace mode directory %s is not a valid directory", dir)
	}
	path := filepath.Join(dir, workspaceModeFileName)
	info, err = os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("workspace mode file must be a regular file")
	}
	return path, nil
}

// loadWorkspaceModeFile loads and validates a mode JSON file.
func loadWorkspaceModeFile(folder WorkspaceFolder, modeID string, path string) (AgentMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentMode{}, fmt.Errorf("read workspace mode: %w", err)
	}
	if len(data) == 0 {
		return AgentMode{}, fmt.Errorf("workspace mode file is empty")
	}
	var raw workspaceModeFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentMode{}, fmt.Errorf("parse workspace mode: %w", err)
	}
	// Validate ID matches directory name.
	if raw.ID != modeID {
		return AgentMode{}, fmt.Errorf("mode id %q does not match directory %q", raw.ID, modeID)
	}
	mode := AgentMode{
		ID:              raw.ID,
		Name:            raw.Name,
		Prompt:          raw.Prompt,
		Permissions:     raw.Permissions,
		BuiltIn:         false,
		ToolPermissions: raw.ToolPermissions,
		PathPermissions: raw.PathPermissions,
	}
	migrateAgentMode(&mode)
	return mode, nil
}

// writeWorkspaceModeFile atomically writes a mode JSON file.
func writeWorkspaceModeFile(workspaceID string, folder WorkspaceFolder, mode AgentMode) (string, error) {
	data, err := json.MarshalIndent(workspaceModeFile{
		ID:              mode.ID,
		Name:            mode.Name,
		Prompt:          mode.Prompt,
		Permissions:     mode.Permissions,
		ToolPermissions: mode.ToolPermissions,
		PathPermissions: mode.PathPermissions,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal workspace mode: %w", err)
	}

	target, err := workspaceModeCachePath(workspaceID, folder, filepath.ToSlash(filepath.Join(mode.ID, workspaceModeFileName)))
	if err != nil {
		return "", err
	}

	parent := filepath.Dir(target)
	temp, err := os.CreateTemp(parent, ".mode-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary workspace mode: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(workspaceCacheFilePermission); err != nil {
		temp.Close()
		return "", fmt.Errorf("set workspace mode permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("write temporary workspace mode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("sync temporary workspace mode: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary workspace mode: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return "", fmt.Errorf("replace workspace mode: %w", err)
	}
	return target, nil
}

// catalogWorkspaceModes loads all user-defined modes from disk for a workspace.
func catalogWorkspaceModes(workspace Workspace) ([]AgentMode, []string) {
	var modes []AgentMode
	var warnings []string

	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		root, err := workspaceModeExistingRoot(workspace.ID, folder)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", folder.Label, err))
			continue
		}
		children, err := os.ReadDir(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: read modes: %v", folder.Label, err))
			continue
		}
		for _, child := range children {
			name := child.Name()
			// Validate that the directory name looks like a UUID (36 chars).
			if len(name) != 36 {
				continue
			}
			childPath := filepath.Join(root, name)
			info, err := os.Lstat(childPath)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			path := filepath.Join(childPath, workspaceModeFileName)
			info, err = os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				continue
			}
			mode, err := loadWorkspaceModeFile(folder, name, path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s/%s: %v", folder.Label, name, err))
				continue
			}
			modes = append(modes, mode)
		}
	}

	sort.SliceStable(modes, func(i, j int) bool {
		return modes[i].ID < modes[j].ID
	})
	return modes, warnings
}

// workspaceModeCreate stores a new user-defined agent mode on disk for the active workspace.
func (s *SystemService) workspaceModeCreate(workspace Workspace, name, prompt string, permissions map[string]tools.ToolPermission) (AgentMode, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentMode{}, fmt.Errorf("agent mode name is required")
	}

	// Check uniqueness across disk + built-ins.
	allModes := s.listAllWorkspaceModes(workspace)
	if exists := agentModeNameExists(allModes, name); exists {
		return AgentMode{}, fmt.Errorf("an agent mode with the name %q already exists", name)
	}

	mode := AgentMode{
		ID:          uuid.NewString(),
		Name:        name,
		Prompt:      strings.TrimSpace(prompt),
		Permissions: permissions,
		BuiltIn:     false,
	}

	// Write to first available workspace folder.
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		if _, err := writeWorkspaceModeFile(workspace.ID, folder, mode); err == nil {
			return mode, nil
		} else {
			return AgentMode{}, err
		}
	}
	return AgentMode{}, fmt.Errorf("workspace has no available folders to store agent modes")
}

// workspaceModeUpdate updates an existing user-defined mode on disk.
func (s *SystemService) workspaceModeUpdate(workspace Workspace, id, name, prompt string, permissions map[string]tools.ToolPermission) (AgentMode, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AgentMode{}, fmt.Errorf("agent mode id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentMode{}, fmt.Errorf("agent mode name is required")
	}

	// Find existing mode on disk and track which folder it's in.
	var targetFolder WorkspaceFolder
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		_, err := workspaceModeExistingPath(workspace.ID, folder, id)
		if err == nil {
			targetFolder = folder
			break
		}
	}
	if targetFolder.ID == "" {
		return AgentMode{}, fmt.Errorf("agent mode was not found")
	}

	mode := AgentMode{
		ID:          id,
		Name:        name,
		Prompt:      strings.TrimSpace(prompt),
		Permissions: permissions,
		BuiltIn:     false,
	}

	// Check uniqueness against all modes.
	allModes := s.listAllWorkspaceModes(workspace)
	for _, m := range allModes {
		if m.ID != id && strings.EqualFold(m.Name, name) {
			return AgentMode{}, fmt.Errorf("an agent mode with the name %q already exists", name)
		}
	}

	if _, err := writeWorkspaceModeFile(workspace.ID, targetFolder, mode); err != nil {
		return AgentMode{}, err
	}
	return mode, nil
}

// workspaceModeDelete removes a user-defined mode from disk.
func (s *SystemService) workspaceModeDelete(workspace Workspace, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("agent mode id is required")
	}

		for _, folder := range workspace.Folders {
			if folder.Missing {
				continue
			}
			root, err := workspaceModeExistingRoot(workspace.ID, folder)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			continue
		}
		dir := filepath.Join(root, id)
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("delete workspace mode: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("agent mode was not found")
}

// listAllWorkspaceModes returns built-in modes + disk-loaded user modes for a workspace.
func (s *SystemService) listAllWorkspaceModes(workspace Workspace) []AgentMode {
	diskModes, _ := catalogWorkspaceModes(workspace)
	result := make([]AgentMode, 0, len(DefaultAgentModes())+len(diskModes))
	result = append(result, DefaultAgentModes()...)
	result = append(result, diskModes...)
	return result
}
