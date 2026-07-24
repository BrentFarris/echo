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
	workspaceAutosaveVersion  = 2
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
	SavedCommands     map[string][]SavedCommand        `json:"savedCommands,omitempty"`
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
	Version       int                     `json:"version"`
	ChatSession   *persistedChatSession   `json:"chatSession,omitempty"`
	ChatWorkspace *persistedChatWorkspace `json:"chatWorkspace,omitempty"`
	KanbanCards   []KanbanCard            `json:"kanbanCards"`
}

type persistedChatSession struct {
	WorkspaceID string        `json:"workspaceId"`
	ChatID      string        `json:"chatId,omitempty"`
	Preview     string        `json:"preview,omitempty"`
	Messages    []ChatMessage `json:"messages"`
	History     []llm.Message `json:"history"`
	Revision    uint64        `json:"revision"`
}

type persistedChatWorkspace struct {
	WorkspaceID  string                 `json:"workspaceId"`
	ActiveChatID string                 `json:"activeChatId"`
	Sessions     []persistedChatSession `json:"sessions"`
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
		SavedCommands:     state.SavedCommands,
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
		SavedCommands:     state.SavedCommands,
	}
}

func (s *SystemService) persistWorkspaceAutosave(workspaceID string) error {
	s.autosaveMu.Lock()
	defer s.autosaveMu.Unlock()

	s.chatMu.Lock()
	workspaceState := s.chatWorkspaces[workspaceID]
	if workspaceState == nil {
		workspaceState = s.ensureChatWorkspaceLocked(workspaceID)
	}
	snapshot := persistedChatWorkspaceFrom(workspaceState)
	activeSnapshot := persistedChatSessionFrom(workspaceState.Sessions[workspaceState.ActiveChatID])
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
		Version:       workspaceAutosaveVersion,
		ChatSession:   &activeSnapshot,
		ChatWorkspace: &snapshot,
		KanbanCards:   cards,
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
		// audit and reasoning remain on the message, but indicators must never
		// enter autosaves.
		messages[i].ResearchAgents = nil
		for reasoningIndex := range messages[i].ResearchReasoning {
			entry := &messages[i].ResearchReasoning[reasoningIndex]
			bounded, truncated := appendBoundedResearchReasoning("", entry.Reasoning)
			entry.Reasoning = bounded
			entry.Truncated = entry.Truncated || truncated
			entry.Replace = false
		}
	}
	return persistedChatSession{
		WorkspaceID: session.WorkspaceID,
		ChatID:      session.ChatID,
		Preview:     session.Preview,
		Messages:    messages,
		History:     cloneLLMMessages(session.History),
		Revision:    session.Revision,
	}
}

func persistedChatWorkspaceFrom(workspace *chatWorkspaceState) persistedChatWorkspace {
	persisted := persistedChatWorkspace{
		WorkspaceID:  workspace.WorkspaceID,
		ActiveChatID: workspace.ActiveChatID,
		Sessions:     make([]persistedChatSession, 0, len(workspace.TabIDs)),
	}
	for _, chatID := range workspace.TabIDs {
		if session := workspace.Sessions[chatID]; session != nil {
			persisted.Sessions = append(persisted.Sessions, persistedChatSessionFrom(session))
		}
	}
	return persisted
}

