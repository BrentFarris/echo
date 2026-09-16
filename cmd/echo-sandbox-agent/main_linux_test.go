//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sandboxprotocol"
)

func TestHealthReportsBuiltProtocolRatherThanHostEnvironment(t *testing.T) {
	t.Setenv("ECHO_SANDBOX_PROTOCOL", "incompatible-host-version")
	service := &agent{role: "protocol-test"}
	response := httptest.NewRecorder()
	service.health(response, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	var health struct {
		ProtocolVersion string `json:"protocolVersion"`
		Role            string `json:"role"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || health.ProtocolVersion != sandboxprotocol.Version || health.Role != "protocol-test" {
		t.Fatalf("unexpected health: %d %s", response.Code, response.Body.String())
	}
}

func TestRuntimeHealthFailsWithoutDesktopSession(t *testing.T) {
	t.Setenv("DISPLAY", ":9999")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	response := httptest.NewRecorder()
	(&agent{role: "runtime"}).health(response, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("partial runtime marked healthy: %d", response.Code)
	}
}
func TestBaseEnvironmentIncludesSharedDesktopAndToolchain(t *testing.T) {
	t.Setenv("PATH", "/opt/custom-tools/bin:/usr/bin:/bin")
	for key, value := range map[string]string{"DISPLAY": ":1", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus", "XDG_RUNTIME_DIR": "/run/user/1000", "ECHO_SANDBOX_ROLE": "runtime"} {
		t.Setenv(key, value)
	}
	env := strings.Join(baseEnvironment(), "\n")
	for _, expected := range []string{"DISPLAY=:1", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "XDG_RUNTIME_DIR=/run/user/1000", "HOME=/home/echo", "/usr/local/go/bin", "/home/echo/go/bin", "ECHO_SANDBOX_ROLE=runtime", "/opt/custom-tools/bin"} {
		if !strings.Contains(env, expected) {
			t.Fatalf("missing %s in %s", expected, env)
		}
	}
}
