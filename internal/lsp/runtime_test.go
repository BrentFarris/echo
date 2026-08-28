package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/lspconfig"
	"github.com/brent/echo/internal/workspaces"
)

func TestLSPFakeProcess(t *testing.T) {
	if os.Getenv("ECHO_FAKE_LSP") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "fake language server stderr")
	reader := bufio.NewReader(os.Stdin)
	for {
		frame, err := readRPCFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var message rpcMessage
		if json.Unmarshal(frame, &message) != nil {
			os.Exit(2)
		}
		if message.Method == "exit" {
			os.Exit(0)
		}
		if message.Method == "initialized" && os.Getenv("ECHO_FAKE_LSP_MALFORMED") == "1" {
			go func() {
				time.Sleep(50 * time.Millisecond)
				fmt.Fprint(os.Stdout, "Malformed header\r\n\r\n")
			}()
		}
		if len(message.ID) == 0 {
			continue
		}
		response := rpcMessage{JSONRPC: "2.0", ID: message.ID}
		switch message.Method {
		case "initialize":
			_ = os.WriteFile(os.Getenv("ECHO_FAKE_LSP_LOG"), message.Params, 0o600)
			response.Result = json.RawMessage(`{"capabilities":{"textDocumentSync":2,"hoverProvider":true,"completionProvider":{"resolveProvider":true,"triggerCharacters":["."]}}}`)
		case "shutdown":
			response.Result = json.RawMessage("null")
		default:
			response.Result = json.RawMessage(`{"contents":{"kind":"markdown","value":"fake hover"}}`)
		}
		if err := writeTestRPCMessage(os.Stdout, response); err != nil {
			os.Exit(3)
		}
	}
}

func TestServerRuntimeInitializationRoutingStderrAndShutdown(t *testing.T) {
	current, service, workspace, logPath := startTestRuntime(t)

	result, err := current.call(context.Background(), "textDocument/hover", json.RawMessage(`{"textDocument":{"uri":"file:///test.go"}}`))
	if err != nil || !strings.Contains(string(result), "fake hover") {
		t.Fatalf("hover result=%s err=%v", result, err)
	}

	var initialize struct {
		RootURI               string           `json:"rootUri"`
		WorkspaceFolders      []map[string]any `json:"workspaceFolders"`
		InitializationOptions map[string]any   `json:"initializationOptions"`
		Capabilities          map[string]any   `json:"capabilities"`
	}
	waitFor(t, 2*time.Second, func() bool {
		data, err := os.ReadFile(logPath)
		return err == nil && json.Unmarshal(data, &initialize) == nil
	})
	if initialize.RootURI != fileURI(workspace.MainPath) || len(initialize.WorkspaceFolders) != 2 {
		t.Fatalf("unexpected initialize roots: root=%q folders=%+v", initialize.RootURI, initialize.WorkspaceFolders)
	}
	if initialize.InitializationOptions["test"] != true {
		t.Fatalf("initialization options missing: %+v", initialize.InitializationOptions)
	}
	if initialize.Capabilities["textDocument"] == nil {
		t.Fatalf("client capabilities missing: %+v", initialize.Capabilities)
	}
	waitFor(t, time.Second, func() bool { return strings.Contains(current.status().Stderr, "fake language server stderr") })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	current.stop(ctx)
	if state := current.status().State; state != "stopped" {
		t.Fatalf("runtime state after shutdown = %q", state)
	}
	service.mu.Lock()
	delete(service.runtimes, runtimeKey(workspace.ID, current.profile.ID))
	service.mu.Unlock()
}

func TestClientCapabilitiesAdvertiseOrganizeImports(t *testing.T) {
	textDocument := clientCapabilities()["textDocument"].(map[string]any)
	codeAction := textDocument["codeAction"].(map[string]any)
	literal := codeAction["codeActionLiteralSupport"].(map[string]any)
	kinds := literal["codeActionKind"].(map[string]any)["valueSet"].([]string)
	for _, kind := range kinds {
		if kind == "source.organizeImports" {
			return
		}
	}
	t.Fatalf("source.organizeImports missing from code action kinds: %v", kinds)
}

