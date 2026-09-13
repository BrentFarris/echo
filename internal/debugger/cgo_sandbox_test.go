package debugger

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

// Explicit opt-in: creates an isolated sandbox and removes only its own Docker
// resources. Delve is built on the host, so this needs no sandbox network grant.
func TestDockerIntegrationCGOStepping(t *testing.T) {
	if os.Getenv("ECHO_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set ECHO_SANDBOX_INTEGRATION=1 after building sandbox images")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	for _, name := range []string{"main.go", "native.c", "native.h"} {
		content, err := os.ReadFile(filepath.Join("testdata", "cgo", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module cgo-fixture\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".echo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".echo", "workspace.json"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	module := exec.CommandContext(ctx, "go", "mod", "download", "-json", "github.com/go-delve/delve@v1.27.1")
	output, err := module.Output()
	if err != nil {
		t.Fatalf("prepare pinned Delve sources: %v", err)
	}
	var location struct{ Dir string }
	if err := json.Unmarshal(output, &location); err != nil || location.Dir == "" {
		t.Fatalf("Delve source location: %s, %v", output, err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(root, "dlv-linux"), ".")
	build.Dir = filepath.Join(location.Dir, "cmd", "dlv")
	build.Env = mergeEnvironment(os.Environ(), map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Linux Delve: %s, %v", output, err)
	}

	previousImages := sandbox.BuildImages()
	sandbox.RuntimeImage = firstNonEmpty(os.Getenv("ECHO_SANDBOX_RUNTIME_IMAGE"), "echo-sandbox-runtime:dev")
	sandbox.GatewayImage = firstNonEmpty(os.Getenv("ECHO_SANDBOX_GATEWAY_IMAGE"), "echo-sandbox-egress:dev")
	t.Cleanup(func() { sandbox.RuntimeImage = previousImages.Runtime; sandbox.GatewayImage = previousImages.Gateway })
	engine, err := sandbox.NewDockerEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	workspace := workspaces.Workspace{
		ID: "cgo-" + uuid.NewString(), MainPath: root, Folders: []string{root},
		Sandbox: workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 4096, IdleTimeoutMinutes: 30},
		Debug:   debugconfig.WorkspaceConfig{Version: 1, EnabledAdapterProfileIDs: []string{"delve"}},
	}
	resolver := staticWorkspaceResolver{workspace}
	manager := sandbox.NewManager(resolver, filepath.Join(t.TempDir(), "state"), "echo-cgo-tests", engine)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := manager.Delete(cleanup, workspace.ID); err != nil {
			t.Errorf("sandbox cleanup: %v", err)
		}
		_ = manager.Shutdown(cleanup)
	})
	guestRoot, err := manager.HostToGuest(workspace.ID, root)
	if err != nil {
		t.Fatal(err)
	}
	// Copy the fixture to a guest-only path. Its source cannot accidentally be
	// read from the host mount, exercising the authenticated source-read path.
	result, err := manager.Execute(ctx, workspace.ID, sandbox.ExecRequest{
		Role: "runtime", WorkingDirectory: guestRoot, OutputLimit: 64 << 10,
		Command: []string{"sh", "-c", `set -eu; mkdir -p /home/echo/echo-cgo-fixture; cp main.go native.c native.h go.mod /home/echo/echo-cgo-fixture/; cd /home/echo/echo-cgo-fixture; CGO_ENABLED=1 CGO_CFLAGS='-O0 -g' go build -gcflags='all=-N -l' -o /home/echo/echo-cgo-fixture/probe .; file /home/echo/echo-cgo-fixture/probe; ls -l /home/echo/echo-cgo-fixture/probe; go env GOOS GOARCH`},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("build sandbox fixture: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	t.Logf("sandbox fixture: %s", result.Stdout)

	data := appdata.NewStore(filepath.Join(t.TempDir(), "echo.json"))
	profile, _ := debugconfig.TemplateByID("delve")
	profile.Command = guestRoot + "/dlv-linux"
	if os.Getenv("ECHO_CGO_TRACE") == "1" {
		profile.Args = append(profile.Args, "--log", "--log-output=dap")
	}
	profiles := debugconfig.NewProfileStore(data)
	if err := profiles.Save([]debugconfig.AdapterProfile{profile}); err != nil {
		t.Fatal(err)
	}
	service := New(profiles, debugconfig.NewStateStore(data), resolver, nil)
	service.SetSandbox(manager)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	exerciseCGOStepping(t, ctx, service, workspace, "/home/echo/echo-cgo-fixture/probe")
}