func clonePersistedChatWorkspaces(workspaces map[string]persistedChatWorkspace) map[string]persistedChatWorkspace {
	clone := make(map[string]persistedChatWorkspace, len(workspaces))
	for workspaceID, workspace := range workspaces {
		next := persistedChatWorkspace{
			WorkspaceID:  workspace.WorkspaceID,
			ActiveChatID: workspace.ActiveChatID,
			Sessions:     make([]persistedChatSession, 0, len(workspace.Sessions)),
		}
		for _, session := range workspace.Sessions {
			session.Messages = cloneChatMessages(session.Messages)
			session.History = cloneLLMMessages(session.History)
			next.Sessions = append(next.Sessions, session)
		}
		clone[workspaceID] = next
	}
	return clone
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
		clone[i].ResearchReasoning = append([]ChatResearchReasoning(nil), clone[i].ResearchReasoning...)
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
	if s.chatWorkspaces == nil {
		s.chatWorkspaces = make(map[string]*chatWorkspaceState)
	}
	for workspaceID, persistedWorkspace := range s.persistedChatWorkspaces {
		if !workspaceExists(s.state.Workspaces, workspaceID) {
			delete(s.persistedChatWorkspaces, workspaceID)
			changed = true
			continue
		}
		workspace := &chatWorkspaceState{
			WorkspaceID: workspaceID,
			Sessions:    make(map[string]*chatSessionState),
		}
		for _, persisted := range persistedWorkspace.Sessions {
			session, sessionChanged := s.restorePersistedChatSessionLocked(workspaceID, persisted)
			changed = changed || sessionChanged
			if _, exists := workspace.Sessions[session.ChatID]; exists {
				session.ChatID = s.nextChatIDLocked("chat")
				changed = true
			}
			workspace.TabIDs = append(workspace.TabIDs, session.ChatID)
			workspace.Sessions[session.ChatID] = session
		}
		if len(workspace.TabIDs) == 0 {
			session := s.newChatSessionLocked(workspaceID)
			workspace.TabIDs = []string{session.ChatID}
			workspace.Sessions[session.ChatID] = session
			changed = true
		}
		workspace.ActiveChatID = persistedWorkspace.ActiveChatID
		if workspace.Sessions[workspace.ActiveChatID] == nil {
			workspace.ActiveChatID = workspace.TabIDs[0]
			changed = true
		}
		s.chatWorkspaces[workspaceID] = workspace
		s.chatSessions[workspaceID] = workspace.Sessions[workspace.ActiveChatID]
		s.persistedChatWorkspaces[workspaceID] = persistedChatWorkspaceFrom(workspace)
		delete(s.persistedChatSessions, workspaceID)
	}

	for workspaceID, persisted := range s.persistedChatSessions {
		if s.chatWorkspaces[workspaceID] != nil {
			continue
		}
		if !workspaceExists(s.state.Workspaces, workspaceID) {
			delete(s.persistedChatSessions, workspaceID)
			changed = true
			continue
		}
		session, sessionChanged := s.restorePersistedChatSessionLocked(workspaceID, persisted)
		changed = changed || sessionChanged
		workspace := &chatWorkspaceState{
			WorkspaceID:  workspaceID,
			ActiveChatID: session.ChatID,
			TabIDs:       []string{session.ChatID},
			Sessions:     map[string]*chatSessionState{session.ChatID: session},
		}
		s.chatWorkspaces[workspaceID] = workspace
		s.chatSessions[workspaceID] = session
		s.persistedChatSessions[workspaceID] = persistedChatSessionFrom(session)
		s.persistedChatWorkspaces[workspaceID] = persistedChatWorkspaceFrom(workspace)
		changed = true
	}
	return changed
}

func (s *SystemService) restorePersistedChatSessionLocked(workspaceID string, persisted persistedChatSession) (*chatSessionState, bool) {
	changed := false
	session := &chatSessionState{
		WorkspaceID: workspaceID,
		ChatID:      persisted.ChatID,
		Preview:     persisted.Preview,
		Messages:    cloneChatMessages(persisted.Messages),
		History:     cloneLLMMessages(persisted.History),
		Revision:    persisted.Revision,
	}
	if session.ChatID == "" {
		session.ChatID = s.nextChatIDLocked("chat")
		changed = true
	} else {
		s.observeChatID(session.ChatID)
	}
	interrupted := false
	for i := range session.Messages {
		message := &session.Messages[i]
		if len(message.ResearchAgents) > 0 {
			message.ResearchAgents = nil
			changed = true
		}
		for reasoningIndex := range message.ResearchReasoning {
			entry := &message.ResearchReasoning[reasoningIndex]
			original := entry.Reasoning
			bounded, truncated := appendBoundedResearchReasoning("", original)
			if bounded != original || entry.Replace {
				changed = true
			}
			entry.Reasoning = bounded
			entry.Truncated = entry.Truncated || truncated
			entry.Replace = false
		}
		if message.Status == "streaming" || message.Status == "retrying" || message.Status == "compacting" {
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
	if session.Preview == "" {
		for _, message := range session.Messages {
			if message.Role == llm.RoleUser && strings.TrimSpace(message.Content) != "" {
				session.Preview = chatPreview(message.Content)
				changed = true
				break
			}
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
	return session, changed
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
