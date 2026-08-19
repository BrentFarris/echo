package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspaces"
	"github.com/gorilla/websocket"
)

type gatedStreamer struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *gatedStreamer) StreamChat(ctx context.Context, _ llm.ChatRequest) *llm.Stream {
	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventReasoning, Content: "thinking"}
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "partial"}
	f.once.Do(func() { close(f.started) })
	go func() {
		defer close(events)
		select {
		case <-f.release:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: " complete"}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		case <-ctx.Done():
			events <- llm.StreamEvent{Type: llm.EventCanceled}
		}
	}()
	return &llm.Stream{ID: "gated", Events: events}
}

func startWebSocketTestServer(t *testing.T, s *Server) string {
	t.Helper()
	listener, err := netListenLocal()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go s.httpServer.Serve(listener)
	return "ws://" + listener.Addr().String() + "/ws"
}

// Kept behind a tiny helper so all test listeners use the same safe binding.
func netListenLocal() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func dialSharedClient(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var welcome map[string]any
	if err := conn.ReadJSON(&welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("read welcome: %v (%v)", err, welcome)
	}
	return conn
}

func readUntilSessionEvent(t *testing.T, conn *websocket.Conn, eventType string) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read %s event: %v", eventType, err)
		}
		if message["type"] != "session_event" {
			continue
		}
		event, _ := message["event"].(map[string]any)
		if event["type"] == eventType {
			return event
		}
	}
}

func loadActiveTabTranscript(t *testing.T, workspace workspaces.Workspace) sessions.TabTranscript {
	t.Helper()
	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatalf("load persisted chat workspace: %v", err)
	}
	for _, tab := range stored.Tabs {
		if tab.ChatID == stored.ActiveChatID {
			return tab
		}
	}
	t.Fatalf("active chat %q was not persisted: %#v", stored.ActiveChatID, stored.Tabs)
	return sessions.TabTranscript{}
}

func TestLateJoinSnapshotAndInitiatorDisconnectDoNotStopStream(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "late-join")
	fake := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	s.llm = fake
	url := startWebSocketTestServer(t, s)

	first := dialSharedClient(t, url)
	subscribeChat(t, first, workspace.ID)
	if err := first.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-late", "message": "continue elsewhere",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, first, "token")
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}

	sessionPath := filepath.Join(workspace.MainPath, ".echo", sessions.FileName)
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("in-flight response was written to disk: %v", err)
	}

	second := dialSharedClient(t, url)
	if err := second.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	for {
		if err := second.ReadJSON(&snapshot); err != nil {
			t.Fatalf("read late snapshot: %v", err)
		}
		if snapshot["type"] == "session_snapshot" {
			break
		}
	}
	active, _ := snapshot["activeTurn"].(map[string]any)
	steps, _ := active["assistantTurns"].([]any)
	if len(steps) != 1 || !strings.Contains(steps[0].(map[string]any)["content"].(string), "partial") {
		t.Fatalf("late snapshot did not contain partial response: %v", active)
	}

	_ = first.Close()
	close(fake.release)
	finished := readUntilSessionEvent(t, second, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("expected completed stream after initiator disconnect, got %v", finished)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 1 || transcript.Turns[0].AssistantTurns[0].Content != "partial complete" {
		t.Fatalf("unexpected persisted response: %#v", transcript.Turns)
	}
	if transcript.Turns[0].AssistantTurns[0].Reasoning != "thinking" {
		t.Fatalf("reasoning was not persisted: %#v", transcript.Turns[0])
	}

	restarted := NewWithSettingsPath("127.0.0.1:0", s.webDir, s.settingsPath)
	restarted.authDisabled = true
	t.Cleanup(restarted.hub.Shutdown)
	reloaded, err := restarted.sessions.get(workspace.ID)
	if err != nil {
		t.Fatalf("reload session after restart: %v", err)
	}
	reloaded.mu.Lock()
	reloadedTab := reloaded.tabs[reloaded.activeChatID]
	reloaded.mu.Unlock()
	reloadedTab.mu.Lock()
	defer reloadedTab.mu.Unlock()
	if reloadedTab.active != nil || len(reloadedTab.transcript.Turns) != 1 || reloadedTab.transcript.Turns[0].Status != "done" {
		t.Fatalf("restart did not restore terminal state: %#v", reloadedTab.transcript)
	}
}

