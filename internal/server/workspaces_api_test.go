package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetWorkspacesEmpty(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodGet, "/api/workspaces")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Workspaces []map[string]any `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Fatalf("expected ok=true")
	}
	if len(env.Data.Workspaces) != 0 {
		t.Fatalf("expected no workspaces, got %d", len(env.Data.Workspaces))
	}
}

func TestCreateWorkspaceAndGetIcon(t *testing.T) {
	s, dir := newTestServer(t)
	main := filepath.Join(dir, "main")
	extra := filepath.Join(dir, "extra")
	for _, p := range []string{main, extra} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	body := map[string]any{
		"name":     "Alpha",
		"mainPath": main,
		"folders":  []string{extra},
		"icon":     map[string]any{"data": []byte("fake-png"), "ext": "png"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Workspace struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				MainPath string   `json:"mainPath"`
				IconExt  string   `json:"iconExt"`
				Folders  []string `json:"folders"`
			} `json:"workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Workspace.ID == "" {
		t.Fatal("expected workspace id")
	}
	if env.Data.Workspace.Name != "Alpha" {
		t.Fatalf("unexpected name %q", env.Data.Workspace.Name)
	}
	if env.Data.Workspace.IconExt != "png" {
		t.Fatalf("expected iconExt png, got %q", env.Data.Workspace.IconExt)
	}
	if len(env.Data.Workspace.Folders) != 2 || env.Data.Workspace.Folders[0] != main || env.Data.Workspace.Folders[1] != extra {
		t.Fatalf("unexpected folders: %v", env.Data.Workspace.Folders)
	}

	// The .echo workspace.json should exist on disk.
	if _, err := os.Stat(filepath.Join(main, ".echo", "workspace.json")); err != nil {
		t.Fatalf("expected .echo/workspace.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(main, ".echo", "icon.png")); err != nil {
		t.Fatalf("expected .echo/icon.png: %v", err)
	}

	// GET workspaces should now include it.
	rr2 := doRequest(t, s, http.MethodGet, "/api/workspaces")
	var env2 struct {
		Data struct {
			Workspaces []map[string]any `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &env2); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(env2.Data.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(env2.Data.Workspaces))
	}

	// Icon endpoint serves the image.
	id := env.Data.Workspace.ID
	iconRR := doRequest(t, s, http.MethodGet, "/api/workspaces/"+id+"/icon")
	if iconRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for icon, got %d", iconRR.Code)
	}
	if iconRR.Body.String() != "fake-png" {
		t.Fatalf("unexpected icon body %q", iconRR.Body.String())
	}
}

func TestCreateWorkspaceDuplicateName(t *testing.T) {
	s, dir := newTestServer(t)
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	create := func(name string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]any{"name": name, "mainPath": main})
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		return rr
	}
	if rr := create("Beta"); rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	if rr := create("beta"); rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate, got %d", rr.Code)
	}
}

func TestCreateWorkspaceInvalidPath(t *testing.T) {
	s, dir := newTestServer(t)
	payload, _ := json.Marshal(map[string]any{"name": "Gamma", "mainPath": filepath.Join(dir, "missing")})
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid path, got %d", rr.Code)
	}
}

func TestSetActiveWorkspace(t *testing.T) {
	s, dir := newTestServer(t)
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a workspace.
	createPayload, _ := json.Marshal(map[string]any{"name": "Active", "mainPath": main})
	createReq := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(createPayload)))
	createReq.Header.Set("Content-Type", "application/json")
	createRR := httptest.NewRecorder()
	s.routes().ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRR.Code)
	}
	var createEnv struct {
		Data struct {
			Workspace struct {
				ID string `json:"id"`
			} `json:"workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRR.Body.Bytes(), &createEnv); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id := createEnv.Data.Workspace.ID

	// Initially no active id.
	rr0 := doRequest(t, s, http.MethodGet, "/api/workspaces")
	var env0 struct {
		Data struct {
			ActiveID string `json:"activeId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr0.Body.Bytes(), &env0); err != nil {
		t.Fatalf("decode initial: %v", err)
	}
	if env0.Data.ActiveID != "" {
		t.Fatalf("expected no active id initially, got %q", env0.Data.ActiveID)
	}

	// Set active.
	activePayload, _ := json.Marshal(map[string]any{"id": id})
	activeReq := httptest.NewRequest(http.MethodPut, "/api/workspaces/active", strings.NewReader(string(activePayload)))
	activeReq.Header.Set("Content-Type", "application/json")
	activeRR := httptest.NewRecorder()
	s.routes().ServeHTTP(activeRR, activeReq)
	if activeRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", activeRR.Code, activeRR.Body.String())
	}

	// GET reflects the active id.
	rr1 := doRequest(t, s, http.MethodGet, "/api/workspaces")
	var env1 struct {
		Data struct {
			ActiveID string `json:"activeId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr1.Body.Bytes(), &env1); err != nil {
		t.Fatalf("decode after set: %v", err)
	}
	if env1.Data.ActiveID != id {
		t.Fatalf("expected active id %q, got %q", id, env1.Data.ActiveID)
	}

	// Setting an unknown id fails.
	badPayload, _ := json.Marshal(map[string]any{"id": "ws-nope"})
	badReq := httptest.NewRequest(http.MethodPut, "/api/workspaces/active", strings.NewReader(string(badPayload)))
	badReq.Header.Set("Content-Type", "application/json")
	badRR := httptest.NewRecorder()
	s.routes().ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown id, got %d", badRR.Code)
	}
}
