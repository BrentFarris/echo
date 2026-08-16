package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/services"
)

func TestPrepareHeadlessWebAccessSettings(t *testing.T) {
	tests := []struct {
		name     string
		current  services.WebAccessSettings
		port     int
		bindHost string
		want     services.WebAccessSettings
	}{
		{
			name: "force enable and keep saved values",
			current: services.WebAccessSettings{
				Enabled:     false,
				BindHost:    "127.0.0.1",
				Port:        4321,
				AccessToken: "token-abc",
			},
			port:     0,
			bindHost: "",
			want: services.WebAccessSettings{
				Enabled:     true,
				BindHost:    "127.0.0.1",
				Port:        4321,
				AccessToken: "token-abc",
			},
		},
		{
			name: "port and bind overrides win",
			current: services.WebAccessSettings{
				Enabled:     true,
				BindHost:    "127.0.0.1",
				Port:        4321,
				AccessToken: "token-abc",
			},
			port:     5000,
			bindHost: "0.0.0.0",
			want: services.WebAccessSettings{
				Enabled:     true,
				BindHost:    "0.0.0.0",
				Port:        5000,
				AccessToken: "token-abc",
			},
		},
		{
			name: "empty bind falls back to all interfaces",
			current: services.WebAccessSettings{
				Enabled:     false,
				BindHost:    "   ",
				Port:        4321,
				AccessToken: "token-abc",
			},
			port:     0,
			bindHost: "",
			want: services.WebAccessSettings{
				Enabled:     true,
				BindHost:    "0.0.0.0",
				Port:        4321,
				AccessToken: "token-abc",
			},
		},
		{
			name: "invalid port is ignored and saved port kept",
			current: services.WebAccessSettings{
				Enabled:     false,
				BindHost:    "127.0.0.1",
				Port:        4321,
				AccessToken: "token-abc",
			},
			port:     99999,
			bindHost: "",
			want: services.WebAccessSettings{
				Enabled:     true,
				BindHost:    "127.0.0.1",
				Port:        4321,
				AccessToken: "token-abc",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := prepareHeadlessWebAccessSettings(test.current, test.port, test.bindHost)
			if got != test.want {
				t.Fatalf("prepareHeadlessWebAccessSettings = %+v, want %+v", got, test.want)
			}
		})
	}
}

// portProbingController captures the actual bound port of a web server started
// on an ephemeral port (port 0), then stops it so the real headless run can
// bind the same port.
type portProbingController struct {
	port int
	done chan struct{}
}

func newPortProbingController() *portProbingController {
	return &portProbingController{done: make(chan struct{})}
}

func (c *portProbingController) ApplyWebAccessSettings(settings services.WebAccessSettings) (services.WebAccessStatus, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", settings.Port))
	if err != nil {
		return services.WebAccessStatus{}, err
	}
	c.port = listener.Addr().(*net.TCPAddr).Port
	status := services.WebAccessStatus{
		Enabled:  true,
		Running:  true,
		BindHost: settings.BindHost,
		Port:     c.port,
	}
	close(c.done)
	return status, listener.Close()
}

func (c *portProbingController) LoadWebAccessStatus(settings services.WebAccessSettings) services.WebAccessStatus {
	return services.WebAccessStatus{Enabled: settings.Enabled, BindHost: settings.BindHost, Port: c.port}
}

// TestRunHeadlessWithInterruptEndToEnd boots the real headless flow against an
// isolated temp state store and verifies that a remote browser can authenticate
// with the saved token, that unauthorized requests are rejected, and that no
// runtime-only web access change leaks into state.json.
func TestRunHeadlessWithInterruptEndToEnd(t *testing.T) {
	const token = "headless-e2e-token-0123456789"

	storePath := filepath.Join(t.TempDir(), "state.json")
	seed := map[string]any{
		"settings":    map[string]any{"endpoint": "http://localhost:1", "model": "test-model"},
		"webAccess":   map[string]any{"enabled": false, "bindHost": "0.0.0.0", "port": 3740, "accessToken": token},
		"workspaces":  []any{},
		"kanbanCards": []any{},
	}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed state: %v", err)
	}
	if err := os.WriteFile(storePath, data, 0o600); err != nil {
		t.Fatalf("write seed state: %v", err)
	}

	// One shared service instance so the controller below can probe an
	// ephemeral port that the headless run will then bind.
	system := services.NewSystemServiceWithStorePath(storePath)
	prober := newPortProbingController()
	services.SetWebAccessController(system, prober)
	if _, err := prober.ApplyWebAccessSettings(services.WebAccessSettings{BindHost: "127.0.0.1", Port: 0}); err != nil {
		t.Fatalf("probe ephemeral port: %v", err)
	}
	select {
	case <-prober.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out probing ephemeral port")
	}
	port := prober.port

	interrupt, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- runHeadlessWithInterrupt(interrupt, port, "127.0.0.1", func() *services.SystemService { return system })
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Wait for the headless server to accept connections.
	var resp *http.Response
	for start := time.Now(); time.Since(start) < 15*time.Second; {
		resp, err = http.Get(baseURL + "/")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if resp == nil {
		t.Fatalf("headless server did not start: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// A remote browser with the saved token can call whitelisted RPC methods.
	rpcReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/rpc/SystemService/AppInfo", bytes.NewReader([]byte(`{"args":[]}`)))
	if err != nil {
		t.Fatalf("build rpc request: %v", err)
	}
	rpcReq.Header.Set("Content-Type", "application/json")
	rpcReq.Header.Set("X-Echo-Access-Token", token)
	resp, err = http.DefaultClient.Do(rpcReq)
	if err != nil {
		t.Fatalf("rpc AppInfo: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rpc AppInfo with token: status = %d, body = %s", resp.StatusCode, string(body))
	}
	var rpcResult struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResult); err != nil {
		t.Fatalf("decode rpc result: %v (body = %s)", err, string(body))
	}
	if rpcResult.Result.Name == "" {
		t.Fatalf("expected non-empty app name in rpc result: %s", string(body))
	}

	// Requests without the token are rejected.
	resp, err = http.Post(baseURL+"/api/rpc/SystemService/AppInfo", "application/json", bytes.NewReader([]byte(`{"args":[]}`)))
	if err != nil {
		t.Fatalf("rpc AppInfo unauthorized: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rpc AppInfo without token: status = %d, want 401", resp.StatusCode)
	}

	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("runHeadlessWithInterrupt exit code = %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for headless shutdown")
	}

	// The saved settings must remain disabled on disk: headless force-enable is
	// runtime-only.
	persisted, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
	var state struct {
		WebAccess struct {
			Enabled     bool   `json:"enabled"`
			AccessToken string `json:"accessToken"`
		} `json:"webAccess"`
	}
	if err := json.Unmarshal(persisted, &state); err != nil {
		t.Fatalf("decode persisted state: %v", err)
	}
	if state.WebAccess.Enabled {
		t.Fatal("headless mode leaked enabled web access into state.json")
	}
	if state.WebAccess.AccessToken != token {
		t.Fatalf("persisted token changed: got %q, want %q", state.WebAccess.AccessToken, token)
	}
}
