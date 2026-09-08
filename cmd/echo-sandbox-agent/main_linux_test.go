//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brent/echo/internal/sandboxprotocol"
)

func TestHealthReportsBuiltProtocolRatherThanHostEnvironment(t *testing.T) {
	t.Setenv("ECHO_SANDBOX_PROTOCOL", "incompatible-host-version")
	service := &agent{role: "workbench"}
	response := httptest.NewRecorder()
	service.health(response, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	var health struct {
		ProtocolVersion string `json:"protocolVersion"`
		Role            string `json:"role"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || health.ProtocolVersion != sandboxprotocol.Version || health.Role != "workbench" {
		t.Fatalf("unexpected health: %d %s", response.Code, response.Body.String())
	}
}
