package services

import (
	"strconv"
	"strings"

	"github.com/brent/echo/internal/llm"
)

type storedAppState struct {
	Settings          llm.Settings                    `json:"settings"`
	WebAccess         WebAccessSettings               `json:"webAccess"`
	Workspaces        []Workspace                     `json:"workspaces"`
	ActiveWorkspaceID string                          `json:"activeWorkspaceId"`
	KanbanCards       []KanbanCard                    `json:"kanbanCards"`
	ChatSessions      map[string]persistedChatSession `json:"chatSessions,omitempty"`
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

type persistedChatSession struct {
	WorkspaceID string        `json:"workspaceId"`
	Messages    []ChatMessage `json:"messages"`
	History     []llm.Message `json:"history"`
}

func storedAppStateFrom(state AppState, chats map[string]persistedChatSession) storedAppState {
	return storedAppState{
		Settings:          state.Settings,
		WebAccess:         state.WebAccess,
		Workspaces:        state.Workspaces,
		ActiveWorkspaceID: state.ActiveWorkspaceID,
		KanbanCards:       state.KanbanCards,
		ChatSessions:      chats,
	}
}

func (state storedAppState) appState() AppState {
	return AppState{
		Settings:          state.Settings,
		WebAccess:         state.WebAccess,
		Workspaces:        state.Workspaces,
		ActiveWorkspaceID: state.ActiveWorkspaceID,
		KanbanCards:       state.KanbanCards,
	}
}

func (s *SystemService) persistChatSession(workspaceID string) error {
	s.chatMu.Lock()
	session := s.chatSessions[workspaceID]
	var snapshot persistedChatSession
	if session != nil {
		snapshot = persistedChatSessionFrom(session)
	}
	s.chatMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if session == nil || (len(snapshot.Messages) == 0 && len(snapshot.History) == 0) {
		delete(s.persistedChatSessions, workspaceID)
	} else {
		s.persistedChatSessions[workspaceID] = snapshot
	}
	return s.saveLocked()
}

func (s *SystemService) persistAllChatSessions() error {
	s.chatMu.Lock()
	snapshots := make(map[string]persistedChatSession, len(s.chatSessions))
	for workspaceID, session := range s.chatSessions {
		if session == nil || (len(session.Messages) == 0 && len(session.History) == 0) {
			continue
		}
		snapshots[workspaceID] = persistedChatSessionFrom(session)
	}
	s.chatMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistedChatSessions = snapshots
	return s.saveLocked()
}

func persistedChatSessionFrom(session *chatSessionState) persistedChatSession {
	return persistedChatSession{
		WorkspaceID: session.WorkspaceID,
		Messages:    cloneChatMessages(session.Messages),
		History:     cloneLLMMessages(session.History),
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
		}
		for i := range session.Messages {
			message := &session.Messages[i]
			if message.Status == "streaming" || message.Status == "retrying" {
				message.Status = "canceled"
				if message.Error == "" {
					message.Error = "Interrupted when Echo closed."
				}
				changed = true
			}
			s.observeChatID(message.ID)
			for _, activity := range message.ToolCalls {
				s.observeChatID(activity.ID)
			}
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
