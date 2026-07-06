package services

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/brent/echo/internal/tools"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AgentMode defines a named chat mode with its own system prompt and
// tool/path permission boundaries.
type AgentMode struct {
	ID              string                                    `json:"id"`
	Name            string                                    `json:"name"`
	Prompt          string                                    `json:"prompt"`
	Permissions     map[string]tools.ToolPermission           `json:"permissions,omitempty"`
	BuiltIn         bool                                      `json:"builtIn"`
	ToolPermissions []string                                  `json:"toolPermissions,omitempty"` // deprecated: use Permissions
	PathPermissions []string                                  `json:"pathPermissions,omitempty"` // deprecated: use Permissions
}

const agentModeEventName = "echo:agent-mode:event"

// Default agent mode IDs.
const (
	AgentModeIDGeneral = "general"
	AgentModeIDPlan    = "plan"
)

// migrateAgentMode converts legacy flat permission lists to the new per-tool
// Permissions map format.  If Permissions is already populated it is a no-op.
func migrateAgentMode(mode *AgentMode) {
	if len(mode.Permissions) > 0 {
		return // already migrated
	}

	// Build per-tool permissions from flat lists.
	for _, toolName := range mode.ToolPermissions {
		name := strings.TrimSpace(toolName)
		if name == "" {
			continue
		}
		if mode.Permissions == nil {
			mode.Permissions = make(map[string]tools.ToolPermission)
		}
		mode.Permissions[name] = tools.ToolPermission{
			Name:  name,
			Paths: cloneStrings(mode.PathPermissions),
		}
	}

	// Clear legacy fields after migration.
	mode.ToolPermissions = nil
	mode.PathPermissions = nil
}

// UnmarshalJSON implements custom JSON unmarshaling for AgentMode to auto-migrate
// legacy flat permission lists to the new Permissions map format.
func (m *AgentMode) UnmarshalJSON(data []byte) error {
	type raw AgentMode
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*m = AgentMode(r)
	migrateAgentMode(m)
	return nil
}

// DefaultAgentModes returns the built-in modes that ship with Echo.
func DefaultAgentModes() []AgentMode {
	return []AgentMode{
		{
			ID:      AgentModeIDGeneral,
			Name:    "General",
			Prompt:  "",
			BuiltIn: true,
		},
		{
			ID:      AgentModeIDPlan,
			Name:    "Plan",
			Prompt:  "",
			BuiltIn: true,
		},
	}
}

// resolveWorkspaceLocked looks up a workspace by ID without locking.
// Caller must hold s.mu.
func (s *SystemService) resolveWorkspaceLocked(workspaceID string) Workspace {
	for _, w := range s.state.Workspaces {
		if w.ID == workspaceID {
			return w
		}
	}
	return Workspace{}
}

// ListAgentModes returns a clone of the current agent modes for the given
// workspace. If workspaceID is empty, it falls back to the active workspace.
func (s *SystemService) ListAgentModes(workspaceID string) []AgentMode {
	s.mu.Lock()
	defer s.mu.Unlock()

	if workspaceID == "" {
		return s.listAgentModesLocked()
	}

	workspace := s.resolveWorkspaceLocked(workspaceID)
	if workspace.ID == "" {
		return cloneAgentModes(DefaultAgentModes())
	}
	all := s.listAllWorkspaceModes(workspace)
	return cloneAgentModes(all)
}

func (s *SystemService) listAgentModesLocked() []AgentMode {
	if s.state.ActiveWorkspaceID == "" {
		return cloneAgentModes(DefaultAgentModes())
	}
	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return cloneAgentModes(DefaultAgentModes())
	}
	all := s.listAllWorkspaceModes(workspace)
	return cloneAgentModes(all)
}