func TestDocumentLeaseDenialTakeoverDisconnectAndStaleVersion(t *testing.T) {
	current, service, workspace, _ := startTestRuntime(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		current.stop(ctx)
	}()

	var messagesMu sync.Mutex
	messages := map[string][]any{}
	newClient := func(id string) *Client {
		client := &Client{
			ID: id, WorkspaceID: workspace.ID, service: service,
			documents: map[string]Document{}, pending: map[string]chan serverRequestResponse{},
			send: func(message any) {
				messagesMu.Lock()
				messages[id] = append(messages[id], message)
				messagesMu.Unlock()
			},
		}
		service.mu.Lock()
		service.clients[id] = client
		service.mu.Unlock()
		return client
	}
	first := newClient("first")
	second := newClient("second")
	document := Document{URI: fileURI(filepath.Join(workspace.MainPath, "main.go")), LanguageID: "go", Version: 1, Text: "package main"}
	nonMatching := document
	nonMatching.URI = fileURI(filepath.Join(workspace.MainPath, "main.py"))
	if err := service.ClaimDocument(first, current.profile.ID, nonMatching, false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("non-matching selector error = %v", err)
	}
	if err := service.ClaimDocument(first, current.profile.ID, document, false); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := service.ClaimDocument(second, current.profile.ID, document, false); err != ErrLeaseDenied {
		t.Fatalf("second claim error = %v", err)
	}
	document.Text = "package takeover"
	if err := service.ClaimDocument(second, current.profile.ID, document, true); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := service.ChangeDocument(first, current.profile.ID, document.URI, 2, json.RawMessage(`[{"text":"old owner"}]`)); err != ErrLeaseRequired {
		t.Fatalf("old owner change error = %v", err)
	}
	if err := service.ChangeDocument(second, current.profile.ID, document.URI, 1, json.RawMessage(`[{"text":"stale"}]`)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale version error = %v", err)
	}
	if err := service.ChangeDocument(second, current.profile.ID, document.URI, 2, json.RawMessage(`[{"text":"fresh"}]`)); err != nil {
		t.Fatalf("fresh change: %v", err)
	}
	second.Close()
	service.mu.Lock()
	_, leased := service.leases[leaseKey(workspace.ID, current.profile.ID, document.URI)]
	service.mu.Unlock()
	if leased {
		t.Fatal("lease remained after browser disconnect")
	}
	messagesMu.Lock()
	firstMessages := len(messages["first"])
	secondMessages := len(messages["second"])
	messagesMu.Unlock()
	if firstMessages == 0 || secondMessages == 0 {
		t.Fatalf("lease events were not emitted: first=%d second=%d", firstMessages, secondMessages)
	}
	first.Close()
}

