package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/ctest"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestCTestingConfigLensesAndValidationAPI(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Shutdown(t.Context())
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := "int main(void);\n\nint main(void) { return 0; }\n"
	entryPath := filepath.Join(rootPath, "test_main.c")
	if err := os.WriteFile(entryPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "C Tests", MainPath: rootPath})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := server.fs.Roots(workspace.ID)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots = %#v, %v", roots, err)
	}
	base := "/api/workspaces/" + workspace.ID + "/testing/c"
	config := gotestconfig.CConfig{CodeLens: true, Coverage: true, Targets: []gotestconfig.CTarget{{
		ID: "unit", Name: "Unit tests", Entry: gotestconfig.CEntry{File: "${workspaceFolder}/test_main.c", Function: "main"},
		Executable: "${workspaceFolder}/tests.exe", SourceRoots: []string{"${workspaceFolder}/src"},
		Coverage: gotestconfig.CCoverage{Provider: "gcov", ObjectRoots: []string{"${workspaceFolder}"}},
	}}}
	update := doJSONRequest(t, server, http.MethodPut, base+"/config", map[string]any{"config": config})
	if update.Code != http.StatusOK {
		t.Fatalf("config update: %d %s", update.Code, update.Body.String())
	}
	ref := workspacefs.FileRef{RootID: roots[0].ID, Path: "test_main.c"}
	response := doJSONRequest(t, server, http.MethodPost, base+"/lenses", ctest.LensRequest{Ref: ref, Text: source})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"title":"run C tests: Unit tests"`) || !strings.Contains(response.Body.String(), `"targetId":"unit"`) {
		t.Fatalf("lenses: %d %s", response.Code, response.Body.String())
	}
	goUpdate := doJSONRequest(t, server, http.MethodPut, "/api/workspaces/"+workspace.ID+"/testing/go/config", map[string]any{"config": map[string]any{
		"codeLens": true, "coverage": true, "timeout": "5s", "flags": []string{}, "environment": map[string]string{},
	}})
	if goUpdate.Code != http.StatusOK {
		t.Fatalf("Go config update: %d %s", goUpdate.Code, goUpdate.Body.String())
	}
	cAfterGo := doJSONRequest(t, server, http.MethodGet, base+"/config", nil)
	if cAfterGo.Code != http.StatusOK || !strings.Contains(cAfterGo.Body.String(), `"id":"unit"`) {
		t.Fatalf("Go config update discarded C targets: %d %s", cAfterGo.Code, cAfterGo.Body.String())
	}
	missing := doJSONRequest(t, server, http.MethodPost, base+"/runs", ctest.RunRequest{TargetID: "missing"})
	if missing.Code != http.StatusConflict {
		t.Fatalf("missing target: %d %s", missing.Code, missing.Body.String())
	}
	genericMissing := doJSONRequest(t, server, http.MethodPost, "/api/workspaces/"+workspace.ID+"/testing/runs/missing/rerun", map[string]any{})
	if genericMissing.Code != http.StatusNotFound {
		t.Fatalf("generic rerun: %d %s", genericMissing.Code, genericMissing.Body.String())
	}
}
