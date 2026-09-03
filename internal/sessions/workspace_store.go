package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/llm"
)

const (
	WorkspaceVersion  = 4
	WorkspaceFileName = "chat-workspace.json"
)

// ChatWorkspace is the durable, ordered collection of chat tabs for one
// workspace. Active stream state is intentionally runtime-only.
type ChatWorkspace struct {
	Version      int             `json:"version"`
	WorkspaceID  string          `json:"workspaceId"`
	Revision     uint64          `json:"revision"`
	ActiveChatID string          `json:"activeChatId"`
	Tabs         []TabTranscript `json:"tabs"`
	CodeChat     *TabTranscript  `json:"codeChat,omitempty"`
}

// TabTranscript contains the durable history and display metadata for one tab.
type TabTranscript struct {
	ChatID            string             `json:"chatId"`
	Preview           string             `json:"preview,omitempty"`
	Revision          uint64             `json:"revision"`
	Vision            bool               `json:"vision,omitempty"`
	Turns             []Turn             `json:"turns"`
	Messages          []llm.Message      `json:"messages"`
	ContextCheckpoint *ContextCheckpoint `json:"contextCheckpoint,omitempty"`
	Goals             []GoalState        `json:"goals,omitempty"`
	CurrentGoalID     string             `json:"currentGoalId,omitempty"`
}

// WorkspaceStore serializes atomic updates to the multi-tab chat file.
type WorkspaceStore struct {
	path string
	mu   sync.Mutex
}

func NewWorkspaceStore(workspacePath string) *WorkspaceStore {
	return &WorkspaceStore{path: filepath.Join(workspacePath, ".echo", WorkspaceFileName)}
}

func (s *WorkspaceStore) Path() string { return s.path }

func (s *WorkspaceStore) Load(workspaceID string) (ChatWorkspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(workspaceID, false)
}

// LoadRecoveringGoals is used when constructing the live chat supervisor.
// Any goal left active by a prior process is durably paused before it is
// returned; ordinary read paths use Load so inspecting state cannot interrupt
// a currently running process.
func (s *WorkspaceStore) LoadRecoveringGoals(workspaceID string) (ChatWorkspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(workspaceID, true)
}

func (s *WorkspaceStore) Save(workspace ChatWorkspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := normalizeAndValidateWorkspace(&workspace, workspace.WorkspaceID); err != nil {
		return err
	}
	return s.saveLocked(workspace)
}

// Update reloads the latest file while holding the store lock, applies update,
// validates the result, and atomically replaces the file.
func (s *WorkspaceStore) Update(workspaceID string, update func(*ChatWorkspace) error) (ChatWorkspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace, err := s.loadLocked(workspaceID, false)
	if err != nil {
		return ChatWorkspace{}, err
	}
	if err := update(&workspace); err != nil {
		return ChatWorkspace{}, err
	}
	if err := normalizeAndValidateWorkspace(&workspace, workspaceID); err != nil {
		return ChatWorkspace{}, err
	}
	if err := s.saveLocked(workspace); err != nil {
		return ChatWorkspace{}, err
	}
	return workspace, nil
}

func (s *WorkspaceStore) loadLocked(workspaceID string, recoverInterruptedGoals bool) (ChatWorkspace, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ChatWorkspace{Version: WorkspaceVersion, WorkspaceID: workspaceID, Tabs: []TabTranscript{}}, nil
		}
		return ChatWorkspace{}, fmt.Errorf("read chat workspace: %w", err)
	}
	var workspace ChatWorkspace
	if err := json.Unmarshal(data, &workspace); err != nil {
		return ChatWorkspace{}, fmt.Errorf("parse chat workspace %q: %w", s.path, err)
	}
	previousWorkspaceID := workspace.WorkspaceID
	previousVersion := workspace.Version
	if err := normalizeAndValidateWorkspace(&workspace, workspaceID); err != nil {
		return ChatWorkspace{}, err
	}
	goalsPaused := false
	if recoverInterruptedGoals {
		goalsPaused = pauseActiveGoalsAfterLoad(&workspace)
	}
	if previousVersion != workspace.Version || goalsPaused || (previousWorkspaceID != "" && previousWorkspaceID != workspaceID) {
		// Workspace IDs live in the machine-local registry. When a portable
		// workspace is opened after that registry was recreated or rebound, the
		// file location is the ownership boundary and its chat state can safely
		// adopt the current local ID.
		if err := s.saveLocked(workspace); err != nil {
			return ChatWorkspace{}, fmt.Errorf("rebind chat workspace id: %w", err)
		}
	}
	return workspace, nil
}

