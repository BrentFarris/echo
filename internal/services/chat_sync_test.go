package services

import (
	"net/http"
	"testing"
	"time"
)

func TestChatEventsSynchronizeStartedSnapshotAndOrderedDeltas(t *testing.T) {
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	first, unsubscribeFirst := SubscribeEvents(service, 16)
	defer unsubscribeFirst()
	second, unsubscribeSecond := SubscribeEvents(service, 16)
	defer unsubscribeSecond()

	returned, err := service.SendChatMessage(workspaceID, "From phone")
	if err != nil {
		t.Fatalf("send chat: %v", err)
	}
	if returned.Revision != 1 {
		t.Fatalf("expected initial revision 1, got %d", returned.Revision)
	}

	for index, events := range []<-chan RuntimeEvent{first, second} {
		started := waitForChatRuntimeEvent(t, events, "started")
		if started.Revision != 1 || started.Session == nil {
			t.Fatalf("subscriber %d expected revisioned started snapshot, got %#v", index, started)
		}
		if len(started.Session.Messages) != 2 || started.Session.Messages[0].Content != "From phone" {
			t.Fatalf("subscriber %d got incomplete started snapshot: %#v", index, started.Session)
		}
		token := waitForChatRuntimeEvent(t, events, "token")
		complete := waitForChatRuntimeEvent(t, events, "complete")
		if token.Revision != 2 || complete.Revision != 3 {
			t.Fatalf("subscriber %d expected revisions 2 and 3, got %d and %d", index, token.Revision, complete.Revision)
		}
	}

	final := waitForChatIdle(t, service, workspaceID)
	if final.Revision != 3 || final.Busy || final.StreamID != "" {
		t.Fatalf("expected atomically settled revision 3 session, got %#v", final)
	}
}

func TestChatTranscriptMutationsEmitSessionSnapshots(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	service.chatMu.Lock()
	service.chatSessions[workspaceID] = &chatSessionState{
		WorkspaceID: workspaceID,
		Revision:    7,
		Messages: []ChatMessage{
			{ID: "msg-1", Role: "user", Content: "Question", Status: "complete"},
			{ID: "msg-2", Role: "assistant", Content: "Answer", Status: "complete"},
		},
	}
	service.chatMu.Unlock()
	events, unsubscribe := SubscribeEvents(service, 16)
	defer unsubscribe()

	edited, err := service.EditChatMessage(workspaceID, "msg-2", "Updated answer", "")
	if err != nil {
		t.Fatalf("edit assistant message: %v", err)
	}
	assertChatSnapshotEvent(t, events, 8, 2)
	if edited.Revision != 8 {
		t.Fatalf("expected edit revision 8, got %d", edited.Revision)
	}

	pruned, err := service.PruneChatMessage(workspaceID, "msg-1")
	if err != nil {
		t.Fatalf("prune chat message: %v", err)
	}
	assertChatSnapshotEvent(t, events, 9, 1)
	if pruned.Revision != 9 {
		t.Fatalf("expected prune revision 9, got %d", pruned.Revision)
	}

	cleared, err := service.ClearChat(workspaceID)
	if err != nil {
		t.Fatalf("clear chat: %v", err)
	}
	assertChatSnapshotEvent(t, events, 10, 0)
	if cleared.Revision != 10 {
		t.Fatalf("expected clear revision 10, got %d", cleared.Revision)
	}
}

func waitForChatRuntimeEvent(t *testing.T, events <-chan RuntimeEvent, eventType string) ChatStreamEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case runtimeEvent := <-events:
			if runtimeEvent.Name != ChatRuntimeEventName {
				continue
			}
			event, ok := runtimeEvent.Data.(ChatStreamEvent)
			if ok && event.Type == eventType {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for chat event %q", eventType)
		}
	}
}

func assertChatSnapshotEvent(t *testing.T, events <-chan RuntimeEvent, revision uint64, messageCount int) {
	t.Helper()
	event := waitForChatRuntimeEvent(t, events, "session_updated")
	if event.Revision != revision || event.Session == nil || len(event.Session.Messages) != messageCount {
		t.Fatalf("expected revision %d snapshot with %d messages, got %#v", revision, messageCount, event)
	}
}