// CreateAgentMode adds a new user-defined agent mode and returns the updated list.
// It validates that the name is unique across all modes (case-insensitive).
func (s *SystemService) CreateAgentMode(name, prompt string, toolPermissions, pathPermissions []string) ([]AgentMode, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent mode name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return nil, fmt.Errorf("no active workspace to store agent modes")
	}

	permissions := buildPermissionsMap(toolPermissions, pathPermissions)
	if _, err := s.workspaceModeCreate(workspace, name, prompt, permissions); err != nil {
		return nil, err
	}

	result := s.listAgentModesLocked()
	s.emitAgentModeEvent(result)
	return result, nil
}

// UpdateAgentMode updates an existing agent mode by ID.
// Built-in modes cannot be updated. Name uniqueness is validated against other modes.
func (s *SystemService) UpdateAgentMode(id, name, prompt string, toolPermissions, pathPermissions []string) ([]AgentMode, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("agent mode id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent mode name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return nil, fmt.Errorf("no active workspace to store agent modes")
	}

	// Check if it's a built-in mode.
	for _, m := range DefaultAgentModes() {
		if m.ID == id {
			return nil, fmt.Errorf("built-in agent modes cannot be updated")
		}
	}

	permissions := buildPermissionsMap(toolPermissions, pathPermissions)
	if _, err := s.workspaceModeUpdate(workspace, id, name, prompt, permissions); err != nil {
		return nil, err
	}

	result := s.listAgentModesLocked()
	s.emitAgentModeEvent(result)
	return result, nil
}

// CreateAgentModePerTool adds a new user-defined agent mode with per-tool
// path permissions and returns the updated list. It validates that the name is
// unique across all modes (case-insensitive).
// The permissions map keys are tool names; values are glob path patterns.
// An empty paths array for a tool means "allow all paths" for that tool.
func (s *SystemService) CreateAgentModePerTool(name, prompt string, permissions map[string][]string) ([]AgentMode, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent mode name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return nil, fmt.Errorf("no active workspace to store agent modes")
	}

	permMap := buildPermissionsMapFromPerTool(permissions)
	if _, err := s.workspaceModeCreate(workspace, name, prompt, permMap); err != nil {
		return nil, err
	}

	result := s.listAgentModesLocked()
	s.emitAgentModeEvent(result)
	return result, nil
}

// UpdateAgentModePerTool updates an existing agent mode by ID with per-tool
// path permissions. Built-in modes cannot be updated. Name uniqueness is
// validated against other modes.
func (s *SystemService) UpdateAgentModePerTool(id, name, prompt string, permissions map[string][]string) ([]AgentMode, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("agent mode id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent mode name is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return nil, fmt.Errorf("no active workspace to store agent modes")
	}

	// Check if it's a built-in mode.
	for _, m := range DefaultAgentModes() {
		if m.ID == id {
			return nil, fmt.Errorf("built-in agent modes cannot be updated")
		}
	}

	permMap := buildPermissionsMapFromPerTool(permissions)
	if _, err := s.workspaceModeUpdate(workspace, id, name, prompt, permMap); err != nil {
		return nil, err
	}

	result := s.listAgentModesLocked()
	s.emitAgentModeEvent(result)
	return result, nil
}

// buildPermissionsMapFromPerTool converts a per-tool path map into the internal
// Permissions map format. Each key is a tool name; values are glob patterns.
func buildPermissionsMapFromPerTool(permissions map[string][]string) map[string]tools.ToolPermission {
	if len(permissions) == 0 {
		return nil
	}
	result := make(map[string]tools.ToolPermission, len(permissions))
	for name, paths := range permissions {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		result[n] = tools.ToolPermission{
			Name:  n,
			Paths: cloneStrings(paths),
		}
	}
	return result
}

// createAgentModeFromGenerated creates an agent mode from the LLM-synthesized
// generatedAgentMode struct. It prefers the new Permissions map format and
// falls back to the flat tool/path lists for backward compatibility.
func (s *SystemService) createAgentModeFromGenerated(generated generatedAgentMode) ([]AgentMode, error) {
	if len(generated.Permissions) > 0 {
		// New per-tool permissions format.
		perToolMap := make(map[string][]string, len(generated.Permissions))
		for name, perm := range generated.Permissions {
			perToolMap[name] = cloneStrings(perm.Paths)
		}
		return s.CreateAgentModePerTool(generated.Name, generated.Prompt, perToolMap)
	}
	// Fallback to flat format for backward compatibility.
	return s.CreateAgentMode(generated.Name, generated.Prompt, generated.ToolPermissions, generated.PathPermissions)
}