func normalizeAndValidateWorkspace(workspace *ChatWorkspace, workspaceID string) error {
	if workspace.Version == 0 && len(workspace.Tabs) == 0 && workspace.WorkspaceID == "" {
		workspace.Version = WorkspaceVersion
		workspace.WorkspaceID = workspaceID
	}
	if workspace.Version == 1 || workspace.Version == 2 || workspace.Version == 3 {
		workspace.Version = WorkspaceVersion
	}
	if workspace.Version != WorkspaceVersion {
		return fmt.Errorf("unsupported chat workspace version %d", workspace.Version)
	}
	workspace.WorkspaceID = workspaceID
	if workspace.Tabs == nil {
		workspace.Tabs = []TabTranscript{}
	}
	seen := make(map[string]struct{}, len(workspace.Tabs))
	for index := range workspace.Tabs {
		tab := &workspace.Tabs[index]
		tab.ChatID = strings.TrimSpace(tab.ChatID)
		if tab.ChatID == "" {
			return fmt.Errorf("chat tab %d has no id", index)
		}
		if _, exists := seen[tab.ChatID]; exists {
			return fmt.Errorf("duplicate chat tab id %q", tab.ChatID)
		}
		seen[tab.ChatID] = struct{}{}
		if tab.Turns == nil {
			tab.Turns = []Turn{}
		}
		if tab.Messages == nil {
			tab.Messages = []llm.Message{}
		}
		normalizeContextCheckpoint(tab)
		if err := normalizeGoals(tab); err != nil {
			return fmt.Errorf("chat tab %q: %w", tab.ChatID, err)
		}
	}
	if workspace.CodeChat != nil {
		codeChat := workspace.CodeChat
		codeChat.ChatID = strings.TrimSpace(codeChat.ChatID)
		if codeChat.ChatID == "" {
			return fmt.Errorf("code chat has no id")
		}
		if _, exists := seen[codeChat.ChatID]; exists {
			return fmt.Errorf("duplicate chat id %q", codeChat.ChatID)
		}
		if codeChat.Turns == nil {
			codeChat.Turns = []Turn{}
		}
		if codeChat.Messages == nil {
			codeChat.Messages = []llm.Message{}
		}
		normalizeContextCheckpoint(codeChat)
		if err := normalizeGoals(codeChat); err != nil {
			return fmt.Errorf("code chat: %w", err)
		}
	}
	if len(workspace.Tabs) == 0 {
		workspace.ActiveChatID = ""
		return nil
	}
	if _, exists := seen[workspace.ActiveChatID]; !exists {
		return fmt.Errorf("active chat tab %q was not found", workspace.ActiveChatID)
	}
	return nil
}

