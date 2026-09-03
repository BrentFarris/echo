package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/agentmodes"
)

func TestAgentModesAPICRUD(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "mode-api")

	body := `{"workspaceId":"` + workspace.ID + `","mode":{"name":"Reviewer","prompt":"Review the implementation.","permissions":{"filesystem_read_text":{"paths":["src/**"]}}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent-modes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rr.Code, rr.Body.String())
	}
	var created struct {
		Data struct {
			Modes []agentmodes.Mode `json:"modes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Data.Modes) != 4 || created.Data.Modes[3].Name != "Reviewer" {
		t.Fatalf("unexpected create response: %+v", created.Data.Modes)
	}
	id := created.Data.Modes[3].ID

	rr = doRequest(t, s, http.MethodGet, "/api/agent-modes?workspaceId="+workspace.ID)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "filesystem_read_text") || !strings.Contains(rr.Body.String(), "Review the implementation") {
		t.Fatalf("unexpected list response %d: %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/agent-modes/"+id+"?workspaceId="+workspace.ID, nil)
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "Review the implementation") {
		t.Fatalf("deleted mode remained in response: %s", rr.Body.String())
	}
}

func TestAgentModesAPIProtectsBuiltIns(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "mode-builtins")
	req := httptest.NewRequest(http.MethodDelete, "/api/agent-modes/general?workspaceId="+workspace.ID, nil)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
