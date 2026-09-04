package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/workspaces"
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

func TestGetWorkspacesPrunesMissingConfigAndClearsActiveID(t *testing.T) {
	s, directory := newTestServer(t)
	main := filepath.Join(directory, "missing-config")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspaces.Create(workspaces.CreateRequest{Name: "Missing", MainPath: main})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.workspaces.SetActive(workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(main, ".echo", "workspace.json")); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, s, http.MethodGet, "/api/workspaces")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Data struct {
			Workspaces []map[string]any `json:"workspaces"`
			ActiveID   string           `json:"activeId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Workspaces) != 0 || response.Data.ActiveID != "" {
		t.Fatalf("stale workspace remained in API response: %+v", response.Data)
	}
	stored, err := os.ReadFile(filepath.Join(directory, "echo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Workspaces        []map[string]any `json:"workspaces"`
		ActiveWorkspaceID string           `json:"activeWorkspaceId"`
	}
	if err := json.Unmarshal(stored, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Workspaces) != 0 || persisted.ActiveWorkspaceID != "" {
		t.Fatalf("stale workspace remained in echo.json: %+v", persisted)
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
	mainA := filepath.Join(dir, "main-a")
	mainB := filepath.Join(dir, "main-b")
	for _, main := range []string{mainA, mainB} {
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	create := func(name, main string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]any{"name": name, "mainPath": main})
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		return rr
	}
	if rr := create("Beta", mainA); rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	if rr := create("beta", mainB); rr.Code != http.StatusBadRequest {
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

func TestCreateWorkspaceRebindsExistingPortableWorkspace(t *testing.T) {
	s, directory := newTestServer(t)
	oldMain := filepath.Join(directory, "old", "project")
	newMain := filepath.Join(directory, "new", "project")
	for _, folder := range []string{oldMain, newMain} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	postWorkspace := func(name, mainPath string) (int, struct {
		Data struct {
			Workspace struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				MainPath string   `json:"mainPath"`
				Folders  []string `json:"folders"`
			} `json:"workspace"`
		} `json:"data"`
	}) {
		payload, _ := json.Marshal(map[string]any{"name": name, "mainPath": mainPath})
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		var response struct {
			Data struct {
				Workspace struct {
					ID       string   `json:"id"`
					Name     string   `json:"name"`
					MainPath string   `json:"mainPath"`
					Folders  []string `json:"folders"`
				} `json:"workspace"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return rr.Code, response
	}

	status, original := postWorkspace("Portable", oldMain)
	if status != http.StatusCreated {
		t.Fatalf("create original: %d", status)
	}
	echoDir := filepath.Join(newMain, ".echo")
	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(echoDir, "workspace.json"), []byte(`{"name":"Portable","mainPath":"../","folders":["../"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	status, rebound := postWorkspace("Ignored", newMain)
	if status != http.StatusCreated {
		t.Fatalf("rebind: %d", status)
	}
	if rebound.Data.Workspace.ID != original.Data.Workspace.ID || rebound.Data.Workspace.Name != "Portable" || rebound.Data.Workspace.MainPath != newMain {
		t.Fatalf("unexpected rebound workspace: %+v", rebound.Data.Workspace)
	}
	list := doRequest(t, s, http.MethodGet, "/api/workspaces")
	var listed struct {
		Data struct {
			Workspaces []map[string]any `json:"workspaces"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data.Workspaces) != 1 {
		t.Fatalf("expected one rebound registration, got %+v", listed.Data.Workspaces)
	}
}

func TestCreateWorkspaceRejectsMalformedExistingConfig(t *testing.T) {
	s, directory := newTestServer(t)
	main := filepath.Join(directory, "main")
	echoDir := filepath.Join(main, ".echo")
	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(echoDir, "workspace.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"name": "Ignored", "mainPath": main})
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "parse workspace config") {
		t.Fatalf("expected clear malformed-config response, got %d: %s", rr.Code, rr.Body.String())
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

func TestUpdateWorkspaceAPI(t *testing.T) {
	s, directory := newTestServer(t)
	main := filepath.Join(directory, "main")
	extra := filepath.Join(directory, "extra")
	for _, folder := range []string{main, extra} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := s.workspaces.Create(workspaces.CreateRequest{
		Name: "Before", MainPath: main, Icon: &workspaces.Icon{Data: []byte("old"), Ext: "png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"name": "After", "folders": []string{extra},
		"icon": map[string]any{"data": []byte("new"), "ext": "webp"},
	})
	request := httptest.NewRequest(http.MethodPut, "/api/workspaces/"+workspace.ID, strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Workspace workspaces.Workspace `json:"workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	updated := response.Data.Workspace
	if updated.ID != workspace.ID || updated.Name != "After" || updated.IconExt != "webp" || len(updated.Folders) != 2 || updated.Folders[1] != extra {
		t.Fatalf("unexpected update response: %+v", updated)
	}
	icon := doRequest(t, s, http.MethodGet, "/api/workspaces/"+workspace.ID+"/icon")
	if icon.Code != http.StatusOK || icon.Body.String() != "new" || icon.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("updated icon response: status=%d cache=%q body=%q", icon.Code, icon.Header().Get("Cache-Control"), icon.Body.String())
	}
}

func TestDeleteWorkspaceAPISelectsNextAndRetainsFiles(t *testing.T) {
	s, directory := newTestServer(t)
	created := make([]workspaces.Workspace, 0, 2)
	for _, name := range []string{"First", "Second"} {
		main := filepath.Join(directory, strings.ToLower(name))
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
		workspace, err := s.workspaces.Create(workspaces.CreateRequest{Name: name, MainPath: main})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, workspace)
	}
	if err := s.workspaces.SetActive(created[0].ID); err != nil {
		t.Fatal(err)
	}
	recorder := doRequest(t, s, http.MethodDelete, "/api/workspaces/"+created[0].ID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			DeletedID              string `json:"deletedId"`
			ActiveID               string `json:"activeId"`
			WorkspaceFilesRetained bool   `json:"workspaceFilesRetained"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.DeletedID != created[0].ID || response.Data.ActiveID != created[1].ID || !response.Data.WorkspaceFilesRetained {
		t.Fatalf("unexpected delete response: %+v", response.Data)
	}
	if _, err := os.Stat(filepath.Join(created[0].MainPath, ".echo", "workspace.json")); err != nil {
		t.Fatalf("workspace config was removed: %v", err)
	}
	list := doRequest(t, s, http.MethodGet, "/api/workspaces")
	if strings.Contains(list.Body.String(), created[0].ID) || !strings.Contains(list.Body.String(), created[1].ID) {
		t.Fatalf("unexpected workspace list: %s", list.Body.String())
	}
}

func TestDeleteUnavailableWorkspaceAPI(t *testing.T) {
	s, directory := newTestServer(t)
	main := filepath.Join(directory, "unavailable")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspaces.Create(workspaces.CreateRequest{Name: "Unavailable", MainPath: main})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	recorder := doRequest(t, s, http.MethodDelete, "/api/workspaces/"+workspace.ID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected unavailable workspace deletion to succeed, got %d: %s", recorder.Code, recorder.Body.String())
	}
	missing := doRequest(t, s, http.MethodDelete, "/api/workspaces/missing")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing workspace, got %d: %s", missing.Code, missing.Body.String())
	}
}
