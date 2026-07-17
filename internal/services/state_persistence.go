package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const (
	workspaceAutosaveFileName = "autosave.json"
	workspaceAutosaveVersion  = 1
)

type storedAppState struct {
	Settings          llm.Settings                     `json:"settings"`
	WebAccess         WebAccessSettings                `json:"webAccess"`
	Workspaces        []Workspace                      `json:"workspaces"`
	ActiveWorkspaceID string                           `json:"activeWorkspaceId"`
	HeartbeatConfigs  map[string]HeartbeatConfig       `json:"heartbeatConfigs,omitempty"`
	LivenessConfigs   map[string]LivenessConfig        `json:"livenessConfigs,omitempty"`
	WatchdogConfigs   map[string]WatchdogConfig        `json:"watchdogConfigs,omitempty"`
	TokenBudgets      map[string]TokenBudget           `json:"tokenBudgets,omitempty"`
	DashboardLayouts  map[string][]DashboardWidgetJSON `json:"dashboardLayouts,omitempty"`
	KanbanCards       []KanbanCard                     `json:"kanbanCards,omitempty"`
	ChatSessions      map[string]persistedChatSession  `json:"chatSessions,omitempty"`
}

// storedAppStateRaw is used to read old state files that may contain agentModes.
type storedAppStateRaw struct {
	Settings          llm.Settings                    `json:"settings"`
	WebAccess         WebAccessSettings               `json:"webAccess"`
	Workspaces        []Workspace                     `json:"workspaces"`
	ActiveWorkspaceID string                          `json:"activeWorkspaceId"`
	AgentModes        []AgentMode                     `json:"agentModes,omitempty"` // deprecated: read for migration only
	KanbanCards       []KanbanCard                    `json:"kanbanCards"`
	ChatSessions      map[string]persistedChatSession `json:"chatSessions,omitempty"`
}

type workspaceAutosave struct {
	Version     int                   `json:"version"`
	ChatSession *persistedChatSession `json:"chatSession,omitempty"`
	KanbanCards []KanbanCard          `json:"kanbanCards"`
}

type persistedChatSession struct {
	WorkspaceID string        `json:"workspaceId"`
	Messages    []ChatMessage `json:"messages"`
	History     []llm.Message `json:"history"`
	Revision    uint64        `json:"revision"`
}

func storedAppStateFrom(state AppState) storedAppState {
	return storedAppState{
		Settings:          state.Settings,
		WebAccess:         state.WebAccess,
		Workspaces:        state.Workspaces,
		ActiveWorkspaceID: state.ActiveWorkspaceID,
		HeartbeatConfigs:  state.HeartbeatConfigs,
		LivenessConfigs:   state.LivenessConfigs,
		WatchdogConfigs:   state.WatchdogConfigs,
		DashboardLayouts:  state.DashboardLayouts,
	}
}

func (state storedAppState) appState() AppState {
	return AppState{
		Settings:          state.Settings,
		WebAccess:         state.WebAccess,
		Workspaces:        state.Workspaces,
		ActiveWorkspaceID: state.ActiveWorkspaceID,
		HeartbeatConfigs:  state.HeartbeatConfigs,
		LivenessConfigs:   state.LivenessConfigs,
		WatchdogConfigs:   state.WatchdogConfigs,
		DashboardLayouts:  state.DashboardLayouts,
	}
}

func (s *SystemService) persistWorkspaceAutosave(workspaceID string) error {
	s.autosaveMu.Lock()
	defer s.autosaveMu.Unlock()

	s.chatMu.Lock()
	session := s.chatSessions[workspaceID]
	var persisted *persistedChatSession
	if session != nil && (session.Revision > 0 || len(session.Messages) > 0 || len(session.History) > 0) {
		snapshot := persistedChatSessionFrom(session)
		persisted = &snapshot
	}
	s.chatMu.Unlock()

	s.mu.Lock()
	var workspace Workspace
	found := false
	for _, candidate := range s.state.Workspaces {
		if candidate.ID == workspaceID {
			workspace = candidate
			found = true
			break
		}
	}
	cards := make([]KanbanCard, 0)
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID {
			cards = append(cards, cloneKanbanCard(card))
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("workspace was not found")
	}

	return writeWorkspaceAutosave(workspace, workspaceAutosave{
		Version:     workspaceAutosaveVersion,
		ChatSession: persisted,
		KanbanCards: cards,
	})
}

