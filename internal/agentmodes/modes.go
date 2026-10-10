// Package agentmodes manages workspace-scoped chat modes.
package agentmodes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/brent/echo/internal/tools"
)

const (
	GeneralID         = "general"
	PlanID            = "plan"
	GoalID            = "goal"
	PairProgrammingID = "pair-programming"
	fileName          = "agent-modes.json"
)

// Mode defines a named system prompt and its tool/path boundaries. An empty
// Permissions map means every registered tool is available without a path
// restriction, matching the legacy Echo agent-mode contract.
type Mode struct {
	ID          string                          `json:"id"`
	Name        string                          `json:"name"`
	Prompt      string                          `json:"prompt"`
	Permissions map[string]tools.ToolPermission `json:"permissions,omitempty"`
	BuiltIn     bool                            `json:"builtIn"`
}

// store is the on-disk shape of .echo/agent-modes.json. Older workspaces
// persisted a bare JSON array of modes; load upgrades that shape in memory.
type store struct {
	DefaultModeID string `json:"defaultModeId,omitempty"`
	Modes         []Mode `json:"modes"`
}

// Manager serializes reads and writes to workspace mode files.
type Manager struct {
	mu sync.Mutex
}

func NewManager() *Manager { return &Manager{} }

func Defaults() []Mode {
	return []Mode{
		{ID: GeneralID, Name: "General", BuiltIn: true},
		{
			ID:      PlanID,
			Name:    "Plan",
			BuiltIn: true,
			Prompt:  "Planning only: inspect and reason about the workspace, but do not create, edit, delete, or otherwise mutate files. Provide a concrete implementation plan instead of carrying it out.",
			Permissions: map[string]tools.ToolPermission{
				"ask_user_questions":          {Name: "ask_user_questions"},
				"filesystem_list":             {Name: "filesystem_list"},
				"filesystem_read_image":       {Name: "filesystem_read_image"},
				"filesystem_read_text":        {Name: "filesystem_read_text"},
				"filesystem_read_video":       {Name: "filesystem_read_video"},
				"filesystem_search_text":      {Name: "filesystem_search_text"},
				"filesystem_search_workspace": {Name: "filesystem_search_workspace"},
				"filesystem_stat":             {Name: "filesystem_stat"},
				"git_inspect":                 {Name: "git_inspect"},
				"fossil_inspect":              {Name: "fossil_inspect"},
				"source_control_inspect":      {Name: "source_control_inspect"},
				"research_agent_send":         {Name: "research_agent_send"},
				"research_agents_cancel":      {Name: "research_agents_cancel"},
				"research_agents_spawn":       {Name: "research_agents_spawn"},
				"research_agents_wait":        {Name: "research_agents_wait"},
				"workspace_skill_search":      {Name: "workspace_skill_search"},
				"workspace_skill_read":        {Name: "workspace_skill_read"},
				"web_fetch":                   {Name: "web_fetch"},
				"web_search":                  {Name: "web_search"},
			},
		},
		{
			ID:      GoalID,
			Name:    "Goal",
			BuiltIn: true,
			Prompt:  "Pursue the chat's durable goal autonomously. Keep making concrete progress until the goal is verifiably complete or genuinely blocked, and use the goal status tool instead of ending after an ordinary response.",
		},
		{
			ID:      PairProgrammingID,
			Name:    "Pair Programming",
			BuiltIn: true,
			Prompt:  "Pair with the user on implementation work: carry out edits and run tools directly, but keep the user in the loop. You can ask the user questions at any time with ask_user_questions — both to resolve ambiguity and to request that the user perform an action you cannot do yourself (for example: question \"Can you close the editor?\" with options \"I have closed the editor\" and \"Try another method\"). Options are suggested answers only; the user can always reply with free text. After asking, wait for the answers, incorporate them, and continue working. Ask at most two rounds per turn, do not ask things you can resolve yourself from the workspace, and if the user skips, proceed with your best judgment.",
		},
	}
}

