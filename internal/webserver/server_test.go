package webserver

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brent/echo/internal/services"
)

func TestWebServerRequiresToken(t *testing.T) {
	service, _ := newWebServerTestService(t)
	server := httptest.NewServer(New(service, testAssets()).routes())
	defer server.Close()

	response := postRPC(t, server.URL, "", "LoadState", `{"args":[]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected missing token to return 401, got %d", response.StatusCode)
	}
	_ = response.Body.Close()

	response = postRPC(t, server.URL, "bad-token", "LoadState", `{"args":[]}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid token to return 401, got %d", response.StatusCode)
	}
	_ = response.Body.Close()
}

func TestWebServerRPCCallsAllowlistedServiceMethod(t *testing.T) {
	service, token := newWebServerTestService(t)
	server := httptest.NewServer(New(service, testAssets()).routes())
	defer server.Close()

	response := postRPC(t, server.URL, token, "LoadState", `{"args":[]}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected LoadState to succeed, got %d: %s", response.StatusCode, body)
	}
	var payload struct {
		Result services.AppState `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Result.WebAccess.AccessToken != token {
		t.Fatalf("expected RPC response to include current web access settings")
	}
}

func TestWebServerRejectsDisallowedRPCMethod(t *testing.T) {
	service, token := newWebServerTestService(t)
	server := httptest.NewServer(New(service, testAssets()).routes())
	defer server.Close()

	response := postRPC(t, server.URL, token, "Shutdown", `{"args":[]}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected disallowed method to return 404, got %d", response.StatusCode)
	}

	for _, method := range []string{"ReadExternalTextFile", "SaveExternalTextFile"} {
		response := postRPC(t, server.URL, token, method, `{"args":[]}`)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("expected desktop-only %s to return 404, got %d", method, response.StatusCode)
		}
	}
}

func TestWebServerSSEReceivesServiceEventsAndTokenRotationClosesOldClient(t *testing.T) {
	service, token := newWebServerTestService(t)
	state, err := service.AddWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	server := httptest.NewServer(New(service, testAssets()).routes())
	defer server.Close()

	response, lines := openEvents(t, server.URL, token)
	defer response.Body.Close()

	if _, err := service.ClearWorkspaceChangeReview(workspaceID); err != nil {
		t.Fatalf("clear change review: %v", err)
	}
	if !waitForLine(lines, "event: echo:file-changes:event") {
		t.Fatalf("expected file changes SSE event")
	}

	if _, err := service.RotateWebAccessToken(); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if _, err := service.ClearWorkspaceChangeReview(workspaceID); err != nil {
		t.Fatalf("clear change review after rotation: %v", err)
	}
	if !waitForLine(lines, "EOF") {
		t.Fatalf("expected old SSE client to close after token rotation")
	}
}

func TestWebServerFailedBindKeepsPreviousServerRunning(t *testing.T) {
	service, token := newWebServerTestService(t)
	server := New(service, testAssets())

	firstPort := freeTCPPort(t)
	first := services.WebAccessSettings{
		Enabled:     true,
		BindHost:    "127.0.0.1",
		Port:        firstPort,
		AccessToken: token,
	}
	status, err := server.ApplyWebAccessSettings(first)
	if err != nil {
		t.Fatalf("start first server: %v", err)
	}
	if !status.Running {
		t.Fatalf("expected first server to be running")
	}
	defer server.Shutdown(nil)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port

	second := first
	second.Port = occupiedPort
	status, err = server.ApplyWebAccessSettings(second)
	if err == nil {
		t.Fatalf("expected occupied port to fail")
	}
	if !status.Running {
		t.Fatalf("expected previous server to remain running after failed bind")
	}

	response := postRPC(t, "http://"+server.address, token, "LoadState", `{"args":[]}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected previous server to respond, got %d: %s", response.StatusCode, body)
	}
}

func newWebServerTestService(t *testing.T) (*services.SystemService, string) {
	t.Helper()
	service := services.NewSystemServiceWithStorePath(t.TempDir() + "/state.json")
	settings := service.LoadState().WebAccess
	settings.Enabled = true
	settings.BindHost = "127.0.0.1"
	settings.Port = 3740
	state, err := service.SaveWebAccessSettings(settings)
	if err != nil {
		t.Fatalf("save web settings: %v", err)
	}
	return service, state.WebAccess.AccessToken
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><body>Echo</body></html>")},
	}
}

func postRPC(t *testing.T, serverURL string, token string, method string, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+rpcPrefix+method, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post rpc: %v", err)
	}
	return response
}

func openEvents(t *testing.T, serverURL string, token string) (*http.Response, <-chan string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, serverURL+eventsRoute+"?access_token="+token, nil)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("expected events to open, got %d: %s", response.StatusCode, body)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		lines <- "EOF"
	}()
	return response, lines
}

func waitForLine(lines <-chan string, expected string) bool {
	timeout := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			if line == expected {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port <= 0 {
		t.Fatalf("invalid port %s", strconv.Itoa(port))
	}
	return port
}
