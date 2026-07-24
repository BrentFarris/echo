package services

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestChatTabsLifecycleAndPersistence(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	appState, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := appState.ActiveWorkspaceID

	initial, err := service.LoadChatWorkspace(workspaceID)
	if err != nil {
		t.Fatalf("load initial chat workspace: %v", err)
	}
	if len(initial.Tabs) != 1 || initial.ActiveChatID == "" || initial.ActiveSession.ChatID != initial.ActiveChatID {
		t.Fatalf("expected one active blank tab, got %#v", initial)
	}
	firstID := initial.ActiveChatID

	created, err := service.CreateChatTab(workspaceID)
	if err != nil {
		t.Fatalf("create second tab: %v", err)
	}
	if len(created.Tabs) != 2 || created.ActiveChatID == firstID {
		t.Fatalf("expected appended active tab, got %#v", created)
	}
	secondID := created.ActiveChatID

	service.chatMu.Lock()
	first := service.chatWorkspaces[workspaceID].Sessions[firstID]
	second := service.chatWorkspaces[workspaceID].Sessions[secondID]
	first.Preview = "First original prompt"
	first.Messages = []ChatMessage{{ID: "msg-1001", Role: llm.RoleUser, Content: "First original prompt", Status: "complete"}}
	first.History = []llm.Message{{Role: llm.RoleUser, Content: "First original prompt"}}
	first.Revision = 1
	second.Preview = "Second original prompt"
	second.Messages = []ChatMessage{{ID: "msg-1002", Role: llm.RoleUser, Content: "Second original prompt", Status: "complete"}}
	second.History = []llm.Message{{Role: llm.RoleUser, Content: "Second original prompt"}}
	second.Revision = 1
	service.chatMu.Unlock()

	if _, err := service.ActivateChatTab(workspaceID, firstID); err != nil {
		t.Fatalf("activate first tab: %v", err)
	}
	if err := service.persistWorkspaceAutosave(workspaceID); err != nil {
		t.Fatalf("persist chat tabs: %v", err)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	_ = reloaded.LoadState()
	restored, err := reloaded.LoadChatWorkspace(workspaceID)
	if err != nil {
		t.Fatalf("load restored tabs: %v", err)
	}
	if len(restored.Tabs) != 2 || restored.ActiveChatID != firstID {
		t.Fatalf("expected persisted tab order and active tab, got %#v", restored)
	}
	if restored.Tabs[0].Preview != "First original prompt" || restored.Tabs[1].Preview != "Second original prompt" {
		t.Fatalf("expected persisted previews, got %#v", restored.Tabs)
	}

	closed, err := reloaded.CloseChatTab(workspaceID, firstID)
	if err != nil {
		t.Fatalf("close active tab: %v", err)
	}
	if len(closed.Tabs) != 1 || closed.ActiveChatID != secondID {
		t.Fatalf("expected nearest surviving tab, got %#v", closed)
	}
	lastClosed, err := reloaded.CloseChatTab(workspaceID, secondID)
	if err != nil {
		t.Fatalf("close final tab: %v", err)
	}
	if len(lastClosed.Tabs) != 1 || lastClosed.ActiveChatID == secondID || lastClosed.ActiveSession.Preview != "" {
		t.Fatalf("expected a fresh replacement tab, got %#v", lastClosed)
	}
}

func TestConcurrentChatTabsCloseBusyTabIndependently(t *testing.T) {
	startedA := make(chan struct{})
	startedB := make(chan struct{})
	canceledA := make(chan struct{})
	releaseB := make(chan struct{})
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode chat request: %v", err)
			return
		}
		content := ""
		for index := len(request.Messages) - 1; index >= 0; index-- {
			if request.Messages[index].Role == llm.RoleUser {
				content = request.Messages[index].Content
				break
			}
		}
		if strings.Contains(content, "first tab") {
			close(startedA)
			<-r.Context().Done()
			close(canceledA)
			return
		}
		close(startedB)
		select {
		case <-releaseB:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"second complete"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case <-r.Context().Done():
		}
	}))
	currentState := service.LoadState()
	settings := currentState.Settings
	settings.ResearchAgentConcurrency = 0
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("disable research agents: %v", err)
	}

	first, err := service.LoadChatWorkspace(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.ActiveChatID
	second, err := service.CreateChatTab(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	secondID := second.ActiveChatID

	if _, err := service.SendChatMessageWithAttachmentsToTab(workspaceID, firstID, ChatMessageRequest{Content: "run first tab"}); err != nil {
		t.Fatalf("send first tab: %v", err)
	}
	if _, err := service.SendChatMessageWithAttachmentsToTab(workspaceID, secondID, ChatMessageRequest{Content: "run second tab"}); err != nil {
		t.Fatalf("send second tab: %v", err)
	}
	waitSignal(t, startedA, "first request")
	waitSignal(t, startedB, "second request")

	closed, err := service.CloseChatTab(workspaceID, firstID)
	if err != nil {
		t.Fatalf("close first tab: %v", err)
	}
	waitSignal(t, canceledA, "first cancellation")
	if len(closed.Tabs) != 1 || closed.ActiveChatID != secondID {
		t.Fatalf("expected second tab to survive close, got %#v", closed)
	}
	if _, err := service.LoadChatSessionForTab(workspaceID, firstID); err == nil {
		t.Fatal("expected closed chat tab to stay deleted")
	}
	if secondSession, err := service.LoadChatSessionForTab(workspaceID, secondID); err != nil || !secondSession.Busy {
		t.Fatalf("expected second tab to remain busy, session=%#v err=%v", secondSession, err)
	}

	close(releaseB)
	secondSession := waitForChatTabIdle(t, service, workspaceID, secondID)
	if len(secondSession.Messages) < 2 || secondSession.Messages[1].Content != "second complete" {
		t.Fatalf("expected independent second response, got %#v", secondSession.Messages)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForChatTabIdle(t *testing.T, service *SystemService, workspaceID string, chatID string) ChatSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		session, err := service.LoadChatSessionForTab(workspaceID, chatID)
		if err != nil {
			t.Fatalf("load chat tab: %v", err)
		}
		if !session.Busy {
			return session
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat tab %s did not become idle", chatID)
	return ChatSession{}
}
