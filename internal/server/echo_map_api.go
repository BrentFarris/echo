package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspaces"
)

// Echo Map dashboard states. Every chat appears in exactly one section:
// pending questions first, then chats with an in-flight turn, then idle chats.
const (
	echoMapStatePendingQuestions = "pending_questions"
	echoMapStateInProgress       = "in_progress"
	echoMapStateCompleted        = "completed"
)

// echoMapTurnWindow caps how many recent turns each dashboard card carries so
// the payload stays bounded no matter how long a thread grows.
const echoMapTurnWindow = 10

// awaitingInputToolStatus marks a tool call that is paused waiting for the
// user to answer its plan questions.
const awaitingInputToolStatus = "awaiting_input"

type echoMapChat struct {
	WorkspaceID string `json:"workspaceId"`
	// WorkspaceName is denormalized so the dashboard can label cards without a
	// second round trip.
	WorkspaceName    string          `json:"workspaceName"`
	ChatID           string          `json:"chatId"`
	Surface          chatSurface     `json:"surface"`
	Title            string          `json:"title"`
	State            string          `json:"state"`
	PendingQuestions int             `json:"pendingQuestions"`
	LastActivityAt   time.Time       `json:"lastActivityAt"`
	Turns            []sessions.Turn `json:"turns"`
}

type echoMapWarning struct {
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	Message       string `json:"message"`
}

func (s *Server) handleGetEchoMap(w http.ResponseWriter, _ *http.Request) {
	chats, warnings, err := s.sessions.echoMap()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chats: "+err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"chats": chats, "warnings": warnings})
}

func (m *chatSessionManager) echoMap() ([]echoMapChat, []echoMapWarning, error) {
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

	chats := make([]echoMapChat, 0)
	warnings := make([]echoMapWarning, 0)
	for _, workspace := range registered {
		var entries []echoMapChat
		var loadErr error
		if session := live[workspace.ID]; session != nil {
			entries, loadErr = session.echoMapChats(workspace)
		} else {
			var stored sessions.ChatWorkspace
			stored, loadErr = sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
			if loadErr == nil {
				entries = storedEchoMapChats(workspace, stored)
			}
		}
		if loadErr != nil {
			warnings = append(warnings, echoMapWarning{
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

func storedEchoMapChats(workspace workspaces.Workspace, stored sessions.ChatWorkspace) []echoMapChat {
	chats := make([]echoMapChat, 0, len(stored.Tabs)+1)
	for index := range stored.Tabs {
		if chat, ok := transcriptEchoMapChat(workspace, chatSurfaceMain, &stored.Tabs[index], nil); ok {
			chats = append(chats, chat)
		}
	}
	if stored.CodeChat != nil {
		if chat, ok := transcriptEchoMapChat(workspace, chatSurfaceCode, stored.CodeChat, nil); ok {
			chats = append(chats, chat)
		}
	}
	return chats
}

func (w *chatWorkspaceSession) echoMapChats(workspace workspaces.Workspace) ([]echoMapChat, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.loadErr != nil {
		return nil, w.loadErr
	}

	chats := make([]echoMapChat, 0, len(w.tabOrder)+1)
	for _, chatID := range w.tabOrder {
		tab := w.tabs[chatID]
		if tab == nil {
			continue
		}
		tab.mu.Lock()
		chat, ok := transcriptEchoMapChat(workspace, chatSurfaceMain, &tab.transcript, tab.active)
		if ok && tab.pendingPlanQuestions != nil && tab.pendingPlanQuestions.set != nil {
			chat.State = echoMapStatePendingQuestions
			chat.PendingQuestions = len(tab.pendingPlanQuestions.set.Questions)
		}
		tab.mu.Unlock()
		if ok {
			chats = append(chats, chat)
		}
	}
	if w.codeChat != nil {
		w.codeChat.mu.Lock()
		chat, ok := transcriptEchoMapChat(workspace, chatSurfaceCode, &w.codeChat.transcript, w.codeChat.active)
		if ok && w.codeChat.pendingPlanQuestions != nil && w.codeChat.pendingPlanQuestions.set != nil {
			chat.State = echoMapStatePendingQuestions
			chat.PendingQuestions = len(w.codeChat.pendingPlanQuestions.set.Questions)
		}
		w.codeChat.mu.Unlock()
		if ok {
			chats = append(chats, chat)
		}
	}
	return chats, nil
}

// transcriptEchoMapChat projects one chat transcript into a dashboard card.
// The returned turns are redacted snapshot copies; the live transcript is
// never mutated.
func transcriptEchoMapChat(workspace workspaces.Workspace, surface chatSurface, transcript *sessions.TabTranscript, active *sessions.Turn) (echoMapChat, bool) {
	if transcript == nil || (len(transcript.Turns) == 0 && active == nil) {
		return echoMapChat{}, false
	}
	turns := echoMapRecentTurns(transcript.Turns, active)
	chat := echoMapChat{
		WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, ChatID: transcript.ChatID,
		Surface: surface, Title: echoMapTitle(transcript.Turns, active, surface),
		State:          echoMapStateCompleted,
		LastActivityAt: chatLastActivity(transcript.Turns, active),
		Turns:          redactTurnsForSnapshot(turns),
	}
	if active != nil {
		chat.State = echoMapStateInProgress
	}
	if pending := echoMapPendingQuestions(turns); pending > 0 {
		chat.State = echoMapStatePendingQuestions
		chat.PendingQuestions = pending
	}
	return chat, true
}

// echoMapRecentTurns returns the last echoMapTurnWindow turns, including the
// in-flight active turn when present.
func echoMapRecentTurns(turns []sessions.Turn, active *sessions.Turn) []sessions.Turn {
	limit := echoMapTurnWindow
	if active != nil {
		limit--
	}
	start := len(turns) - limit
	if start < 0 {
		start = 0
	}
	recent := make([]sessions.Turn, 0, echoMapTurnWindow)
	recent = append(recent, turns[start:]...)
	if active != nil {
		recent = append(recent, *active)
	}
	return recent
}

// echoMapTitle returns the chat's first user prompt so card titles stay
// stable while the conversation grows, falling back to the newest preview and
// then the surface default.
func echoMapTitle(turns []sessions.Turn, active *sessions.Turn, surface chatSurface) string {
	for index := range turns {
		if turns[index].UserDeleted {
			continue
		}
		if content := strings.TrimSpace(turns[index].UserContent); content != "" {
			return chatPreview(content)
		}
	}
	if active != nil {
		if content := strings.TrimSpace(active.UserContent); content != "" {
			return chatPreview(content)
		}
	}
	if fallback := previewForTurns(turns); fallback != "" {
		return fallback
	}
	if surface == chatSurfaceCode {
		return "Code Chat"
	}
	return "Chat"
}

// echoMapPendingQuestions counts the questions in the newest tool call that is
// still waiting for user answers, or zero when none are pending. It covers
// chats restored from disk where the live wait struct no longer exists.
func echoMapPendingQuestions(turns []sessions.Turn) int {
	for index := len(turns) - 1; index >= 0; index-- {
		turn := turns[index]
		for assistant := len(turn.AssistantTurns) - 1; assistant >= 0; assistant-- {
			tools := turn.AssistantTurns[assistant].Tools
			for tool := len(tools) - 1; tool >= 0; tool-- {
				if tools[tool].Status == awaitingInputToolStatus && tools[tool].PlanQuestions != nil {
					return len(tools[tool].PlanQuestions.Questions)
				}
			}
		}
	}
	return 0
}
