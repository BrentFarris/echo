package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/gotest"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestGoTestingConfigLensAndTargetValidationAPI(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Shutdown(t.Context())
	rootPath := t.TempDir()
	source := "package sample\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(rootPath, "sample_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "Go Tests", MainPath: rootPath})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := server.fs.Roots(workspace.ID)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots = %#v, %v", roots, err)
	}
	ref := workspacefs.FileRef{RootID: roots[0].ID, Path: "sample_test.go"}
	base := "/api/workspaces/" + workspace.ID + "/testing/go"
	coverage := doJSONRequest(t, server, http.MethodGet, base+"/coverage", nil)
	if coverage.Code != http.StatusOK || !strings.Contains(coverage.Body.String(), `"coverage":null`) {
		t.Fatalf("initial coverage: %d %s", coverage.Code, coverage.Body.String())
	}

	response := doJSONRequest(t, server, http.MethodPost, base+"/lenses", gotest.LensRequest{Ref: ref, Text: source})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"title":"run package tests"`) || !strings.Contains(response.Body.String(), `"title":"debug test"`) {
		t.Fatalf("lenses: %d %s", response.Code, response.Body.String())
	}

	update := doJSONRequest(t, server, http.MethodPut, base+"/config", map[string]any{"config": gotestconfig.GoConfig{
		CodeLens: false, Coverage: false, Timeout: "5s", Flags: []string{"-count=1"}, Environment: map[string]string{"ECHO_ENV": "test"},
	}})
	if update.Code != http.StatusOK {
		t.Fatalf("config update: %d %s", update.Code, update.Body.String())
	}
	var envelope struct {
		Data struct {
			Config gotestconfig.GoConfig `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Config.CodeLens || envelope.Data.Config.Coverage || envelope.Data.Config.Timeout != "5s" {
		t.Fatalf("updated config = %#v", envelope.Data.Config)
	}

	disabled := doJSONRequest(t, server, http.MethodPost, base+"/lenses", gotest.LensRequest{Ref: ref, Text: source})
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"lenses":null`) {
		t.Fatalf("disabled lenses: %d %s", disabled.Code, disabled.Body.String())
	}

	invalid := doJSONRequest(t, server, http.MethodPost, base+"/runs", gotest.RunRequest{Ref: ref, Target: gotest.Target{Kind: gotest.TargetTest, Name: "Injected", Path: []string{"Injected"}}})
	if invalid.Code != http.StatusConflict {
		t.Fatalf("invalid target: %d %s", invalid.Code, invalid.Body.String())
	}
}
