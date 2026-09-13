package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestSourceRegistryUsesSessionIdentityAndOriginalPath(t *testing.T) {
	service, current, _, cleanup := stoppedPipeSession(t, 4, 7)
	defer cleanup()
	service.workspaces = staticWorkspaceResolver{workspaces.Workspace{ID: "workspace"}}
	filename := filepath.Join(t.TempDir(), "native.c")
	if err := os.WriteFile(filename, []byte("int value = 7;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	translated := service.translateSessionBody(current, mustJSON(map[string]any{"source": adapterSource{Path: filename}}))
	var body struct {
		Source map[string]any `json:"source"`
	}
	if err := json.Unmarshal(translated, &body); err != nil {
		t.Fatal(err)
	}
	id, _ := body.Source["echoSourceId"].(string)
	if id == "" {
		t.Fatal("path-only source has no identity")
	}
	// A browser cannot swap the path, sourceReference, or name after registration.
	response, err := service.Request(context.Background(), "workspace", current.id, "source", ControlRequest{StopGeneration: 7, Arguments: map[string]any{"source": map[string]any{"echoSourceId": id, "path": "/arbitrary", "sourceReference": 999}}})
	if err != nil || !strings.Contains(string(response.Body), "int value = 7") {
		t.Fatalf("registered source: %s, %v", response.Body, err)
	}
	for _, source := range []map[string]any{{"path": filename}, {"echoSourceId": "unknown"}, {"echoSourceId": service.registerSource(&session{id: "other"}, adapterSource{Path: filename})}} {
		if _, err := service.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": source}}); err == nil {
			t.Fatalf("read unregistered source: %v", source)
		}
	}
	if service.registerSource(current, adapterSource{Name: "native.c", Path: "/one/native.c"}) == service.registerSource(current, adapterSource{Name: "native.c", Path: "/two/native.c"}) {
		t.Fatal("same-named source identities collided")
	}
}

func TestVirtualSourceReferenceTakesPrecedenceOverDisk(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 4, 7)
	defer cleanup()
	id := service.registerSource(current, adapterSource{Name: "native.c", Path: "not-a-real-file.c", Reference: 13})
	go func() {
		request := readTestDAPRequest(t, adapter)
		if request.Command != "source" || !strings.Contains(string(request.Arguments), `"sourceReference":13`) {
			t.Errorf("source request: %+v", request)
		}
		writeTestDAPResponse(t, adapter, request, map[string]any{"content": "adapter content"})
	}()
	response, err := service.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": map[string]any{"echoSourceId": id}}})
	if err != nil || !strings.Contains(string(response.Body), "adapter content") {
		t.Fatalf("virtual source: %s, %v", response.Body, err)
	}
}

func TestExternalSourcesMustBeAvailableTextFiles(t *testing.T) {
	service := &Service{workspaces: staticWorkspaceResolver{workspaces.Workspace{ID: "workspace"}}}
	root := t.TempDir()
	for name, content := range map[string][]byte{"binary.c": {'a', 0}, "invalid.c": {0xff}, "large.c": make([]byte, workspacefs.MaxEditableBytes+1)} {
		filename := filepath.Join(root, name)
		if err := os.WriteFile(filename, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.readAdapterSource(context.Background(), "workspace", filename); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	for _, filename := range []string{root, "_cgo_gotypes.go", filepath.Join(root, "missing.c")} {
		if _, err := service.readAdapterSource(context.Background(), "workspace", filename); err == nil {
			t.Errorf("accepted %s", filename)
		}
	}
	if service.fileRefForPath("workspace", "_cgo_gotypes.go") != nil {
		t.Fatal("resolved generated relative source against Echo's cwd")
	}
}

func TestSandboxSourcesNeverFallBackToHostFiles(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "host.c")
	if err := os.WriteFile(filename, []byte("host-only"), 0600); err != nil {
		t.Fatal(err)
	}
	service := &Service{workspaces: staticWorkspaceResolver{workspaces.Workspace{ID: "workspace", Sandbox: workspaces.SandboxConfig{Enabled: true}}}}
	if content, err := service.readAdapterSource(context.Background(), "workspace", filename); err == nil || content != "" {
		t.Fatalf("sandbox read host file: %q %v", content, err)
	}
}

func TestMetadataRevisionDoesNotInvalidateStoppedInspection(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 5, 7)
	defer cleanup()
	go func() {
		request := readTestDAPRequest(t, adapter)
		writeTestDAPResponse(t, adapter, request, map[string]any{"variables": []any{}})
	}()
	if _, err := service.Request(context.Background(), "workspace", current.id, "variables", ControlRequest{ExpectedRevision: 4, StopGeneration: 7}); err != nil {
		t.Fatal(err)
	}
}

func TestLateStackDoesNotChangeReusedFrameLanguage(t *testing.T) {
	service, current, adapter, cleanup := stoppedPipeSession(t, 4, 7)
	defer cleanup()
	go func() {
		request := readTestDAPRequest(t, adapter)
		service.mu.Lock()
		current.stopGeneration++
		current.cFrames = map[int]bool{1: false}
		service.mu.Unlock()
		writeTestDAPResponse(t, adapter, request, map[string]any{"stackFrames": []stepFrame{{ID: 1, Name: "C.old", Line: 1}}})
	}()
	_, err := service.Request(context.Background(), "workspace", current.id, "stackTrace", ControlRequest{StopGeneration: 7})
	if !errors.Is(err, ErrStaleStop) {
		t.Fatalf("late stack error = %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if current.cFrames[1] {
		t.Fatal("late C stack changed the new Go frame's language")
	}
}

func TestDelveCExpressionPreservesLiterals(t *testing.T) {
	for input, expected := range map[string]string{
		"ptr->items[1]": "ptr.items[1]", "ptr -> next->value": "ptr . next.value",
		`ptr->value == "a->b"`: `ptr.value == "a->b"`, "'->'": "'->'", "`->`": "`->`",
		`"escaped\"->string"`: `"escaped\"->string"`, "left - right > 0": "left - right > 0",
	} {
		if actual := delveCExpression(input); actual != expected {
			t.Errorf("%s => %s, want %s", input, actual, expected)
		}
	}
}
