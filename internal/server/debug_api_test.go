package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/gorilla/websocket"
)

func TestDebugConfigurationStateRevisionAndSnapshotAPI(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "debug-api")
	profile, err := s.debugger.AddTemplate("delve")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/workspaces/" + workspace.ID + "/debug"
	config := `{"version":1,"enabledAdapterProfileIds":["delve"],"configurations":[{"id":"main","name":"Go: Main","adapterProfileId":"delve","request":"launch","arguments":{"program":"${workspaceFolder}"}}]}`
	configured := terminalRequest[struct {
		Config debugconfig.WorkspaceConfig `json:"config"`
	}](t, s, http.MethodPut, base+"/config", config, http.StatusOK)
	if len(configured.Config.Configurations) != 1 || configured.Config.Configurations[0].AdapterProfileID != profile.ID {
		t.Fatalf("debug config = %#v", configured.Config)
	}

	state := `{"expectedRevision":0,"state":{"sourceBreakpoints":[{"id":"bp-1","source":{"rootId":"main","path":"main.go"},"line":7,"enabled":true}],"watches":[{"id":"watch-1","expression":"value","enabled":true}]}}`
	saved := terminalRequest[struct {
		State debugconfig.State `json:"state"`
	}](t, s, http.MethodPut, base+"/state", state, http.StatusOK)
	if saved.State.Revision != 1 || len(saved.State.SourceBreakpoints) != 1 {
		t.Fatalf("debug state = %#v", saved.State)
	}
	terminalRequest[map[string]any](t, s, http.MethodPut, base+"/state", state, http.StatusConflict)
	snapshot := terminalRequest[struct {
		Snapshot struct {
			WorkspaceID string            `json:"workspaceId"`
			State       debugconfig.State `json:"state"`
		} `json:"snapshot"`
	}](t, s, http.MethodGet, base+"/snapshot", "", http.StatusOK)
	if snapshot.Snapshot.WorkspaceID != workspace.ID || snapshot.Snapshot.State.Revision != 1 {
		t.Fatalf("debug snapshot = %#v", snapshot.Snapshot)
	}
}

func TestDebugWebSocketBroadcastsOnlyToSubscribedWorkspace(t *testing.T) {
	s, _ := newTestServer(t)
	workspaceA := createChatWorkspace(t, s, "debug-ws-a")
	workspaceB := createChatWorkspace(t, s, "debug-ws-b")
	url := startWebSocketTestServer(t, s)
	clientA := dialSharedClient(t, url)
	clientB := dialSharedClient(t, url)
	subscribeDebugClient(t, clientA, workspaceA.ID)
	subscribeDebugClient(t, clientB, workspaceB.ID)

	s.hub.BroadcastWorkspaceDebug(workspaceA.ID, map[string]any{
		"type": "debug_event", "workspaceId": workspaceA.ID, "sequence": 3, "event": "stopped",
	})
	clientA.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event map[string]any
	if err := clientA.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "debug_event" || event["workspaceId"] != workspaceA.ID {
		t.Fatalf("debug event = %#v", event)
	}
	clientB.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if err := clientB.ReadJSON(&event); err == nil {
		t.Fatalf("workspace B received workspace A debug event: %#v", event)
	}
}

func TestDebugStopAttentionBroadcastReachesUnsubscribedBrowsers(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "debug-attention")
	url := startWebSocketTestServer(t, s)
	subscribed := dialSharedClient(t, url)
	unsubscribed := dialSharedClient(t, url)
	subscribeDebugClient(t, subscribed, workspace.ID)

	s.broadcastDebugEvent(debugger.Event{
		Type: "debug_event", WorkspaceID: workspace.ID, SessionID: "session-1", Sequence: 7, Event: "stopped",
		Session: &debugger.SessionSnapshot{ID: "session-1", WorkspaceID: workspace.ID, Configuration: "Editor", Status: debugger.StatusStopped, StopGeneration: 2, StoppedReason: "breakpoint"},
	})

	subscribed.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event map[string]any
	if err := subscribed.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "debug_event" {
		t.Fatalf("subscribed event = %#v", event)
	}
	if err := subscribed.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "debug_stopped" || event["sessionId"] != "session-1" || event["phase"] != "stopped" {
		t.Fatalf("subscribed attention event = %#v", event)
	}

	unsubscribed.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := unsubscribed.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "debug_stopped" || event["workspaceId"] != workspace.ID {
		t.Fatalf("unsubscribed attention event = %#v", event)
	}
}

func subscribeDebugClient(t *testing.T, connection *websocket.Conn, workspaceID string) {
	t.Helper()
	if err := connection.WriteJSON(map[string]any{"type": "debug_subscribe", "workspaceId": workspaceID}); err != nil {
		t.Fatal(err)
	}
	connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var message map[string]any
		if err := connection.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message["type"] == "debug_subscribed" && message["workspaceId"] == workspaceID {
			return
		}
	}
}