func IsBuiltIn(id string) bool {
	switch strings.TrimSpace(id) {
	case GeneralID, PlanID, GoalID, PairProgrammingID:
		return true
	default:
		return false
	}
}

func (m *Manager) List(workspacePath string) ([]Mode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return nil, err
	}
	return append(cloneModes(Defaults()), cloneModes(parsed.Modes)...), nil
}

func (m *Manager) Resolve(workspacePath, id string) (Mode, error) {
	modes, err := m.List(workspacePath)
	if err != nil {
		return Mode{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = GeneralID
	}
	for _, mode := range modes {
		if mode.ID == id {
			return cloneMode(mode), nil
		}
	}
	return cloneMode(Defaults()[0]), nil
}

func (m *Manager) Create(workspacePath string, mode Mode) ([]Mode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return nil, err
	}
	custom := parsed.Modes
	mode, err = normalizeCustom(mode)
	if err != nil {
		return nil, err
	}
	all := append(Defaults(), custom...)
	if nameExists(all, mode.Name, "") {
		return nil, fmt.Errorf("an agent mode named %q already exists", mode.Name)
	}
	mode.ID = newID()
	custom = append(custom, mode)
	if err := save(workspacePath, store{DefaultModeID: parsed.DefaultModeID, Modes: custom}); err != nil {
		return nil, err
	}
	return append(cloneModes(Defaults()), cloneModes(custom)...), nil
}

func (m *Manager) Update(workspacePath, id string, mode Mode) ([]Mode, error) {
	id = strings.TrimSpace(id)
	if IsBuiltIn(id) {
		return nil, fmt.Errorf("built-in agent modes cannot be edited")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return nil, err
	}
	custom := parsed.Modes
	mode, err = normalizeCustom(mode)
	if err != nil {
		return nil, err
	}
	all := append(Defaults(), custom...)
	if nameExists(all, mode.Name, id) {
		return nil, fmt.Errorf("an agent mode named %q already exists", mode.Name)
	}
	found := false
	for i := range custom {
		if custom[i].ID == id {
			mode.ID = id
			custom[i] = mode
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("agent mode %q not found", id)
	}
	if err := save(workspacePath, store{DefaultModeID: parsed.DefaultModeID, Modes: custom}); err != nil {
		return nil, err
	}
	return append(cloneModes(Defaults()), cloneModes(custom)...), nil
}

func (m *Manager) Delete(workspacePath, id string) ([]Mode, error) {
	id = strings.TrimSpace(id)
	if IsBuiltIn(id) {
		return nil, fmt.Errorf("built-in agent modes cannot be deleted")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return nil, err
	}
	if parsed.DefaultModeID == id {
		parsed.DefaultModeID = ""
	}
	custom := parsed.Modes
	next := make([]Mode, 0, len(custom))
	found := false
	for _, mode := range custom {
		if mode.ID == id {
			found = true
			continue
		}
		next = append(next, mode)
	}
	if !found {
		return nil, fmt.Errorf("agent mode %q not found", id)
	}
	if err := save(workspacePath, store{DefaultModeID: parsed.DefaultModeID, Modes: next}); err != nil {
		return nil, err
	}
	return append(cloneModes(Defaults()), cloneModes(next)...), nil
}

// Default returns the workspace's configured default mode for new chat tabs.
// Unset or invalid values resolve to General.
func (m *Manager) Default(workspacePath string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return "", err
	}
	return normalizeDefault(parsed.DefaultModeID, parsed.Modes), nil
}

// SetDefault validates and persists the workspace's default mode for new chat
// tabs. An empty ID or "general" clears the explicit setting, which resolves
// to General.
func (m *Manager) SetDefault(workspacePath, id string) (string, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	parsed, err := load(workspacePath)
	if err != nil {
		return "", err
	}
	if id != "" && id != GeneralID {
		all := append(Defaults(), parsed.Modes...)
		if !modeIDExists(all, id) {
			return "", fmt.Errorf("agent mode %q not found", id)
		}
		parsed.DefaultModeID = id
	} else {
		parsed.DefaultModeID = ""
	}
	if err := save(workspacePath, parsed); err != nil {
		return "", err
	}
	return normalizeDefault(parsed.DefaultModeID, parsed.Modes), nil
}

