package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/sessions"
)

type echoMapEnvelope struct {
	Chats    []echoMapChat    `json:"chats"`
	Warnings []echoMapWarning `json:"warnings"`
}

func getEchoMapResponse(t *testing.T, server *Server) echoMapEnvelope {
	t.Helper()
	recorder := doRequest(t, server, http.MethodGet, "/api/echo-map")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		OK   bool            `json:"ok"`
		Data echoMapEnvelope `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode echo map: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("expected ok response: %s", recorder.Body.String())
	}
	return envelope.Data
}

func findEchoMapChat(chats []echoMapChat, chatID string) *echoMapChat {
	for index := range chats {
		if chats[index].ChatID == chatID {
			return &chats[index]
		}
	}
	return nil
}

func TestGetEchoMapClassifiesPendingInProgressAndCompleted(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "Classify")
	liveWorkspace := createChatWorkspace(t, server, "Live")
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)

	completed := base
	pendingQuestionSet := &sessions.PlanQuestionSet{
		QuestionSetID: "call-pending",
		Questions:     []sessions.PlanQuestion{{ID: "q1", Question: "Which database?", Options: []string{"Postgres", "SQLite"}}},
	}
	if err := sessions.NewWorkspaceStore(workspace.MainPath).Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: "done-chat",
		Tabs: []sessions.TabTranscript{
			{
				ChatID: "done-chat", Preview: "Newest prompt",
				Turns: []sessions.Turn{{
					ID: "turn-done", UserContent: "First prompt here", Status: "done",
					StartedAt: completed, CompletedAt: timePointer(completed.Add(time.Minute)),
				}},
			},
			{
				ChatID: "pending-chat", Preview: "Waiting",
				Turns: []sessions.Turn{{
					ID: "turn-pending", UserContent: "Plan the migration", Status: "streaming",
					StartedAt: completed.Add(2 * time.Minute),
					AssistantTurns: []sessions.AssistantTurn{{
						Number: 1, Content: "Let me ask before proceeding.",
						Tools: []sessions.ToolActivity{{
							CallID: "call-pending", Name: "ask_user_questions", Status: "awaiting_input",
							PlanQuestions: pendingQuestionSet,
						}},
					}},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// A live session with an in-flight turn classifies as in_progress.
	parent, err := server.sessions.get(liveWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	tab := parent.tabs[parent.activeChatID]
	parent.mu.Unlock()
	tab.mu.Lock()
	tab.transcript.Preview = "Streaming now"
	tab.active = &sessions.Turn{ID: "turn-live", UserContent: "Streaming now", Status: "streaming", StartedAt: base.Add(3 * time.Minute)}
	liveChatID := tab.transcript.ChatID
	tab.mu.Unlock()

	response := getEchoMapResponse(t, server)
	if len(response.Chats) != 3 {
		t.Fatalf("expected three chats, got %#v", response.Chats)
	}

	done := findEchoMapChat(response.Chats, "done-chat")
	if done == nil || done.State != echoMapStateCompleted {
		t.Fatalf("expected done-chat completed, got %#v", done)
	}
	if done.Title != "First prompt here" {
		t.Fatalf("expected first user prompt as title, got %q", done.Title)
	}
	if done.Surface != chatSurfaceMain {
		t.Fatalf("expected main surface, got %q", done.Surface)
	}

	pending := findEchoMapChat(response.Chats, "pending-chat")
	if pending == nil || pending.State != echoMapStatePendingQuestions {
		t.Fatalf("expected pending-chat pending_questions, got %#v", pending)
	}
	if pending.PendingQuestions != 1 {
		t.Fatalf("expected one pending question, got %d", pending.PendingQuestions)
	}

	live := findEchoMapChat(response.Chats, liveChatID)
	if live == nil || live.State != echoMapStateInProgress {
		t.Fatalf("expected live chat in_progress, got %#v", live)
	}
	if len(live.Turns) != 1 || live.Turns[0].ID != "turn-live" {
		t.Fatalf("expected the active turn in the payload, got %#v", live.Turns)
	}
}

func TestGetEchoMapCapsTurnWindowAndKeepsFirstPromptTitle(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "Window")
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)

	turns := make([]sessions.Turn, 0, 12)
	for index := 0; index < 12; index++ {
		completed := base.Add(time.Duration(index) * time.Minute)
		turns = append(turns, sessions.Turn{
			ID: "turn-" + string(rune('a'+index)), UserContent: "Prompt number " + strings.Repeat("x", index+1),
			Status: "done", StartedAt: completed, CompletedAt: &completed,
		})
	}
	if err := sessions.NewWorkspaceStore(workspace.MainPath).Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: "window-chat",
		Tabs: []sessions.TabTranscript{{ChatID: "window-chat", Preview: "Prompt number xxxxxxxxxxx", Turns: turns}},
	}); err != nil {
		t.Fatal(err)
	}

	response := getEchoMapResponse(t, server)
	chat := findEchoMapChat(response.Chats, "window-chat")
	if chat == nil {
		t.Fatalf("window chat missing: %#v", response.Chats)
	}
	if len(chat.Turns) != echoMapTurnWindow {
		t.Fatalf("expected %d turns, got %d", echoMapTurnWindow, len(chat.Turns))
	}
	if chat.Turns[0].ID != "turn-c" || chat.Turns[len(chat.Turns)-1].ID != "turn-l" {
		t.Fatalf("expected the newest ten turns, got %#v", chat.Turns)
	}
	if chat.Title != "Prompt number x" {
		t.Fatalf("expected the thread's first prompt as title, got %q", chat.Title)
	}
}

func TestGetEchoMapRedactsHeavyTurnContent(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "Redact")
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)

	longResult := strings.Repeat("r", snapshotToolResultPreviewBytes+512)
	if err := sessions.NewWorkspaceStore(workspace.MainPath).Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: "redact-chat",
		Tabs: []sessions.TabTranscript{{
			ChatID: "redact-chat", Preview: "Redact",
			Turns: []sessions.Turn{{
				ID: "turn-redact", UserContent: "Redact", Status: "done",
				StartedAt: base, CompletedAt: timePointer(base.Add(time.Minute)),
				AssistantTurns: []sessions.AssistantTurn{{
					Number: 1, Content: "Visible content", Reasoning: "secret reasoning",
					Tools: []sessions.ToolActivity{{CallID: "call-1", Name: "shell", Status: "done", Success: true, Result: longResult}},
				}},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	response := getEchoMapResponse(t, server)
	chat := findEchoMapChat(response.Chats, "redact-chat")
	if chat == nil || len(chat.Turns) != 1 || len(chat.Turns[0].AssistantTurns) != 1 {
		t.Fatalf("expected one redacted assistant turn, got %#v", chat)
	}
	assistant := chat.Turns[0].AssistantTurns[0]
	if assistant.Reasoning != "" || !assistant.ReasoningTruncated {
		t.Fatalf("expected reasoning to be redacted, got %#v", assistant)
	}
	if assistant.Content != "Visible content" {
		t.Fatalf("expected content to survive redaction, got %q", assistant.Content)
	}
	tool := assistant.Tools[0]
	if len(tool.Result) != snapshotToolResultPreviewBytes || !tool.ResultTruncated {
		t.Fatalf("expected tool result truncated to %d bytes, got %d (truncated=%v)", snapshotToolResultPreviewBytes, len(tool.Result), tool.ResultTruncated)
	}
}

func TestGetEchoMapReturnsWarningsForBrokenWorkspaces(t *testing.T) {
	server, _ := newTestServer(t)
	good := createChatWorkspace(t, server, "Good")
	broken := createChatWorkspace(t, server, "Broken")
	completed := time.Date(2026, time.August, 27, 14, 0, 0, 0, time.UTC)
	if err := sessions.NewWorkspaceStore(good.MainPath).Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: good.ID, ActiveChatID: "good-chat",
		Tabs: []sessions.TabTranscript{{ChatID: "good-chat", Preview: "Available", Turns: []sessions.Turn{{ID: "turn-good", UserContent: "Available", Status: "done", StartedAt: completed, CompletedAt: &completed}}}},
	}); err != nil {
		t.Fatal(err)
	}
	brokenPath := sessions.NewWorkspaceStore(broken.MainPath).Path()
	if err := os.MkdirAll(filepath.Dir(brokenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	response := getEchoMapResponse(t, server)
	if len(response.Chats) != 1 || response.Chats[0].ChatID != "good-chat" {
		t.Fatalf("available chats were not preserved: %#v", response.Chats)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].WorkspaceID != broken.ID {
		t.Fatalf("expected a warning for the broken workspace, got %#v", response.Warnings)
	}
}
