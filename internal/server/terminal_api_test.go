package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	terminalruntime "github.com/brent/echo/internal/terminal"
	"github.com/gorilla/websocket"
)

type terminalAPIFakeBackend struct {
	mu        sync.Mutex
	read      chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	writes    bytes.Buffer
	cols      int
	rows      int
	process   *terminalAPIFakeProcess
}

type terminalAPIFakeProcess struct {
	backend *terminalAPIFakeBackend
	wait    chan int
	once    sync.Once
}

func newTerminalAPIFakeBackend() *terminalAPIFakeBackend {
	backend := &terminalAPIFakeBackend{read: make(chan []byte, 16), closed: make(chan struct{})}
	backend.process = &terminalAPIFakeProcess{backend: backend, wait: make(chan int, 1)}
	return backend
}

func (b *terminalAPIFakeBackend) Read(buffer []byte) (int, error) {
	select {
	case value := <-b.read:
		return copy(buffer, value), nil
	case <-b.closed:
		return 0, io.EOF
	}
}
func (b *terminalAPIFakeBackend) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.Write(value)
}
func (b *terminalAPIFakeBackend) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}
func (b *terminalAPIFakeBackend) Resize(cols, rows int) error {
	b.mu.Lock()
	b.cols, b.rows = cols, rows
	b.mu.Unlock()
	return nil
}
func (b *terminalAPIFakeBackend) Start(context.Context, terminalruntime.CommandSpec) (terminalruntime.Process, error) {
	return b.process, nil
}
func (b *terminalAPIFakeBackend) send(value string) { b.read <- []byte(value) }
func (b *terminalAPIFakeBackend) written() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes.String()
}

func (p *terminalAPIFakeProcess) Wait() (int, error) { return <-p.wait, nil }
func (p *terminalAPIFakeProcess) Kill() error {
	p.once.Do(func() {
		p.wait <- -1
		p.backend.closeOnce.Do(func() { close(p.backend.closed) })
	})
	return nil
}

func TestTerminalLifecycleAPI(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "terminal-api")
	first, second := newTerminalAPIFakeBackend(), newTerminalAPIFakeBackend()
	queue := []terminalruntime.Backend{first, second}
	var queueMu sync.Mutex
	s.terminal.SetBackendFactory(func() (terminalruntime.Backend, error) {
		queueMu.Lock()
		defer queueMu.Unlock()
		if len(queue) == 0 {
			return nil, errors.New("no fake backend")
		}
		backend := queue[0]
		queue = queue[1:]
		return backend, nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.terminal.Shutdown(ctx)
	})

	base := "/api/workspaces/" + workspace.ID + "/terminal/sessions"
	started := terminalRequest[terminalruntime.Snapshot](t, s, http.MethodPost, base, `{"cols":120,"rows":40}`, http.StatusOK)
	if started.ID == "" || started.Status != "running" {
		t.Fatalf("start = %#v", started)
	}
	again := terminalRequest[terminalruntime.Snapshot](t, s, http.MethodPost, base, `{"cols":80,"rows":24}`, http.StatusOK)
	if again.ID != started.ID {
		t.Fatalf("idempotent start id = %q, want %q", again.ID, started.ID)
	}

	session := base + "/" + started.ID
	terminalRequest[map[string]any](t, s, http.MethodPost, session+"/input", `{"data":"echo api\r"}`, http.StatusOK)
	if got := first.written(); got != "echo api\r" {
		t.Fatalf("input = %q", got)
	}
	size := terminalRequest[map[string]any](t, s, http.MethodPut, session+"/size", `{"cols":999,"rows":0}`, http.StatusOK)
	if size["cols"] != float64(terminalruntime.MaxCols) || size["rows"] != float64(terminalruntime.MinRows) {
		t.Fatalf("clamped size response = %#v", size)
	}
	terminalRequest[map[string]any](t, s, http.MethodGet, session+"?afterSequence=bad", "", http.StatusBadRequest)
	terminalRequest[map[string]any](t, s, http.MethodPost, base+"/stale/input", `{"data":"x"}`, http.StatusNotFound)
	oversized, _ := json.Marshal(map[string]string{"data": strings.Repeat("x", terminalruntime.MaxInput+1)})
	terminalRequest[map[string]any](t, s, http.MethodPost, session+"/input", string(oversized), http.StatusRequestEntityTooLarge)

	terminalRequest[map[string]any](t, s, http.MethodPost, session+"/stop", `{}`, http.StatusOK)
	terminalRequest[map[string]any](t, s, http.MethodPost, session+"/input", `{"data":"x"}`, http.StatusConflict)
	terminalRequest[map[string]any](t, s, http.MethodPut, session+"/size", `{"cols":80,"rows":24}`, http.StatusConflict)
	restarted := terminalRequest[terminalruntime.Snapshot](t, s, http.MethodPost, session+"/restart", `{"cols":100,"rows":30}`, http.StatusOK)
	if restarted.ID == started.ID {
		t.Fatal("restart reused the old id")
	}
	terminalRequest[map[string]any](t, s, http.MethodGet, session, "", http.StatusNotFound)
	terminalRequest[map[string]any](t, s, http.MethodGet, base+"/missing", "", http.StatusNotFound)
}