func (s *SystemService) persistAllWorkspaceAutosaves() error {
	s.mu.Lock()
	workspaceIDs := make([]string, 0, len(s.state.Workspaces))
	for _, workspace := range s.state.Workspaces {
		workspaceIDs = append(workspaceIDs, workspace.ID)
	}
	s.mu.Unlock()

	var firstErr error
	for _, workspaceID := range workspaceIDs {
		if err := s.persistWorkspaceAutosave(workspaceID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func writeWorkspaceAutosave(workspace Workspace, autosave workspaceAutosave) error {
	path, err := workspaceAutosavePath(workspace, true)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(autosave, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace autosave: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".autosave-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace autosave: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(workspaceCacheFilePermission); err != nil {
		temp.Close()
		return fmt.Errorf("set workspace autosave permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary workspace autosave: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary workspace autosave: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary workspace autosave: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace workspace autosave: %w", err)
	}
	return nil
}

func readWorkspaceAutosave(workspace Workspace) (workspaceAutosave, bool, error) {
	path, err := workspaceAutosavePath(workspace, false)
	if err != nil {
		if os.IsNotExist(err) {
			return workspaceAutosave{}, false, nil
		}
		return workspaceAutosave{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return workspaceAutosave{}, false, nil
		}
		return workspaceAutosave{}, false, fmt.Errorf("read workspace autosave: %w", err)
	}
	var autosave workspaceAutosave
	if err := json.Unmarshal(data, &autosave); err != nil {
		return workspaceAutosave{}, false, fmt.Errorf("decode workspace autosave: %w", err)
	}
	return autosave, true, nil
}

func workspaceAutosavePath(workspace Workspace, create bool) (string, error) {
	var firstErr error
	var newestPath string
	var newestModified int64
	for _, folder := range workspace.Folders {
		root, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			if err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		cachePath := filepath.Join(root, workspaceCacheDirName)
		if create {
			if err := ensureWorkspaceCacheDirectory(cachePath, root); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		} else {
			if err := ensureWorkspaceCacheDirectoryExists(cachePath, root); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		target := filepath.Join(cachePath, workspaceAutosaveFileName)
		if create {
			return target, nil
		}
		info, err = os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			if firstErr == nil {
				firstErr = fmt.Errorf("workspace autosave %s must be a regular file", target)
			}
			continue
		}
		modified := info.ModTime().UnixNano()
		if newestPath == "" || modified > newestModified {
			newestPath = target
			newestModified = modified
		}
	}
	if newestPath != "" {
		return newestPath, nil
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", os.ErrNotExist
}

func ensureWorkspaceCacheDirectoryExists(dir string, boundary string) error {
	if err := ensureWorkspaceCachePathInside(boundary, dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace cache directory %s must not be a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace cache path %s is not a directory", dir)
	}
	return nil
}

func persistedChatSessionFrom(session *chatSessionState) persistedChatSession {
	messages := cloneChatMessages(session.Messages)
	for i := range messages {
		// Active research agents are turn-scoped runtime state. Their bounded tool
		// audit remains on the message, but indicators must never enter autosaves.
		messages[i].ResearchAgents = nil
	}
	return persistedChatSession{
		WorkspaceID: session.WorkspaceID,
		Messages:    messages,
		History:     cloneLLMMessages(session.History),
		Revision:    session.Revision,
	}
}

func clonePersistedChatSessions(sessions map[string]persistedChatSession) map[string]persistedChatSession {
	clone := make(map[string]persistedChatSession, len(sessions))
	for workspaceID, session := range sessions {
		session.Messages = cloneChatMessages(session.Messages)
		session.History = cloneLLMMessages(session.History)
		clone[workspaceID] = session
	}
	return clone
}

func cloneChatMessages(messages []ChatMessage) []ChatMessage {
	clone := append([]ChatMessage(nil), messages...)
	for i := range clone {
		clone[i].Images = append([]ChatImageAttachment(nil), clone[i].Images...)
		clone[i].ToolCalls = append([]ChatToolActivity(nil), clone[i].ToolCalls...)
		clone[i].ResearchAgents = append([]ChatResearchAgent(nil), clone[i].ResearchAgents...)
	}
	return clone
}

func cloneLLMMessages(messages []llm.Message) []llm.Message {
	clone := append([]llm.Message(nil), messages...)
	for i := range clone {
		clone[i].ContentParts = append([]llm.MessageContentPart(nil), clone[i].ContentParts...)
		for partIndex := range clone[i].ContentParts {
			if clone[i].ContentParts[partIndex].ImageURL != nil {
				imageURL := *clone[i].ContentParts[partIndex].ImageURL
				clone[i].ContentParts[partIndex].ImageURL = &imageURL
			}
		}
		clone[i].ToolCalls = append([]llm.ToolCall(nil), clone[i].ToolCalls...)
	}
	return clone
}

func (s *SystemService) restoreChatSessionsLocked() bool {
	changed := false
	for workspaceID, persisted := range s.persistedChatSessions {
		if !workspaceExists(s.state.Workspaces, workspaceID) {
			delete(s.persistedChatSessions, workspaceID)
			changed = true
			continue
		}
		session := &chatSessionState{
			WorkspaceID: workspaceID,
			Messages:    cloneChatMessages(persisted.Messages),
			History:     cloneLLMMessages(persisted.History),
			Revision:    persisted.Revision,
		}
		interrupted := false
		for i := range session.Messages {
			message := &session.Messages[i]
			if len(message.ResearchAgents) > 0 {
				message.ResearchAgents = nil
				changed = true
			}
			if message.Status == "streaming" || message.Status == "retrying" {
				message.Status = "canceled"
				if message.Error == "" {
					message.Error = "Interrupted when Echo closed."
				}
				changed = true
				interrupted = true
			}
			s.observeChatID(message.ID)
			for _, activity := range message.ToolCalls {
				s.observeChatID(activity.ID)
			}
		}
		if interrupted {
			session.Revision++
		}
		for _, message := range session.History {
			for _, call := range message.ToolCalls {
				s.observeChatID(call.ID)
			}
		}
		s.chatSessions[workspaceID] = session
		s.persistedChatSessions[workspaceID] = persistedChatSessionFrom(session)
	}
	return changed
}

func (s *SystemService) observeChatID(id string) {
	index := strings.LastIndexByte(id, '-')
	if index < 0 || index == len(id)-1 {
		return
	}
	value, err := strconv.ParseUint(id[index+1:], 10, 64)
	if err == nil && value > s.chatSeq {
		s.chatSeq = value
	}
}

func normalizeInterruptedKanbanCards(cards []KanbanCard) bool {
	changed := false
	for i := range cards {
		card := &cards[i]
		if effectiveKanbanLane(*card) != KanbanLaneInProgress {
			continue
		}
		card.Lane = KanbanLaneBlocked
		card.Status = KanbanLaneBlocked
		card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
			Type:    "error",
			Title:   "Execution interrupted",
			Content: "Echo closed while this card was running. Reset or message the card to continue.",
			Status:  KanbanLaneBlocked,
		})
		changed = true
	}
	return changed
}