func normalizeDefault(id string, custom []Mode) string {
	if id != "" {
		all := append(Defaults(), custom...)
		if modeIDExists(all, id) {
			return id
		}
	}
	return GeneralID
}

func modeIDExists(modes []Mode, id string) bool {
	for _, mode := range modes {
		if mode.ID == id {
			return true
		}
	}
	return false
}

func filePath(workspacePath string) string {
	return filepath.Join(workspacePath, ".echo", fileName)
}

func load(workspacePath string) (store, error) {
	path := filePath(workspacePath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store{}, nil
		}
		return store{}, fmt.Errorf("read agent modes: %w", err)
	}
	var parsed store
	if err := json.Unmarshal(data, &parsed); err != nil {
		// Legacy workspaces stored a bare JSON array of modes.
		var modes []Mode
		if legacyErr := json.Unmarshal(data, &modes); legacyErr != nil {
			return store{}, fmt.Errorf("parse agent modes %q: %w", path, err)
		}
		parsed = store{Modes: modes}
	}
	for i := range parsed.Modes {
		parsed.Modes[i].BuiltIn = false
	}
	return parsed, nil
}

func save(workspacePath string, parsed store) error {
	path := filePath(workspacePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create agent mode directory: %w", err)
	}
	data, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent modes: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write agent modes: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace agent modes: %w", err)
	}
	return nil
}

func normalizeCustom(mode Mode) (Mode, error) {
	mode.Name = strings.TrimSpace(mode.Name)
	mode.Prompt = strings.TrimSpace(mode.Prompt)
	mode.BuiltIn = false
	if mode.Name == "" {
		return Mode{}, fmt.Errorf("agent mode name is required")
	}
	if len(mode.Name) > 80 {
		return Mode{}, fmt.Errorf("agent mode name must be 80 characters or fewer")
	}
	if len(mode.Prompt) > 32*1024 {
		return Mode{}, fmt.Errorf("agent mode prompt is too long")
	}
	clean := make(map[string]tools.ToolPermission, len(mode.Permissions))
	for key, permission := range mode.Permissions {
		name := strings.TrimSpace(key)
		if name == "" {
			name = strings.TrimSpace(permission.Name)
		}
		if name == "" {
			continue
		}
		paths := make([]string, 0, len(permission.Paths))
		seen := map[string]bool{}
		for _, path := range permission.Paths {
			path = filepath.ToSlash(strings.TrimSpace(path))
			if path != "" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
		clean[name] = tools.ToolPermission{Name: name, Paths: paths}
	}
	mode.Permissions = clean
	return mode, nil
}

func nameExists(modes []Mode, name, exceptID string) bool {
	for _, mode := range modes {
		if mode.ID != exceptID && strings.EqualFold(mode.Name, name) {
			return true
		}
	}
	return false
}

func cloneModes(modes []Mode) []Mode {
	out := make([]Mode, len(modes))
	for i := range modes {
		out[i] = cloneMode(modes[i])
	}
	return out
}

func cloneMode(mode Mode) Mode {
	if mode.Permissions != nil {
		permissions := make(map[string]tools.ToolPermission, len(mode.Permissions))
		for name, permission := range mode.Permissions {
			permission.Paths = append([]string(nil), permission.Paths...)
			permissions[name] = permission
		}
		mode.Permissions = permissions
	}
	return mode
}

func newID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("mode-%d", os.Getpid())
	}
	return "mode-" + hex.EncodeToString(value[:])
}

// PermissionList returns permissions in stable name order for a checker.
func PermissionList(mode Mode) []tools.ToolPermission {
	if len(mode.Permissions) == 0 {
		return nil
	}
	names := make([]string, 0, len(mode.Permissions))
	for name := range mode.Permissions {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]tools.ToolPermission, 0, len(names))
	for _, name := range names {
		out = append(out, mode.Permissions[name])
	}
	return out
}
