package server

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
	"github.com/gorilla/websocket"
)

type fileChangingStreamer struct {
	mu       sync.Mutex
	requests int
	path     string
}

func (f *fileChangingStreamer) StreamChat(_ context.Context, _ llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests++
	requestNumber := f.requests
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 4)
	if requestNumber == 1 {
		arguments, _ := json.Marshal(map[string]any{"path": f.path, "content": "hello\n"})
		events <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 0,
			ID:    "call-create",
			Type:  "function",
			Function: llm.FunctionCallDelta{
				Name:      "filesystem_create_text",
				Arguments: string(arguments),
			},
		}}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Created the file."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "file-changing", Events: events}
}

func TestChatPersistsAndEmitsCompactFileChanges(t *testing.T) {
	server, _ := newTestServer(t)
	workspaceDirectory := t.TempDir()
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "file changes", MainPath: workspaceDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.workspaces.SetActive(workspace.ID); err != nil {
		t.Fatal(err)
	}
	rootLabel := normalizeWorkspaceFolderLabel(filepath.Base(workspaceDirectory))
	server.llm = &fileChangingStreamer{path: rootLabel + "/created.txt"}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.httpServer.Serve(listener)

	connection, _, err := websocket.DefaultDialer.Dial("ws://"+listener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	sendSharedChat(t, connection, workspace.ID, "create a file", "")
	var emitted map[string]any
	for {
		event := readCompatChatEvent(t, connection)
		if event["type"] == "chat_event" && event["eventType"] == "tool_result" {
			emitted = event
		}
		if event["type"] == "chat_done" {
			break
		}
	}

	changes, ok := emitted["fileChanges"].([]any)
	if !ok || len(changes) != 1 {
		t.Fatalf("expected one emitted file change, got %#v", emitted["fileChanges"])
	}
	change, _ := changes[0].(map[string]any)
	ref, _ := change["ref"].(map[string]any)
	if change["path"] != rootLabel+"/created.txt" || change["operation"] != tools.FileChangeCreated || ref["path"] != "created.txt" || ref["rootId"] == "" {
		t.Fatalf("unexpected emitted file change: %#v", change)
	}

	stored, err := sessions.NewWorkspaceStore(workspaceDirectory).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tabs) != 1 || len(stored.Tabs[0].Turns) != 1 || len(stored.Tabs[0].Turns[0].FileChanges) != 1 {
		t.Fatalf("file change was not persisted: %#v", stored.Tabs)
	}
	persisted := stored.Tabs[0].Turns[0].FileChanges[0]
	if persisted.Path != rootLabel+"/created.txt" || persisted.Operation != tools.FileChangeCreated || persisted.Ref == nil || persisted.Ref.Path != "created.txt" {
		t.Fatalf("unexpected persisted file change: %#v", persisted)
	}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "before") || strings.Contains(string(encoded), "after") || strings.Contains(string(encoded), "sha256") {
		t.Fatalf("persisted change included full snapshots: %s", encoded)
	}
}

func TestCompactFileChangesAddsConfinedReferences(t *testing.T) {
	roots := []tools.WorkspaceRoot{{ID: "root-one", Label: "echo", Path: "C:/repo"}}
	changes := compactFileChanges([]tools.FileChange{
		{Path: "echo/src/main.go", Operation: tools.FileChangeEdited},
		{Path: "outside.txt", Operation: tools.FileChangeDeleted},
	}, roots)

	if len(changes) != 2 || changes[0].Ref == nil || changes[0].Ref.RootID != "root-one" || changes[0].Ref.Path != "src/main.go" {
		t.Fatalf("confined reference was not added: %#v", changes)
	}
	if changes[1].Ref != nil {
		t.Fatalf("unmatched path received an editor reference: %#v", changes[1])
	}
}
