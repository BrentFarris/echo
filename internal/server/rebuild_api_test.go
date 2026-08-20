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
	"time"

	"github.com/brent/echo/internal/echoupdate"
	"github.com/brent/echo/internal/rebuild"
	"github.com/brent/echo/internal/workspaces"
)

type fakeRebuilder struct {
	request rebuild.Request
	result  rebuild.Result
	err     error
	updated bool
}

func (f *fakeRebuilder) BuildAndPrepare(_ context.Context, request rebuild.Request) (rebuild.Result, error) {
	f.request = request
	return f.result, f.err
}

func (f *fakeRebuilder) UpdateAndPrepare(_ context.Context, request rebuild.Request) (rebuild.Result, error) {
	f.request = request
	f.updated = true
	return f.result, f.err
}

type fakeEchoUpdateChecker struct {
	source string
	status echoupdate.Status
	err    error
}

func (f *fakeEchoUpdateChecker) Check(_ context.Context, source string) (echoupdate.Status, error) {
	f.source = source
	return f.status, f.err
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

func TestEchoUpdateStatusComparesRegisteredSource(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := registerEchoSource(t, s, "Echo")
	checker := &fakeEchoUpdateChecker{status: echoupdate.Status{
		UpdateAvailable:    true,
		LocalMasterCommit:  "1111111111111111111111111111111111111111",
		RemoteMasterCommit: "2222222222222222222222222222222222222222",
		CheckedAt:          time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC),
	}}
	s.updateChecker = checker

	rr := doRequest(t, s, http.MethodGet, "/api/development/update-status")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"updateAvailable":true`) {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if checker.source != workspace.MainPath {
		t.Fatalf("source = %q, want %q", checker.source, workspace.MainPath)
	}
}

func TestEchoUpdateStatusReportsCheckFailure(t *testing.T) {
	s, _ := newTestServer(t)
	registerEchoSource(t, s, "Echo")
	s.updateChecker = &fakeEchoUpdateChecker{err: errors.New("GitHub unavailable")}

	rr := doRequest(t, s, http.MethodGet, "/api/development/update-status")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "update_check_failed" {
		t.Fatalf("code = %q", payload.Code)
	}
}

func TestEchoUpdatePullsBuildsAndRequestsShutdown(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := registerEchoSource(t, s, "Echo")
	fake := &fakeRebuilder{result: rebuild.Result{SourcePath: workspace.MainPath, LogPath: filepath.Join(t.TempDir(), "rebuild.log")}}
	s.rebuilder = fake

	rr := doRequest(t, s, http.MethodPost, "/api/development/update")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !fake.updated || fake.request.SourceDir != workspace.MainPath {
		t.Fatalf("update request = %#v, updated = %v", fake.request, fake.updated)
	}
	select {
	case <-s.RestartRequested():
	default:
		t.Fatal("restart was not requested")
	}
}

func TestEchoUpdateRequiresMasterWithoutShutdown(t *testing.T) {
	s, _ := newTestServer(t)
	registerEchoSource(t, s, "Echo")
	s.rebuilder = &fakeRebuilder{err: &rebuild.BuildError{Stage: "update branch check", LogPath: "update.log", Err: rebuild.ErrMasterNotCheckedOut}}

	rr := doRequest(t, s, http.MethodPost, "/api/development/update")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "update_requires_master" {
		t.Fatalf("code = %q", payload.Code)
	}
	select {
	case <-s.RestartRequested():
		t.Fatal("restart requested after update precondition failure")
	default:
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
