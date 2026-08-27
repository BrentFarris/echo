package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspaces"
)

type chatMapEntry struct {
	WorkspaceID    string      `json:"workspaceId"`
	WorkspaceName  string      `json:"workspaceName"`
	ChatID         string      `json:"chatId"`
	Surface        chatSurface `json:"surface"`
	Preview        string      `json:"preview"`
	LastActivityAt time.Time   `json:"lastActivityAt"`
}

type chatMapWarning struct {
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	Message       string `json:"message"`
}

func (s *Server) handleGetChats(w http.ResponseWriter, _ *http.Request) {
	chats, warnings, err := s.sessions.chatMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chats: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"chats": chats, "warnings": warnings})
}

func (m *chatSessionManager) chatMap() ([]chatMapEntry, []chatMapWarning, error) {
	registered, err := m.server.workspaces.List()
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	live := make(map[string]*chatWorkspaceSession, len(m.sessions))
	for workspaceID, session := range m.sessions {
		live[workspaceID] = session
	}
	m.mu.Unlock()

	chats := make([]chatMapEntry, 0)
	warnings := make([]chatMapWarning, 0)
	for _, workspace := range registered {
		var entries []chatMapEntry
		var loadErr error
		if session := live[workspace.ID]; session != nil {
			entries, loadErr = session.chatMapEntries(workspace)
		} else {
			var stored sessions.ChatWorkspace
			stored, loadErr = sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
			if loadErr == nil {
				entries = storedChatMapEntries(workspace, stored)
			}
		}
		if loadErr != nil {
			warnings = append(warnings, chatMapWarning{
				WorkspaceID: workspace.ID, WorkspaceName: workspace.Name,
				Message: "Chat history is unavailable for this workspace.",
			})
			continue
		}
		chats = append(chats, entries...)
	}

	sort.SliceStable(chats, func(i, j int) bool {
		if !chats[i].LastActivityAt.Equal(chats[j].LastActivityAt) {
			return chats[i].LastActivityAt.After(chats[j].LastActivityAt)
		}
		if chats[i].WorkspaceName != chats[j].WorkspaceName {
			return chats[i].WorkspaceName < chats[j].WorkspaceName
		}
		if chats[i].Surface != chats[j].Surface {
			return chats[i].Surface < chats[j].Surface
		}
		return chats[i].ChatID < chats[j].ChatID
	})
	return chats, warnings, nil
}

func storedChatMapEntries(workspace workspaces.Workspace, stored sessions.ChatWorkspace) []chatMapEntry {
	entries := make([]chatMapEntry, 0, len(stored.Tabs)+1)
	for index := range stored.Tabs {
		if entry, ok := transcriptChatMapEntry(workspace, chatSurfaceMain, &stored.Tabs[index], nil); ok {
			entries = append(entries, entry)
		}
	}
	if stored.CodeChat != nil {
		if entry, ok := transcriptChatMapEntry(workspace, chatSurfaceCode, stored.CodeChat, nil); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (w *chatWorkspaceSession) chatMapEntries(workspace workspaces.Workspace) ([]chatMapEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.loadErr != nil {
		return nil, w.loadErr
	}

	entries := make([]chatMapEntry, 0, len(w.tabOrder)+1)
	for _, chatID := range w.tabOrder {
		tab := w.tabs[chatID]
		if tab == nil {
			continue
		}
		tab.mu.Lock()
		entry, ok := transcriptChatMapEntry(workspace, chatSurfaceMain, &tab.transcript, tab.active)
		tab.mu.Unlock()
		if ok {
			entries = append(entries, entry)
		}
	}
	if w.codeChat != nil {
		w.codeChat.mu.Lock()
		entry, ok := transcriptChatMapEntry(workspace, chatSurfaceCode, &w.codeChat.transcript, w.codeChat.active)
		w.codeChat.mu.Unlock()
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func transcriptChatMapEntry(workspace workspaces.Workspace, surface chatSurface, transcript *sessions.TabTranscript, active *sessions.Turn) (chatMapEntry, bool) {
	if transcript == nil || (len(transcript.Turns) == 0 && active == nil) {
		return chatMapEntry{}, false
	}
	preview := strings.TrimSpace(transcript.Preview)
	if preview == "" && active != nil {
		preview = chatPreview(active.UserContent)
	}
	if preview == "" {
		preview = previewForTurns(transcript.Turns)
	}
	if preview == "" {
		if surface == chatSurfaceCode {
			preview = "Code Chat"
		} else {
			preview = "Chat"
		}
	}
	return chatMapEntry{
		WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, ChatID: transcript.ChatID,
		Surface: surface, Preview: preview, LastActivityAt: chatLastActivity(transcript.Turns, active),
	}, true
}

func chatLastActivity(turns []sessions.Turn, active *sessions.Turn) time.Time {
	if active != nil {
		return active.StartedAt
	}
	if len(turns) == 0 {
		return time.Time{}
	}
	latest := turns[len(turns)-1]
	if latest.CompletedAt != nil {
		return *latest.CompletedAt
	}
	return latest.StartedAt
}