func TestSecondClientCanStopSharedStream(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "shared-stop")
	started := make(chan struct{})
	s.llm = &cancellableStreamer{started: started}
	url := startWebSocketTestServer(t, s)
	first := dialSharedClient(t, url)
	second := dialSharedClient(t, url)
	subscribeChat(t, first, workspace.ID)
	subscribeChat(t, second, workspace.ID)
	if err := first.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-stop", "message": "keep going",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if err := second.WriteJSON(map[string]any{"type": "chat_stop", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	for index, conn := range []*websocket.Conn{first, second} {
		finished := readUntilSessionEvent(t, conn, "turn_finished")
		if finished["status"] != "stopped" {
			t.Fatalf("client %d did not receive stopped state: %v", index, finished)
		}
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 1 || transcript.Turns[0].Status != "stopped" {
		t.Fatalf("stopped turn was not persisted: %#v", transcript)
	}
}

type historyStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (f *historyStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	number := len(f.requests)
	f.mu.Unlock()
	events := make(chan llm.StreamEvent, 2)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: fmt.Sprintf("answer-%d", number)}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "history", Events: events}
}

func TestCompletedHistoryFeedsNextPromptAndDuplicateRequestIsIdempotent(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "history")
	fake := &historyStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)

	for _, prompt := range []string{"first prompt", "second prompt"} {
		requestID := "request-" + strings.Split(prompt, " ")[0]
		if err := conn.WriteJSON(map[string]any{
			"type": "chat_send", "workspaceId": workspace.ID, "requestId": requestID, "message": prompt,
		}); err != nil {
			t.Fatal(err)
		}
		finished := readUntilSessionEvent(t, conn, "turn_finished")
		if finished["status"] != "done" {
			t.Fatalf("prompt failed: %v", finished)
		}
	}

	fake.mu.Lock()
	requests := append([]llm.ChatRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two model requests, got %d", len(requests))
	}
	second := requests[1].Messages
	if len(second) < 4 || second[0].Role != llm.RoleSystem || second[1].Content != "first prompt" || second[2].Content != "answer-1" || second[len(second)-1].Content != "second prompt" {
		t.Fatalf("prior conversation was not supplied to second request: %#v", second)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-second", "message": "second prompt",
	}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var snapshot map[string]any
	for snapshot["type"] != "session_snapshot" {
		if err := conn.ReadJSON(&snapshot); err != nil {
			t.Fatalf("read idempotent snapshot: %v", err)
		}
	}
	fake.mu.Lock()
	count := len(fake.requests)
	fake.mu.Unlock()
	if count != 2 {
		t.Fatalf("duplicate request invoked model again: %d requests", count)
	}
}

