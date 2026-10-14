package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/gorilla/websocket"
)

func readChatSnapshot(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read chat snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			return message
		}
	}
}

func snapshotChatIDs(t *testing.T, snapshot map[string]any) []string {
	t.Helper()
	raw, ok := snapshot["tabs"].([]any)
	if !ok {
		t.Fatalf("snapshot tabs missing: %v", snapshot)
	}
	ids := make([]string, 0, len(raw))
	for _, value := range raw {
		tab, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("invalid tab summary: %v", value)
		}
		ids = append(ids, tab["chatId"].(string))
	}
	return ids
}

func readSessionEventForChat(t *testing.T, conn *websocket.Conn, chatID, eventType string) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read %s for %s: %v", eventType, chatID, err)
		}
		if message["type"] != "session_event" || message["chatId"] != chatID {
			continue
		}
		event, _ := message["event"].(map[string]any)
		if event["type"] == eventType {
			return event
		}
	}
}

func TestChatTabLifecycleAndSharedActivation(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "tab-lifecycle")
	s.llm = &historyStreamer{}
	url := startWebSocketTestServer(t, s)
	first := dialSharedClient(t, url)
	second := dialSharedClient(t, url)
	subscribeChat(t, first, workspace.ID)
	subscribeChat(t, second, workspace.ID)

	if err := first.WriteJSON(map[string]any{"type": "chat_tab_create", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	createdFirst := readChatSnapshot(t, first)
	createdSecond := readChatSnapshot(t, second)
	ids := snapshotChatIDs(t, createdFirst)
	if len(ids) != 2 || createdFirst["activeChatId"] != ids[1] {
		t.Fatalf("create did not append and activate a tab: %v", createdFirst)
	}
	if createdSecond["activeChatId"] != ids[1] {
		t.Fatalf("second client did not receive created selection: %v", createdSecond)
	}

	if err := second.WriteJSON(map[string]any{
		"type": "chat_tab_activate", "workspaceId": workspace.ID, "chatId": ids[0],
	}); err != nil {
		t.Fatal(err)
	}
	for index, conn := range []*websocket.Conn{first, second} {
		snapshot := readChatSnapshot(t, conn)
		if snapshot["activeChatId"] != ids[0] {
			t.Fatalf("client %d did not share activation: %v", index, snapshot)
		}
	}

	if err := first.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": ids[0],
		"requestId": "preview-request", "message": "  latest\n user   prompt  ",
	}); err != nil {
		t.Fatal(err)
	}
	started := readSessionEventForChat(t, first, ids[0], "turn_started")
	if started["message"] != "latest\n user   prompt" {
		t.Fatalf("prompt content was unexpectedly changed: %v", started)
	}
	readSessionEventForChat(t, first, ids[0], "turn_finished")
	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tabs[0].Preview != "latest user prompt" {
		t.Fatalf("preview was not normalized and persisted: %#v", stored.Tabs[0])
	}

	if err := first.WriteJSON(map[string]any{
		"type": "chat_tab_close", "workspaceId": workspace.ID, "chatId": ids[1],
	}); err != nil {
		t.Fatal(err)
	}
	closed := readChatSnapshot(t, first)
	if remaining := snapshotChatIDs(t, closed); len(remaining) != 1 || remaining[0] != ids[0] || closed["activeChatId"] != ids[0] {
		t.Fatalf("closing a tab did not select the left neighbor: %v", closed)
	}
	readChatSnapshot(t, second)

	if err := first.WriteJSON(map[string]any{
		"type": "chat_tab_close", "workspaceId": workspace.ID, "chatId": ids[0],
	}); err != nil {
		t.Fatal(err)
	}
	replaced := readChatSnapshot(t, first)
	replacementIDs := snapshotChatIDs(t, replaced)
	if len(replacementIDs) != 1 || replacementIDs[0] == ids[0] || replaced["activeChatId"] != replacementIDs[0] {
		t.Fatalf("closing the final tab did not create a blank replacement: %v", replaced)
	}
}

func TestTabsStreamConcurrentlyAndCloseIndependently(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "parallel-tabs")
	fake := &parallelStreamer{started: make(chan string, 2), release: make(chan struct{})}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)

	if err := conn.WriteJSON(map[string]any{"type": "chat_tab_create", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	ids := snapshotChatIDs(t, readChatSnapshot(t, conn))
	for index, prompt := range []string{"alpha tab", "beta tab"} {
		if err := conn.WriteJSON(map[string]any{
			"type": "chat_send", "workspaceId": workspace.ID, "chatId": ids[index],
			"requestId": "parallel-" + prompt, "message": prompt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case prompt := <-fake.started:
			seen[prompt] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("tabs did not stream concurrently: %v", seen)
		}
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_tab_close", "workspaceId": workspace.ID, "chatId": ids[1],
	}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read busy close rejection: %v", err)
		}
		if message["type"] == "command_error" && message["chatId"] == ids[1] {
			if message["code"] != "session_busy" {
				t.Fatalf("unexpected busy close rejection: %v", message)
			}
			break
		}
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_tab_close", "workspaceId": workspace.ID, "chatId": ids[1], "stopIfBusy": true,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	remaining := snapshotChatIDs(t, snapshot)
	if len(remaining) != 1 || remaining[0] != ids[0] {
		t.Fatalf("confirmed busy close affected the wrong tab: %v", snapshot)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_stop", "workspaceId": workspace.ID, "chatId": ids[0],
	}); err != nil {
		t.Fatal(err)
	}
	finished := readSessionEventForChat(t, conn, ids[0], "turn_finished")
	if finished["status"] != "stopped" {
		t.Fatalf("remaining tab was not stopped independently: %v", finished)
	}
}

