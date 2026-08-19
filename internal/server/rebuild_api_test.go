package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/rebuild"
	"github.com/brent/echo/internal/workspaces"
)

type fakeRebuilder struct {
	request rebuild.Request
	result  rebuild.Result
	err     error
}

func (f *fakeRebuilder) BuildAndPrepare(_ context.Context, request rebuild.Request) (rebuild.Result, error) {
	f.request = request
	return f.result, f.err
}

func registerEchoSource(t *testing.T, s *Server, name string) workspaces.Workspace {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/brent/echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := s.workspaces.Create(workspaces.CreateRequest{Name: name, MainPath: dir})
	if err != nil {
		t.Fatalf("register Echo workspace: %v", err)
	}
	return workspace
}

func TestRebuildRelaunchPreparesReplacementAndRequestsShutdown(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := registerEchoSource(t, s, "Echo")
	fake := &fakeRebuilder{result: rebuild.Result{SourcePath: workspace.MainPath, LogPath: filepath.Join(t.TempDir(), "rebuild.log")}}
	s.rebuilder = fake
	s.processID = 1234
	s.processArgs = []string{"-port", "4872"}
	s.workingDir = workspace.MainPath

	rr := doRequest(t, s, http.MethodPost, "/api/development/rebuild-relaunch")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if fake.request.SourceDir != workspace.MainPath || fake.request.ProcessID != 1234 || len(fake.request.Arguments) != 2 {
		t.Fatalf("request = %#v", fake.request)
	}
	select {
	case <-s.RestartRequested():
	default:
		t.Fatal("restart was not requested")
	}
	var payload struct {
		Data struct {
			Status     string `json:"status"`
			InstanceID string `json:"instanceId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Status != "restarting" || payload.Data.InstanceID != s.instanceID {
		t.Fatalf("response = %#v", payload.Data)
	}
}

func TestRebuildRelaunchRequiresRegisteredEchoSource(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodPost, "/api/development/rebuild-relaunch")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestRebuildRelaunchReportsBuildFailureWithoutShutdown(t *testing.T) {
	s, _ := newTestServer(t)
	registerEchoSource(t, s, "Echo")
	logPath := filepath.Join(t.TempDir(), "rebuild.log")
	s.rebuilder = &fakeRebuilder{err: &rebuild.BuildError{Stage: "server build", LogPath: logPath, Err: errors.New("compile error")}}

	rr := doRequest(t, s, http.MethodPost, "/api/development/rebuild-relaunch")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	select {
	case <-s.RestartRequested():
		t.Fatal("restart requested after build failure")
	default:
	}
	var payload errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "rebuild_failed" {
		t.Fatalf("error code = %q", payload.Code)
	}
}

func TestRebuildRelaunchPrefersActiveWorkspace(t *testing.T) {
	s, _ := newTestServer(t)
	registerEchoSource(t, s, "First Echo")
	active := registerEchoSource(t, s, "Active Echo")
	if err := s.workspaces.SetActive(active.ID); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRebuilder{result: rebuild.Result{SourcePath: active.MainPath}}
	s.rebuilder = fake
	rr := doRequest(t, s, http.MethodPost, "/api/development/rebuild-relaunch")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if fake.request.SourceDir != active.MainPath {
		t.Fatalf("source = %q, want active %q", fake.request.SourceDir, active.MainPath)
	}
}

func TestHealthIncludesProcessInstanceID(t *testing.T) {
	s, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), s.instanceID) {
		t.Fatalf("health response = %d %s", rr.Code, rr.Body.String())
	}
}
