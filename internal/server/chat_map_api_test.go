package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/sessions"
)

func TestGetChatsListsNonEmptyMainAndCodeChatsNewestFirst(t *testing.T) {
	server, _ := newTestServer(t)
	alpha := createChatWorkspace(t, server, "Alpha")
	beta := createChatWorkspace(t, server, "Beta")
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)

	alphaStore := sessions.NewWorkspaceStore(alpha.MainPath)
	if err := alphaStore.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: alpha.ID, ActiveChatID: "alpha-empty",
		Tabs: []sessions.TabTranscript{
			{ChatID: "alpha-main", Preview: "Older main chat", Turns: []sessions.Turn{{ID: "turn-alpha", UserContent: "Older main chat", Status: "done", StartedAt: base, CompletedAt: timePointer(base.Add(time.Minute))}}, Messages: nil},
			{ChatID: "alpha-empty", Preview: "New chat", Turns: []sessions.Turn{}, Messages: nil},
		},
		CodeChat: &sessions.TabTranscript{ChatID: "alpha-code", Preview: "Code investigation", Turns: []sessions.Turn{{ID: "turn-code", UserContent: "Code investigation", Status: "done", StartedAt: base.Add(2 * time.Minute), CompletedAt: timePointer(base.Add(3 * time.Minute))}}, Messages: nil},
	}); err != nil {
		t.Fatal(err)
	}
	betaStore := sessions.NewWorkspaceStore(beta.MainPath)
	if err := betaStore.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: beta.ID, ActiveChatID: "beta-main",
		Tabs: []sessions.TabTranscript{{ChatID: "beta-main", Preview: "Newest workspace chat", Turns: []sessions.Turn{{ID: "turn-beta", UserContent: "Newest workspace chat", Status: "done", StartedAt: base.Add(4 * time.Minute), CompletedAt: timePointer(base.Add(5 * time.Minute))}}, Messages: nil}},
	}); err != nil {
		t.Fatal(err)
	}

	response := getChatMapResponse(t, server)
	if len(response.Chats) != 3 {
		t.Fatalf("expected three non-empty chats, got %#v", response.Chats)
	}
	got := []string{response.Chats[0].ChatID, response.Chats[1].ChatID, response.Chats[2].ChatID}
	want := []string{"beta-main", "alpha-code", "alpha-main"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected recent-first order: got %v want %v", got, want)
		}
	}
	if response.Chats[0].WorkspaceName != "Beta" || response.Chats[1].Surface != chatSurfaceCode {
		t.Fatalf("workspace or surface metadata missing: %#v", response.Chats)
	}
	if len(response.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", response.Warnings)
	}
}

func TestGetChatsIncludesLiveFirstTurnWithoutCreatingOtherBlankChats(t *testing.T) {
	server, _ := newTestServer(t)
	liveWorkspace := createChatWorkspace(t, server, "Live")
	emptyWorkspace := createChatWorkspace(t, server, "Never opened")
	started := time.Date(2026, time.August, 27, 13, 0, 0, 0, time.UTC)

	parent, err := server.sessions.get(liveWorkspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	tab := parent.tabs[parent.activeChatID]
	parent.mu.Unlock()
	tab.mu.Lock()
	tab.transcript.Preview = "Streaming first question"
	tab.active = &sessions.Turn{ID: "turn-live", UserContent: "Streaming first question", Status: "streaming", StartedAt: started}
	liveChatID := tab.transcript.ChatID
	tab.mu.Unlock()

	response := getChatMapResponse(t, server)
	if len(response.Chats) != 1 || response.Chats[0].ChatID != liveChatID || response.Chats[0].Preview != "Streaming first question" {
		t.Fatalf("live first turn was not listed: %#v", response.Chats)
	}
	if _, err := os.Stat(sessions.NewWorkspaceStore(emptyWorkspace.MainPath).Path()); !os.IsNotExist(err) {
		t.Fatalf("listing chats created state for an unopened workspace: %v", err)
	}
}

func TestGetChatsReturnsPartialResultsWithWorkspaceWarning(t *testing.T) {
	server, _ := newTestServer(t)
	good := createChatWorkspace(t, server, "Good")
	broken := createChatWorkspace(t, server, "Broken")
	completed := time.Date(2026, time.August, 27, 14, 0, 0, 0, time.UTC)
	if err := sessions.NewWorkspaceStore(good.MainPath).Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: good.ID, ActiveChatID: "good-chat",
		Tabs: []sessions.TabTranscript{{ChatID: "good-chat", Preview: "Available", Turns: []sessions.Turn{{ID: "turn-good", UserContent: "Available", Status: "done", StartedAt: completed, CompletedAt: &completed}}, Messages: nil}},
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

	response := getChatMapResponse(t, server)
	if len(response.Chats) != 1 || response.Chats[0].ChatID != "good-chat" {
		t.Fatalf("available chats were not preserved: %#v", response.Chats)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].WorkspaceID != broken.ID {
		t.Fatalf("expected broken workspace warning, got %#v", response.Warnings)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func getChatMapResponse(t *testing.T, server *Server) struct {
	Chats    []chatMapEntry   `json:"chats"`
	Warnings []chatMapWarning `json:"warnings"`
} {
	t.Helper()
	recorder := doRequest(t, server, http.MethodGet, "/api/chats")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Chats    []chatMapEntry   `json:"chats"`
			Warnings []chatMapWarning `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode chat map: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("expected ok response: %s", recorder.Body.String())
	}
	return envelope.Data
}
