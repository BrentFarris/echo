package debugger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/workspaces"
)

func TestInspectionResponseIsRejectedAfterContinue(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 4, 7)
	defer cleanup()
	go func() {
		request := readTestDAPRequest(t, adapter)
		service.markRunning("workspace", current.id, "adapter continued")
		writeTestDAPResponse(t, adapter, request, map[string]any{"variables": []any{}})
	}()

	_, err := service.Request(context.Background(), "workspace", current.id, "variables", ControlRequest{
		ExpectedRevision: 4, StopGeneration: 7,
		Arguments: map[string]any{"variablesReference": 1},
	})
	if !errors.Is(err, ErrStaleStop) {
		t.Fatalf("Request error = %v, want ErrStaleStop", err)
	}
}

func TestConcurrentControlsSerializeOnRevision(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 10, 2)
	defer cleanup()
	go func() {
		request := readTestDAPRequest(t, adapter)
		writeTestDAPResponse(t, adapter, request, map[string]any{"allThreadsContinued": true})
	}()

	errorsFound := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, err := service.Request(context.Background(), "workspace", current.id, "continue", ControlRequest{ExpectedRevision: 10, StopGeneration: 2})
			errorsFound <- err
		}()
	}
	start.Done()
	first, second := <-errorsFound, <-errorsFound
	if (first == nil) == (second == nil) {
		t.Fatalf("control errors = %v, %v; want one success", first, second)
	}
	stale := first
	if stale == nil {
		stale = second
	}
	if !errors.Is(stale, ErrStaleSession) {
		t.Fatalf("losing control error = %v, want ErrStaleSession", stale)
	}
}

func TestStopInterruptsNativeRestart(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 10, 2)
	defer cleanup()
	service.workspaces = staticWorkspaceResolver{workspace: workspaces.Workspace{ID: "workspace"}}
	service.state = debugconfig.NewStateStore(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
	current.status = StatusRunning
	current.configuration.Request = "launch"
	current.capabilities["supportsRestartRequest"] = true
	events := make(chan string, 8)
	service.SetNotifier(func(event Event) { events <- event.Event })

	restartReceived := make(chan struct{})
	adapterDone := make(chan struct{})
	go func() {
		defer close(adapterDone)
		restart := readTestDAPRequest(t, adapter)
		if restart.Command != "restart" {
			t.Errorf("first DAP command = %q, want restart", restart.Command)
			return
		}
		close(restartReceived)
		disconnect := readTestDAPRequest(t, adapter)
		if disconnect.Command != "disconnect" {
			t.Errorf("second DAP command = %q, want disconnect", disconnect.Command)
			return
		}
		writeTestDAPResponse(t, adapter, disconnect, nil)
	}()

	type restartResult struct {
		snapshot Snapshot
		err      error
	}
	restartDone := make(chan restartResult, 1)
	go func() {
		snapshot, err := service.RestartChecked(context.Background(), "workspace", current.id, 10)
		restartDone <- restartResult{snapshot: snapshot, err: err}
	}()

	select {
	case <-restartReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("native restart request did not reach the adapter")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- service.StopChecked("workspace", current.id, 11, nil) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopChecked error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopChecked remained blocked behind the native restart")
	}
	select {
	case result := <-restartDone:
		if result.err != nil {
			t.Fatalf("RestartChecked error after stop = %v", result.err)
		}
		if session := snapshotSession(result.snapshot, current.id); session == nil || (session.Status != StatusTerminating && session.Status != StatusTerminated) {
			t.Fatalf("restart snapshot session = %#v, want terminating or terminated", session)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RestartChecked did not return after stop")
	}
	<-adapterDone
	service.mu.Lock()
	status := current.status
	service.mu.Unlock()
	if status != StatusTerminated {
		t.Fatalf("session status = %q, want terminated", status)
	}
	for len(events) > 0 {
		if event := <-events; event == "restart_failed" {
			t.Fatal("explicit stop published restart_failed")
		}
	}
}

func TestDAPTraceRedaction(t *testing.T) {
	value := map[string]any{
		"command":   "evaluate",
		"arguments": map[string]any{"expression": "secretToken", "frameId": float64(1)},
		"body":      map[string]any{"result": "top-secret", "variables": []any{map[string]any{"name": "token", "value": "abc"}}},
	}
	redactDAPValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"secretToken", "top-secret", `"abc"`} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted trace still contains %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"frameId":1`) || !strings.Contains(text, "redacted") {
		t.Fatalf("redacted trace lost protocol metadata: %s", text)
	}
}

func TestEmptySnapshotUsesCollections(t *testing.T) {
	service := &Service{}
	runtime := &workspaceRuntime{sessions: map[string]*session{}, groups: map[string]*sessionGroup{}}
	snapshot := service.snapshotLocked("workspace", runtime, debugconfig.State{})
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"sessions":[]`) || !strings.Contains(string(encoded), `"groups":[]`) {
		t.Fatalf("empty snapshot did not use JSON arrays: %s", encoded)
	}
}

func stoppedPipeSession(t *testing.T, revision, generation uint64) (*Service, *session, net.Conn, func()) {
	t.Helper()
	client, adapter := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	connection := newDAPConnection(client, nil, nil, nil)
	current := &session{
		id: "session", workspaceID: "workspace", status: StatusStopped,
		revision: revision, stopGeneration: generation, ctx: ctx, cancel: cancel,
		conn: connection, capabilities: map[string]any{}, breakpointStatuses: map[string]BreakpointStatus{},
	}
	service := &Service{runtimes: map[string]*workspaceRuntime{
		"workspace": {sessions: map[string]*session{current.id: current}, groups: map[string]*sessionGroup{}},
	}}
	return service, current, adapter, func() { cancel(); _ = connection.Close(); _ = adapter.Close() }
}

func readTestDAPRequest(t *testing.T, connection net.Conn) dapEnvelope {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload, err := readDAPMessage(bufio.NewReader(connection))
	if err != nil {
		t.Errorf("read DAP request: %v", err)
		return dapEnvelope{}
	}
	var request dapEnvelope
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Errorf("decode DAP request: %v", err)
	}
	return request
}

func writeTestDAPResponse(t *testing.T, connection net.Conn, request dapEnvelope, body any) {
	t.Helper()
	if err := writeDAPFragments(connection, map[string]any{
		"seq": 99, "type": "response", "request_seq": request.Seq,
		"success": true, "command": request.Command, "body": body,
	}, 7, 3); err != nil {
		t.Errorf("write DAP response: %v", err)
	}
}

type staticWorkspaceResolver struct{ workspace workspaces.Workspace }

func (r staticWorkspaceResolver) Get(id string) (workspaces.Workspace, bool, error) {
	return r.workspace, id == r.workspace.ID, nil
}
func (r staticWorkspaceResolver) List() ([]workspaces.Workspace, error) {
	return []workspaces.Workspace{r.workspace}, nil
}
func (r staticWorkspaceResolver) SetDebugConfig(_ string, config debugconfig.WorkspaceConfig) (workspaces.Workspace, error) {
	workspace := r.workspace
	workspace.Debug = config
	return workspace, nil
}

func snapshotSession(snapshot Snapshot, id string) *SessionSnapshot {
	for index := range snapshot.Sessions {
		if snapshot.Sessions[index].ID == id {
			return &snapshot.Sessions[index]
		}
	}
	return nil
}