func TestSavedCommandAPIValidationScopingAndPersistence(t *testing.T) {
	s, _ := newTestServer(t)
	firstWorkspace := createChatWorkspace(t, s, "terminal-commands-a")
	secondWorkspace := createChatWorkspace(t, s, "terminal-commands-b")
	base := "/api/workspaces/" + firstWorkspace.ID + "/terminal/saved-commands"
	terminalRequest[map[string]any](t, s, http.MethodPost, base, `{"name":"","command":"echo bad"}`, http.StatusBadRequest)

	first := terminalRequest[struct {
		Command terminalruntime.SavedCommand `json:"command"`
	}](
		t, s, http.MethodPost, base, `{"name":"Status","command":"git status"}`, http.StatusCreated,
	).Command
	second := terminalRequest[struct {
		Command terminalruntime.SavedCommand `json:"command"`
	}](
		t, s, http.MethodPost, base, `{"name":"Tests","command":"go test ./..."}`, http.StatusCreated,
	).Command
	if first.Order != 0 || second.Order != 1 {
		t.Fatalf("orders = %d, %d", first.Order, second.Order)
	}

	other := terminalRequest[struct {
		Commands []terminalruntime.SavedCommand `json:"commands"`
	}](
		t, s, http.MethodGet, "/api/workspaces/"+secondWorkspace.ID+"/terminal/saved-commands", "", http.StatusOK,
	)
	if len(other.Commands) != 0 {
		t.Fatalf("commands leaked across workspaces: %#v", other.Commands)
	}

	updated := terminalRequest[struct {
		Command terminalruntime.SavedCommand `json:"command"`
	}](
		t, s, http.MethodPut, base+"/"+first.ID, `{"name":"Short status","command":"git status --short"}`, http.StatusOK,
	).Command
	if updated.Order != 0 || updated.Command != "git status --short" {
		t.Fatalf("update = %#v", updated)
	}
	terminalRequest[map[string]any](t, s, http.MethodPut, base+"/missing", `{"name":"x","command":"x"}`, http.StatusNotFound)
	terminalRequest[map[string]any](t, s, http.MethodDelete, base+"/"+second.ID, "", http.StatusOK)
	terminalRequest[map[string]any](t, s, http.MethodDelete, base+"/missing", "", http.StatusNotFound)

	reconstructed := NewWithSettingsPath("127.0.0.1:0", s.webDir, s.settingsPath)
	reconstructed.authDisabled = true
	list := terminalRequest[struct {
		Commands []terminalruntime.SavedCommand `json:"commands"`
	}](
		t, reconstructed, http.MethodGet, base, "", http.StatusOK,
	)
	if len(list.Commands) != 1 || list.Commands[0].ID != first.ID || list.Commands[0].Command != "git status --short" {
		t.Fatalf("persisted commands = %#v", list.Commands)
	}
}

func TestTerminalWebSocketSharedFilteringAndResync(t *testing.T) {
	s, _ := newTestServer(t)
	workspaceA := createChatWorkspace(t, s, "terminal-ws-a")
	workspaceB := createChatWorkspace(t, s, "terminal-ws-b")
	backend := newTerminalAPIFakeBackend()
	s.terminal.SetBackendFactory(func() (terminalruntime.Backend, error) { return backend, nil })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.terminal.Shutdown(ctx)
	})
	started, err := s.terminal.Start(workspaceA.ID, 80, 24)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	url := startWebSocketTestServer(t, s)
	first := dialSharedClient(t, url)
	second := dialSharedClient(t, url)
	filtered := dialSharedClient(t, url)
	subscribeTerminalClient(t, first, workspaceA.ID)
	subscribeTerminalClient(t, second, workspaceA.ID)
	subscribeTerminalClient(t, filtered, workspaceB.ID)

	backend.send("\x1b[36mshared\x1b[0m\r\n")
	for index, conn := range []*websocket.Conn{first, second} {
		event := readTerminalEvent(t, conn, 1)
		if event["workspaceId"] != workspaceA.ID || event["sessionId"] != started.ID {
			t.Fatalf("client %d event = %#v", index, event)
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(event["data"].(string))
		if decodeErr != nil || !strings.Contains(string(decoded), "shared") {
			t.Fatalf("event data = %q, %v", decoded, decodeErr)
		}
	}
	filtered.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var unexpected map[string]any
	if err := filtered.ReadJSON(&unexpected); err == nil {
		t.Fatalf("workspace B received workspace A event: %#v", unexpected)
	}

	_ = first.Close()
	reconnected := dialSharedClient(t, url)
	subscribeTerminalClient(t, reconnected, workspaceA.ID)
	backend.send("second\r\n")
	event := readTerminalEvent(t, reconnected, 2)
	if event["sequence"] != float64(2) {
		t.Fatalf("reconnected event = %#v", event)
	}
	replay, err := s.terminal.Sync(workspaceA.ID, started.ID, 1)
	if err != nil || len(replay.Output) != 1 || replay.Output[0].Sequence != 2 {
		t.Fatalf("gap replay = %#v, %v", replay, err)
	}
}

func terminalRequest[T any](t *testing.T, s *Server, method, path, body string, status int) T {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	var result T
	if status >= 400 {
		return result
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			t.Fatalf("decode data: %v (%s)", err, envelope.Data)
		}
	}
	return result
}

func subscribeTerminalClient(t *testing.T, conn *websocket.Conn, workspaceID string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"type": "terminal_subscribe", "workspaceId": workspaceID}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read terminal subscription: %v", err)
		}
		if message["type"] == "terminal_subscribed" && message["workspaceId"] == workspaceID {
			return
		}
	}
}

func readTerminalEvent(t *testing.T, conn *websocket.Conn, sequence uint64) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read terminal event: %v", err)
		}
		if message["type"] == "terminal_event" && message["event"] == "data" && message["sequence"] == float64(sequence) {
			return message
		}
	}
}

var _ terminalruntime.Backend = (*terminalAPIFakeBackend)(nil)
var _ terminalruntime.Process = (*terminalAPIFakeProcess)(nil)