// DeleteAgentMode removes a user-defined agent mode by ID.
// Built-in modes are protected from deletion.
func (s *SystemService) DeleteAgentMode(id string) ([]AgentMode, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("agent mode id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Protect built-in modes.
	for _, m := range DefaultAgentModes() {
		if m.ID == id {
			return nil, fmt.Errorf("built-in agent modes cannot be deleted")
		}
	}

	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return nil, fmt.Errorf("no active workspace to store agent modes")
	}

	if err := s.workspaceModeDelete(workspace, id); err != nil {
		return nil, err
	}

	result := s.listAgentModesLocked()
	s.emitAgentModeEvent(result)
	return result, nil
}

// migrateGlobalAgentModesToDisk migrates any remaining global agent modes from
// the stored state to disk storage in the first available workspace. This runs
// once on load and clears the global list afterward.
func (s *SystemService) migrateGlobalAgentModesToDisk(stored []AgentMode) {
	if len(stored) == 0 {
		return
	}

	// Separate user-defined modes from built-ins.
	var userModes []AgentMode
	for _, mode := range stored {
		if !mode.BuiltIn {
			userModes = append(userModes, mode)
		}
	}
	if len(userModes) == 0 {
		return
	}

	// Find first available workspace.
	if len(s.state.Workspaces) == 0 {
		return
	}
	workspace := s.state.Workspaces[0]

	for _, mode := range userModes {
		mode.ID = uuid.New().String()
		for _, folder := range workspace.Folders {
			if folder.Missing {
				continue
			}
			// Ensure cache directories exist before writing.
			cache, err := ensureWorkspaceFolderCache(workspace.ID, folder)
			if err != nil {
				continue
			}
			_ = cache // suppress unused variable warning
			if _, err := writeWorkspaceModeFile(workspace.ID, folder, mode); err == nil {
				break
			}
		}
	}
}

// resolveAgentMode looks up an agent mode by ID in the service's current state.
// If the ID is empty or not found, it falls back to the general built-in mode.
// Returns the resolved mode and its canonical ID.
func (s *SystemService) resolveAgentMode(id string) (AgentMode, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id = strings.TrimSpace(id)
	if id == "" {
		id = AgentModeIDGeneral
	}

	all := s.listAllWorkspaceModesForResolve()
	for _, mode := range all {
		if mode.ID == id {
			result := normalizeAgentModeLegacyFields(mode)
			return result, mode.ID
		}
	}

	// Fallback: return the general built-in mode.
	return AgentMode{
		ID:      AgentModeIDGeneral,
		Name:    "General",
		Prompt:  "",
		BuiltIn: true,
	}, AgentModeIDGeneral
}

func (s *SystemService) listAllWorkspaceModesForResolve() []AgentMode {
	if s.state.ActiveWorkspaceID == "" {
		return DefaultAgentModes()
	}
	workspace := s.resolveWorkspaceLocked(s.state.ActiveWorkspaceID)
	if workspace.ID == "" {
		return DefaultAgentModes()
	}
	return s.listAllWorkspaceModes(workspace)
}

// normalizeAgentModeLegacyFields returns a copy of the mode with legacy
// ToolPermissions and PathPermissions populated from the Permissions map.
func normalizeAgentModeLegacyFields(mode AgentMode) AgentMode {
	if len(mode.Permissions) > 0 && (mode.ToolPermissions == nil || len(mode.ToolPermissions) == 0) {
		mode.ToolPermissions = permissionsMapToolNames(mode.Permissions)
	}
	if len(mode.Permissions) > 0 && (mode.PathPermissions == nil || len(mode.PathPermissions) == 0) {
		mode.PathPermissions = permissionsMapPaths(mode.Permissions)
	}
	return mode
}