func TestPublishDiagnosticsMatchesNormalizedDocumentURI(t *testing.T) {
	workspace := workspaces.Workspace{ID: "workspace"}
	profile := lspconfig.Profile{ID: "gopls"}
	current := &serverRuntime{workspace: workspace, profile: profile}
	service := NewService(nil, nil)
	browserURI := "file:///C%3A/Users/test/project/main.go"
	serverURI := "file:///C:/Users/test/project/main.go"
	var received json.RawMessage
	observerMessages := 0
	client := &Client{
		ID: "browser", WorkspaceID: workspace.ID,
		documents: map[string]Document{}, pending: map[string]chan serverRequestResponse{},
		send: func(value any) {
			message := value.(map[string]any)
			received = append(json.RawMessage(nil), message["params"].(json.RawMessage)...)
		},
	}
	service.clients[client.ID] = client
	service.clients["observer"] = &Client{
		ID: "observer", WorkspaceID: workspace.ID,
		documents: map[string]Document{}, pending: map[string]chan serverRequestResponse{},
		send: func(any) { observerMessages++ },
	}
	service.leases[leaseKey(workspace.ID, profile.ID, browserURI)] = &documentLease{clientID: client.ID, uri: browserURI}
	service.runtimeNotification(current, "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"`+serverURI+`","diagnostics":[]}`))
	if uri := documentURI(received); uri != browserURI {
		t.Fatalf("forwarded diagnostic URI = %q, want browser URI %q", uri, browserURI)
	}
	if observerMessages != 0 {
		t.Fatalf("leased diagnostics reached a non-owning browser: %d messages", observerMessages)
	}
}

func TestPublishDiagnosticsBroadcastsUnleasedWorkspaceFiles(t *testing.T) {
	workspace := workspaces.Workspace{ID: "workspace", MainPath: t.TempDir()}
	profile := lspconfig.Profile{ID: "gopls"}
	current := &serverRuntime{workspace: workspace, profile: profile}
	service := NewService(nil, nil)
	messages := map[string][]map[string]any{"first": {}, "second": {}}
	otherWorkspaceMessages := 0
	for _, id := range []string{"first", "second"} {
		id := id
		service.clients[id] = &Client{
			ID: id, WorkspaceID: workspace.ID,
			documents: map[string]Document{}, pending: map[string]chan serverRequestResponse{},
			send: func(value any) { messages[id] = append(messages[id], value.(map[string]any)) },
		}
	}
	service.clients["other-workspace"] = &Client{
		ID: "other-workspace", WorkspaceID: "other",
		documents: map[string]Document{}, pending: map[string]chan serverRequestResponse{},
		send: func(any) { otherWorkspaceMessages++ },
	}
	uri := fileURI(filepath.Join(workspace.MainPath, "sibling.go"))
	service.runtimeNotification(current, "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"`+uri+`","diagnostics":[{"severity":1,"message":"broken"}]}`))
	service.runtimeNotification(current, "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"`+uri+`","diagnostics":[]}`))
	for id, received := range messages {
		if len(received) != 2 {
			t.Fatalf("%s received %d workspace diagnostic messages, want 2", id, len(received))
		}
		if received[0]["profileId"] != profile.ID || received[0]["method"] != "textDocument/publishDiagnostics" {
			t.Fatalf("%s received malformed diagnostic envelope: %#v", id, received[0])
		}
		if got := documentURI(received[1]["params"].(json.RawMessage)); got != uri {
			t.Fatalf("%s clear diagnostic URI = %q, want %q", id, got, uri)
		}
	}
	if otherWorkspaceMessages != 0 {
		t.Fatalf("workspace diagnostics reached another workspace: %d messages", otherWorkspaceMessages)
	}

	outsideURI := fileURI(filepath.Join(t.TempDir(), "outside.go"))
	service.runtimeNotification(current, "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"`+outsideURI+`","diagnostics":[]}`))
	service.runtimeNotification(current, "textDocument/publishDiagnostics", json.RawMessage(`{"uri":"untitled:outside","diagnostics":[]}`))
	for id, received := range messages {
		if len(received) != 2 {
			t.Fatalf("%s received out-of-workspace diagnostics: %d messages", id, len(received))
		}
	}
}

func TestLeaseKeyIgnoresWindowsFileURICasing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows file URI casing is platform-specific")
	}
	browserURI := "file:///c%3A/Users/Test/Project/main.go"
	serverURI := "file:///C:/users/test/project/main.go"
	if browser, server := leaseKey("workspace", "gopls", browserURI), leaseKey("workspace", "gopls", serverURI); browser != server {
		t.Fatalf("equivalent Windows URI lease keys differ:\n%q\n%q", browser, server)
	}
}

func TestMalformedServerStreamTriggersSupervision(t *testing.T) {
	current, service, workspace, _ := startTestRuntimeWithEnvironment(t, map[string]string{"ECHO_FAKE_LSP_MALFORMED": "1"})
	waitFor(t, 3*time.Second, func() bool { return current.status().RestartCount > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	current.stop(ctx)
	service.mu.Lock()
	delete(service.runtimes, runtimeKey(workspace.ID, current.profile.ID))
	service.mu.Unlock()
}

func startTestRuntime(t *testing.T) (*serverRuntime, *Service, workspaces.Workspace, string) {
	return startTestRuntimeWithEnvironment(t, nil)
}

func startTestRuntimeWithEnvironment(t *testing.T, extraEnvironment map[string]string) (*serverRuntime, *Service, workspaces.Workspace, string) {
	t.Helper()
	mainPath := filepath.Join(t.TempDir(), "main")
	extraPath := filepath.Join(filepath.Dir(mainPath), "extra")
	for _, path := range []string{mainPath, extraPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(filepath.Dir(mainPath), "initialize.json")
	workspace := workspaces.Workspace{ID: "workspace", Name: "Workspace", MainPath: mainPath, Folders: []string{mainPath, extraPath}}
	profile := lspconfig.Profile{
		ID: "fake", Name: "Fake LSP", Command: os.Args[0], Args: []string{"-test.run=^TestLSPFakeProcess$"},
		Selectors:             []lspconfig.DocumentSelector{{LanguageID: "go", Extensions: []string{".go"}}},
		Environment:           map[string]string{"ECHO_FAKE_LSP": "1", "ECHO_FAKE_LSP_LOG": logPath},
		InitializationOptions: map[string]any{"test": true}, Settings: map[string]any{"fake": true},
	}
	for key, value := range extraEnvironment {
		profile.Environment[key] = value
	}
	service := NewService(nil, nil)
	current := newServerRuntime(service, workspace, profile)
	service.runtimes[runtimeKey(workspace.ID, profile.ID)] = current
	current.start()
	waitFor(t, 4*time.Second, func() bool { return current.status().State == "running" })
	return current, service, workspace, logPath
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func writeTestRPCMessage(writer *os.File, message rpcMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}
