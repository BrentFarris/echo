package debugger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/debugconfig"
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