// agentModeNameExists reports whether a mode with the given name already exists
// in the list (case-insensitive comparison).
func agentModeNameExists(modes []AgentMode, name string) bool {
	lower := strings.ToLower(name)
	for _, m := range modes {
		if strings.ToLower(m.Name) == lower {
			return true
		}
	}
	return false
}

// cloneAgentModes returns a deep copy of the agent mode slice.
func cloneAgentModes(src []AgentMode) []AgentMode {
	if src == nil {
		return []AgentMode{}
	}
	dst := make([]AgentMode, len(src))
	for i, m := range src {
		dst[i] = AgentMode{
			ID:      m.ID,
			Name:    m.Name,
			Prompt:  m.Prompt,
			BuiltIn: m.BuiltIn,
		}
		if len(m.Permissions) > 0 {
			dst[i].Permissions = cloneToolPermissionMap(m.Permissions)
			// Populate legacy fields from Permissions for backward-compatible API responses.
			dst[i].ToolPermissions = permissionsMapToolNames(m.Permissions)
			dst[i].PathPermissions = permissionsMapPaths(m.Permissions)
		} else {
			// No Permissions map; preserve legacy fields directly.
			if len(m.ToolPermissions) > 0 {
				dst[i].ToolPermissions = cloneStrings(m.ToolPermissions)
			}
			if len(m.PathPermissions) > 0 {
				dst[i].PathPermissions = cloneStrings(m.PathPermissions)
			}
		}
	}
	return dst
}

// permissionsMapToolNames extracts the sorted tool names from a Permissions map.
func permissionsMapToolNames(permissions map[string]tools.ToolPermission) []string {
	if len(permissions) == 0 {
		return nil
	}
	names := make([]string, 0, len(permissions))
	for name := range permissions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// permissionsMapPaths extracts the path patterns from a Permissions map.
// All tools share the same PathMatcher from flat migration, so we extract from
// the first entry with non-empty paths.
func permissionsMapPaths(permissions map[string]tools.ToolPermission) []string {
	for _, perm := range permissions {
		if len(perm.Paths) > 0 {
			return cloneStrings(perm.Paths)
		}
	}
	return nil
}

// buildPermissionsMap converts flat tool/path permission lists into the new
// per-tool Permissions map format.
func buildPermissionsMap(toolPermissions, pathPermissions []string) map[string]tools.ToolPermission {
	if len(toolPermissions) == 0 {
		return nil
	}
	result := make(map[string]tools.ToolPermission, len(toolPermissions))
	for _, name := range toolPermissions {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		result[n] = tools.ToolPermission{
			Name:  n,
			Paths: cloneStrings(pathPermissions),
		}
	}
	return result
}

// buildToolScopes converts a Permissions map into a ToolScopeChecker.
// A nil or empty map produces an allow-all checker.
func buildToolScopes(permissions map[string]tools.ToolPermission) *tools.ToolScopeChecker {
	if len(permissions) == 0 {
		return tools.NewToolScopeChecker(nil)
	}
	slice := make([]tools.ToolPermission, 0, len(permissions))
	for _, perm := range permissions {
		slice = append(slice, perm)
	}
	return tools.NewToolScopeChecker(slice)
}

// cloneToolPermissionMap returns a deep copy of the permissions map.
func cloneToolPermissionMap(src map[string]tools.ToolPermission) map[string]tools.ToolPermission {
	if src == nil {
		return nil
	}
	dst := make(map[string]tools.ToolPermission, len(src))
	for name, perm := range src {
		p := perm // copy struct
		dst[name] = p
	}
	return dst
}

// cloneStrings returns a copy of the string slice.
func cloneStrings(src []string) []string {
	if src == nil {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

func (s *SystemService) emitAgentModeEvent(event any) {
	s.emitRuntimeEvent(agentModeEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, agentModeEventName, event)
	}
}
