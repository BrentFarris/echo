package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brent/echo/internal/llm"
)

const (
	WorkspaceVersion  = 1
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
}

// TabTranscript contains the durable history and display metadata for one tab.
type TabTranscript struct {
	ChatID   string        `json:"chatId"`
	Preview  string        `json:"preview,omitempty"`
	Revision uint64        `json:"revision"`
	Turns    []Turn        `json:"turns"`
	Messages []llm.Message `json:"messages"`
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
	return s.loadLocked(workspaceID)
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
	workspace, err := s.loadLocked(workspaceID)
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

func (s *WorkspaceStore) loadLocked(workspaceID string) (ChatWorkspace, error) {
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
	if err := normalizeAndValidateWorkspace(&workspace, workspaceID); err != nil {
		return ChatWorkspace{}, err
	}
	if previousWorkspaceID != "" && previousWorkspaceID != workspaceID {
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