func TestDeleteAssistantResponsePrunesPersistenceAndFutureContext(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "delete-response-context")
	fake := &historyStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-delete-first", "message": "first prompt",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "turn_finished")
	beforeDelete := loadActiveTabTranscript(t, workspace)
	if len(beforeDelete.Turns) != 1 {
		t.Fatalf("expected one turn before deletion: %#v", beforeDelete.Turns)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_message_delete", "workspaceId": workspace.ID,
		"turnId": beforeDelete.Turns[0].ID, "role": llm.RoleAssistant,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	turns, _ := snapshot["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["assistantDeleted"] != true {
		t.Fatalf("deleted response was not broadcast: %v", snapshot)
	}
	afterDelete := loadActiveTabTranscript(t, workspace)
	if !afterDelete.Turns[0].AssistantDeleted || len(afterDelete.Turns[0].AssistantTurns) != 0 ||
		len(afterDelete.Messages) != 1 || afterDelete.Messages[0].Content != "first prompt" {
		t.Fatalf("response was not durably pruned: %#v", afterDelete)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-delete-second", "message": "second prompt",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "turn_finished")
	fake.mu.Lock()
	requests := append([]llm.ChatRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two model requests, got %d", len(requests))
	}
	for _, message := range requests[1].Messages {
		if message.Content == "answer-1" {
			t.Fatalf("deleted response leaked into future context: %#v", requests[1].Messages)
		}
	}
	if requests[1].Messages[len(requests[1].Messages)-1].Content != "second prompt" {
		t.Fatalf("new prompt was not sent after deletion: %#v", requests[1].Messages)
	}
}

func TestClearChatPersistsAndBroadcastsEmptySnapshot(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "clear-chat")
	s.llm = &historyStreamer{}
	url := startWebSocketTestServer(t, s)
	first := dialSharedClient(t, url)
	second := dialSharedClient(t, url)
	subscribeChat(t, first, workspace.ID)
	subscribeChat(t, second, workspace.ID)

	if err := first.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-clear", "message": "clear me",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, first, "turn_finished")

	if err := first.WriteJSON(map[string]any{"type": "chat_clear", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	for index, conn := range []*websocket.Conn{first, second} {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var snapshot map[string]any
			if err := conn.ReadJSON(&snapshot); err != nil {
				t.Fatalf("client %d read cleared snapshot: %v", index, err)
			}
			if snapshot["type"] != "session_snapshot" {
				continue
			}
			turns, _ := snapshot["turns"].([]any)
			if len(turns) != 0 || snapshot["activeTurn"] != nil {
				t.Fatalf("client %d received non-empty cleared snapshot: %v", index, snapshot)
			}
			break
		}
	}

	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 0 || len(transcript.Messages) != 0 || transcript.Revision == 0 {
		t.Fatalf("chat was not durably cleared: %#v", transcript)
	}
}

func TestBusySessionRejectsNewPromptButDeduplicatesAcceptedRequest(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "busy")
	fake := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	first := dialSharedClient(t, url)
	second := dialSharedClient(t, url)
	subscribeChat(t, first, workspace.ID)
	subscribeChat(t, second, workspace.ID)
	accepted := map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-accepted", "message": "first",
	}
	if err := first.WriteJSON(accepted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if err := second.WriteJSON(accepted); err != nil {
		t.Fatal(err)
	}
	second.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := second.ReadJSON(&message); err != nil {
			t.Fatalf("read duplicate snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			break
		}
	}
	if err := second.WriteJSON(map[string]any{"type": "chat_clear", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	for {
		var message map[string]any
		if err := second.ReadJSON(&message); err != nil {
			t.Fatalf("read busy clear error: %v", err)
		}
		if message["type"] == "command_error" {
			if message["code"] != "session_busy" {
				t.Fatalf("unexpected clear command error: %v", message)
			}
			break
		}
	}
	if err := second.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "request-new", "message": "second",
	}); err != nil {
		t.Fatal(err)
	}
	for {
		var message map[string]any
		if err := second.ReadJSON(&message); err != nil {
			t.Fatalf("read busy error: %v", err)
		}
		if message["type"] == "command_error" {
			if message["code"] != "session_busy" {
				t.Fatalf("unexpected command error: %v", message)
			}
			break
		}
	}
	if err := second.WriteJSON(map[string]any{"type": "chat_stop", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, second, "turn_finished")
}

type parallelStreamer struct {
	started chan string
	release chan struct{}
}

func (f *parallelStreamer) StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream {
	prompt := request.Messages[len(request.Messages)-1].Content
	f.started <- prompt
	events := make(chan llm.StreamEvent, 2)
	go func() {
		defer close(events)
		select {
		case <-f.release:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "answer to " + prompt}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		case <-ctx.Done():
			events <- llm.StreamEvent{Type: llm.EventCanceled}
		}
	}()
	return &llm.Stream{ID: "parallel", Events: events}
}

func TestDifferentWorkspacesStreamConcurrentlyAndStayIsolated(t *testing.T) {
	s, _ := newTestServer(t)
	workspaceA := createChatWorkspace(t, s, "parallel-a")
	workspaceB := createChatWorkspace(t, s, "parallel-b")
	fake := &parallelStreamer{started: make(chan string, 2), release: make(chan struct{})}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	clientA := dialSharedClient(t, url)
	clientB := dialSharedClient(t, url)
	subscribeChat(t, clientA, workspaceA.ID)
	subscribeChat(t, clientB, workspaceB.ID)
	if err := clientA.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspaceA.ID, "requestId": "request-a", "message": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	if err := clientB.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspaceB.ID, "requestId": "request-b", "message": "beta",
	}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case prompt := <-fake.started:
			seen[prompt] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("workspace streams did not run concurrently: %v", seen)
		}
	}
	close(fake.release)
	for workspaceID, conn := range map[string]*websocket.Conn{workspaceA.ID: clientA, workspaceB.ID: clientB} {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var message map[string]any
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("read isolated event: %v", err)
			}
			if message["type"] == "session_event" && message["workspaceId"] != workspaceID {
				t.Fatalf("client received another workspace event: %v", message)
			}
			if message["type"] == "session_event" {
				event, _ := message["event"].(map[string]any)
				if event["type"] == "turn_finished" {
					break
				}
			}
		}
	}
}