func TestTabHistoriesRemainIndependent(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "tab-histories")
	fake := &historyStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{"type": "chat_tab_create", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	ids := snapshotChatIDs(t, readChatSnapshot(t, conn))

	requests := []struct {
		chatID    string
		requestID string
		prompt    string
	}{
		{ids[0], "history-first", "first tab prompt"},
		{ids[1], "history-second", "second tab prompt"},
		{ids[0], "history-followup", "first tab followup"},
	}
	for _, request := range requests {
		if err := conn.WriteJSON(map[string]any{
			"type": "chat_send", "workspaceId": workspace.ID, "chatId": request.chatID,
			"requestId": request.requestID, "message": request.prompt,
		}); err != nil {
			t.Fatal(err)
		}
		readSessionEventForChat(t, conn, request.chatID, "turn_finished")
	}

	fake.mu.Lock()
	modelRequests := append([]llm.ChatRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(modelRequests) != 3 {
		t.Fatalf("expected three model requests, got %d", len(modelRequests))
	}
	followup := modelRequests[2].Messages
	var contents []string
	for _, message := range followup {
		contents = append(contents, message.Content)
	}
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "first tab prompt") || !strings.Contains(joined, "answer-1") || strings.Contains(joined, "second tab prompt") {
		t.Fatalf("first tab followup received another tab's history: %#v", followup)
	}

	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 2 || len(stored.Tabs[0].Turns) != 2 || len(stored.Tabs[1].Turns) != 1 {
		t.Fatalf("tab turns were not persisted independently: %#v", stored.Tabs)
	}
}

func TestChatTabForkCopiesHistoryUpToForkedMessage(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "tab-fork")
	fake := &historyStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	initial, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := initial.Tabs[0].ChatID

	for index, prompt := range []string{"fork first prompt", "fork second prompt"} {
		if err := conn.WriteJSON(map[string]any{
			"type": "chat_send", "workspaceId": workspace.ID,
			"requestId": fmt.Sprintf("fork-%d", index), "message": prompt,
		}); err != nil {
			t.Fatal(err)
		}
		readSessionEventForChat(t, conn, sourceID, "turn_finished")
	}
	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 1 || len(stored.Tabs[0].Turns) != 2 {
		t.Fatalf("expected one tab with two turns before forking: %#v", stored.Tabs)
	}

	// Fork on the first turn's assistant message: the fork should keep only
	// the first turn.
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_tab_fork", "workspaceId": workspace.ID,
		"chatId": stored.Tabs[0].ChatID, "turnId": stored.Tabs[0].Turns[0].ID,
		"role": llm.RoleAssistant,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	ids := snapshotChatIDs(t, snapshot)
	if len(ids) != 2 || snapshot["activeChatId"] != ids[1] {
		t.Fatalf("fork did not append and activate a new tab: %v", snapshot)
	}
	forkedTurns, _ := snapshot["turns"].([]any)
	if len(forkedTurns) != 1 {
		t.Fatalf("forked tab should contain only the first turn: %v", snapshot)
	}

	// A follow-up in the forked tab must not see the source tab's second turn.
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": ids[1],
		"requestId": "fork-followup", "message": "forked followup",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, conn, ids[1], "turn_finished")
	fake.mu.Lock()
	modelRequests := append([]llm.ChatRequest(nil), fake.requests...)
	fake.mu.Unlock()
	if len(modelRequests) != 3 {
		t.Fatalf("expected three model requests, got %d", len(modelRequests))
	}
	var contents []string
	for _, message := range modelRequests[2].Messages {
		contents = append(contents, message.Content)
	}
	joined := strings.Join(contents, "\n")
	if !strings.Contains(joined, "fork first prompt") || !strings.Contains(joined, "answer-1") {
		t.Fatalf("forked followup lost the forked history: %#v", modelRequests[2].Messages)
	}
	if strings.Contains(joined, "fork second prompt") || strings.Contains(joined, "answer-2") {
		t.Fatalf("forked followup saw history after the fork point: %#v", modelRequests[2].Messages)
	}

	// The source tab must remain untouched and keep its full history.
	stored, err = sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 2 || len(stored.Tabs[0].Turns) != 2 || len(stored.Tabs[1].Turns) != 2 {
		t.Fatalf("fork did not persist the copied history next to the intact source: %#v", stored.Tabs)
	}

	// Forking on a user message keeps only that message from its turn.
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_tab_fork", "workspaceId": workspace.ID,
		"chatId": stored.Tabs[0].ChatID, "turnId": stored.Tabs[0].Turns[1].ID,
		"role": llm.RoleUser,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot = readChatSnapshot(t, conn)
	userForkIDs := snapshotChatIDs(t, snapshot)
	if len(userForkIDs) != 3 || snapshot["activeChatId"] != userForkIDs[2] {
		t.Fatalf("user-message fork did not append and activate a new tab: %v", snapshot)
	}
	userForkTurns, _ := snapshot["turns"].([]any)
	if len(userForkTurns) != 2 {
		t.Fatalf("user-message fork should keep both turns: %v", snapshot)
	}
	stored, err = sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	userFork := stored.Tabs[2]
	if len(userFork.Messages) != 3 || userFork.Messages[2].Role != llm.RoleUser {
		t.Fatalf("user-message fork kept the assistant response: %#v", userFork.Messages)
	}
	if !userFork.Turns[1].AssistantDeleted {
		t.Fatalf("user-message fork did not mark the response as deleted: %#v", userFork.Turns[1])
	}
}
