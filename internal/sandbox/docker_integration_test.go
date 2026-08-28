package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// TestDockerIntegrationLifecycle is opt-in because it creates and deletes real
// Docker resources. CI builds the three :dev images first and enables it on a
// Linux runner and on the release-blocking Windows Docker Desktop runner.
func TestDockerIntegrationLifecycle(t *testing.T) {
	if os.Getenv("ECHO_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set ECHO_SANDBOX_INTEGRATION=1 after building the sandbox images")
	}
	engine, err := NewDockerEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	host := engine.Host(ctx, BuildImages())
	if !host.Available || !host.Supported {
		t.Fatalf("Docker host is not a supported linux/amd64 engine: %+v", host)
	}
	for role, image := range host.Images {
		if !image.Present {
			t.Fatalf("%s image %q is not present", role, image.Reference)
		}
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".echo", "sandbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".echo", "workspace.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceID := "integration-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	spec := WorkspaceSpec{
		ID: workspaceID, Installation: "echo-ci", SetupPath: filepath.Join(root, filepath.FromSlash(SetupRecipePath)),
		Config: workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 30},
		Roots:  []RootMount{{ID: "root-main", HostPath: root, GuestPath: "/workspace/root-main", Main: true}},
	}
	state := DefaultMachineState(spec.Installation, spec.ID, BuildImages())
	secrets := RuntimeSecrets{
		WorkbenchAgentToken: strings.Repeat("w", 64), DesktopAgentToken: strings.Repeat("d", 64),
		VNCToken: strings.Repeat("v", 24), ProxyToken: strings.Repeat("p", 64), BrowserToken: strings.Repeat("b", 64),
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		_ = engine.Stop(cleanup, state)
		if err := engine.Delete(cleanup, state, DeleteScope{Containers: true, Network: true, Workbench: true, Desktop: true, Browser: true, Exchange: true}); err != nil {
			t.Errorf("sandbox cleanup: %v", err)
		}
	}()

	if err := engine.ProbeWorkspace(ctx, spec); err != nil {
		t.Fatal(err)
	}
	state, err = engine.Ensure(ctx, spec, state, secrets)
	if err != nil {
		t.Fatalf("%v (daemon cause: %v)", err, errors.Unwrap(err))
	}
	if err := engine.Start(ctx, state); err != nil {
		logDockerIntegrationState(t, engine, state)
		t.Fatalf("%v (daemon cause: %v)", err, errors.Unwrap(err))
	}

	result, err := engine.Exec(ctx, state, ExecRequest{
		Role: "workbench", WorkingDirectory: "/workspace/root-main", OutputLimit: 64 << 10,
		Command: []string{"/bin/bash", "-lc", "set -eu; test \"$(uname -s)\" = Linux; test \"$(id -u)\" = 1000; test \"$HTTP_PROXY\" = http://gateway:3128; test ! -w .echo; test ! -e /var/run/docker.sock; sudo -n true; printf guest-write > integration-write.txt; printf '%s' \"$(uname -s)\""},
	})
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != "Linux" {
		t.Fatalf("workbench execution failed: exit=%d stdout=%q stderr=%q err=%v", result.ExitCode, result.Stdout, result.Stderr, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "integration-write.txt")); err != nil || string(content) != "guest-write" {
		t.Fatalf("guest write was not reflected in canonical host files: %q, %v", content, err)
	}
	if _, _, err := engine.serviceRequest(ctx, state, "workbench", agentPort, "GET", "/v1/health", "wrong-token", nil, 64<<10); err == nil {
		t.Fatal("workbench agent accepted an invalid management token")
	}
	desktopStream, err := engine.OpenDesktop(ctx, state)
	if err != nil {
		t.Fatalf("desktop VNC relay failed: %v", err)
	}
	if deadlineWriter, ok := desktopStream.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = deadlineWriter.SetReadDeadline(time.Now().Add(5 * time.Second))
	}
	banner := make([]byte, 12)
	_, readErr := io.ReadFull(desktopStream, banner)
	_ = desktopStream.Close()
	if readErr != nil || !strings.HasPrefix(string(banner), "RFB ") {
		t.Fatalf("desktop VNC relay returned %q: %v", banner, readErr)
	}

	process, err := engine.OpenProcess(ctx, state, ExecRequest{
		Role: "workbench", WorkingDirectory: "/workspace/root-main",
		Command: []string{"/bin/bash", "-lc", "IFS= read -r line; printf 'out:%s' \"$line\"; printf 'err:%s' \"$line\" >&2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdoutDone, stderrDone := make(chan []byte, 1), make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(process.Stdout()); stdoutDone <- data }()
	go func() { data, _ := io.ReadAll(process.Stderr()); stderrDone <- data }()
	_, _ = process.Stdin().Write([]byte("streamed\n"))
	_ = process.Stdin().Close()
	if code, waitErr := process.Wait(); waitErr != nil || code != 0 || string(<-stdoutDone) != "out:streamed" || string(<-stderrDone) != "err:streamed" {
		t.Fatalf("authenticated stdio process failed: code=%d err=%v", code, waitErr)
	}

	terminal, err := engine.OpenPTY(ctx, state, ExecRequest{
		WorkingDirectory: "/workspace/root-main", Columns: 80, Rows: 24,
		Command: []string{"/bin/bash", "-lc", "printf pty-linux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalOutput, readErr := io.ReadAll(terminal)
	terminalCode, terminalErr := terminal.Wait()
	if readErr != nil || terminalErr != nil || terminalCode != 0 || !strings.Contains(string(terminalOutput), "pty-linux") {
		t.Fatalf("authenticated PTY failed: code=%d output=%q read=%v wait=%v", terminalCode, terminalOutput, readErr, terminalErr)
	}

	cancelContext, cancelCommand := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancelCommand()
	started := time.Now()
	_, cancelErr := engine.Exec(cancelContext, state, ExecRequest{Role: "workbench", Command: []string{"/bin/bash", "-lc", "sleep 30 & wait"}})
	if cancelErr == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("canceled process tree did not terminate promptly: elapsed=%s err=%v", time.Since(started), cancelErr)
	}

	blocked, err := engine.Exec(ctx, state, ExecRequest{
		Role: "workbench", WorkingDirectory: "/workspace/root-main", OutputLimit: 64 << 10,
		Command: []string{"/bin/bash", "-lc", "if curl -fsS --max-time 5 http://169.254.169.254/latest/meta-data/ >/dev/null 2>&1; then exit 91; fi"},
	})
	if err != nil || blocked.ExitCode != 0 {
		t.Fatalf("metadata endpoint was not blocked: exit=%d stderr=%q err=%v", blocked.ExitCode, blocked.Stderr, err)
	}

	snapshot, err := engine.BrowserCall(ctx, state, "snapshot", json.RawMessage(`{"screenshot":true}`))
	if err != nil || !strings.Contains(string(snapshot), `"tabId"`) || !strings.Contains(string(snapshot), `"screenshot"`) {
		t.Fatalf("headed browser bridge is unavailable: %s, %v", snapshot, err)
	}
	image, mediaType, err := engine.DesktopScreenshot(ctx, state)
	if err != nil || len(image) == 0 || (mediaType != "image/png" && mediaType != "image/jpeg") {
		t.Fatalf("desktop screenshot failed: bytes=%d type=%q err=%v", len(image), mediaType, err)
	}
}

func logDockerIntegrationState(t *testing.T, engine *DockerEngine, state MachineState) {
	t.Helper()
	for _, role := range []string{"gateway", "workbench", "desktop"} {
		name := state.ContainerNames[role]
		inspect, err := engine.client.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
		if err != nil {
			t.Logf("%s inspect: %v", role, err)
			continue
		}
		t.Logf("%s state=%s exit=%d exposed=%v bindings=%v ports=%v", role, inspect.Container.State.Status, inspect.Container.State.ExitCode, inspect.Container.Config.ExposedPorts, inspect.Container.HostConfig.PortBindings, inspect.Container.NetworkSettings.Ports)
		if processes, topErr := engine.client.ContainerTop(context.Background(), name, client.ContainerTopOptions{Arguments: []string{"-eo", "pid,user,args"}}); topErr == nil {
			t.Logf("%s processes: %v %v", role, processes.Titles, processes.Processes)
		} else {
			t.Logf("%s process list: %v", role, topErr)
		}
		logs, err := engine.client.ContainerLogs(context.Background(), name, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "100"})
		if err != nil {
			t.Logf("%s logs: %v", role, err)
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(logs, 64<<10))
		_ = logs.Close()
		if len(data) > 0 {
			t.Logf("%s logs:\n%s", role, data)
		}
	}
}