func normalizeGoals(tab *TabTranscript) error {
	tab.CurrentGoalID = strings.TrimSpace(tab.CurrentGoalID)
	seen := make(map[string]struct{}, len(tab.Goals))
	currentFound := tab.CurrentGoalID == ""
	for index := range tab.Goals {
		goal := &tab.Goals[index]
		goal.ID = strings.TrimSpace(goal.ID)
		goal.Objective = strings.TrimSpace(goal.Objective)
		goal.Model = strings.TrimSpace(goal.Model)
		goal.Outcome = strings.TrimSpace(goal.Outcome)
		goal.LastError = strings.TrimSpace(goal.LastError)
		goal.PendingOutcome = strings.TrimSpace(goal.PendingOutcome)
		if goal.ID == "" || goal.Objective == "" {
			return fmt.Errorf("goal %d requires an id and objective", index)
		}
		if _, exists := seen[goal.ID]; exists {
			return fmt.Errorf("duplicate goal id %q", goal.ID)
		}
		seen[goal.ID] = struct{}{}
		if goal.ID == tab.CurrentGoalID {
			currentFound = true
		}
		switch goal.Status {
		case GoalStatusActive, GoalStatusPaused, GoalStatusBlocked, GoalStatusCompleted, GoalStatusCleared:
		default:
			return fmt.Errorf("goal %q has invalid status %q", goal.ID, goal.Status)
		}
		switch goal.PendingStatus {
		case "", GoalStatusCompleted, GoalStatusBlocked:
		default:
			return fmt.Errorf("goal %q has invalid pending status %q", goal.ID, goal.PendingStatus)
		}
		if goal.PendingStatus != "" && goal.PendingOutcome == "" {
			return fmt.Errorf("goal %q has a pending status without an outcome", goal.ID)
		}
		if goal.PendingStatus == "" {
			goal.PendingOutcome = ""
		}
		for steeringIndex := range goal.PendingSteering {
			steering := &goal.PendingSteering[steeringIndex]
			steering.ID = strings.TrimSpace(steering.ID)
			steering.Content = strings.TrimSpace(steering.Content)
			if steering.ID == "" || (steering.Content == "" && len(steering.Images) == 0 && len(steering.Videos) == 0) {
				return fmt.Errorf("goal %q has invalid queued steering", goal.ID)
			}
		}
	}
	if !currentFound {
		return fmt.Errorf("current goal %q was not found", tab.CurrentGoalID)
	}
	return nil
}

func pauseActiveGoalsAfterLoad(workspace *ChatWorkspace) bool {
	changed := false
	pause := func(tab *TabTranscript) {
		for index := range tab.Goals {
			goal := &tab.Goals[index]
			if goal.Status != GoalStatusActive {
				continue
			}
			now := time.Now().UTC()
			if goal.ActiveSince != nil {
				goal.ActiveSeconds += maxInt64(0, int64(now.Sub(*goal.ActiveSince).Seconds()))
			}
			goal.ActiveSince = nil
			goal.Status = GoalStatusPaused
			goal.PendingStatus = ""
			goal.PendingOutcome = ""
			goal.Outcome = ""
			goal.CompletedAt = nil
			goal.LastError = "Echo restarted while this goal was running. Resume to inspect the current workspace state and continue."
			goal.UpdatedAt = now
			tab.Revision++
			changed = true
		}
	}
	for index := range workspace.Tabs {
		pause(&workspace.Tabs[index])
	}
	if workspace.CodeChat != nil {
		pause(workspace.CodeChat)
	}
	if changed {
		workspace.Revision++
	}
	return changed
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func normalizeContextCheckpoint(tab *TabTranscript) {
	checkpoint := tab.ContextCheckpoint
	if checkpoint == nil {
		return
	}
	checkpoint.Summary = strings.TrimSpace(checkpoint.Summary)
	if checkpoint.Summary == "" || checkpoint.ProtectedHeadIndex < 0 || checkpoint.ProtectedHeadIndex >= len(tab.Messages) ||
		tab.Messages[checkpoint.ProtectedHeadIndex].Role != llm.RoleUser ||
		checkpoint.CompactedThrough <= checkpoint.ProtectedHeadIndex || checkpoint.CompactedThrough >= len(tab.Messages) {
		tab.ContextCheckpoint = nil
	}
}

func (s *WorkspaceStore) saveLocked(workspace ChatWorkspace) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create chat workspace directory: %w", err)
	}
	data, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chat workspace: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create chat workspace temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod chat workspace temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write chat workspace: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync chat workspace: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close chat workspace temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace chat workspace: %w", err)
	}
	return nil
}
