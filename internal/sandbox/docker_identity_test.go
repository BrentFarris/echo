package sandbox

import (
	"bytes"
	"context"
	"io"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspaces"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// Exercise UID mapping independently of the test host: Windows uses 1000,
// whereas GitHub's Linux runners use 1001 (pwuser in the Playwright base).
func TestDockerIntegrationRuntimeUID(t *testing.T) {
	if os.Getenv("ECHO_SANDBOX_INTEGRATION") != "1" {
		t.Skip("enable Docker integration to test runtime UID mapping")
	}
	image := os.Getenv("ECHO_SANDBOX_RUNTIME_IMAGE")
	if image == "" {
		image = "echo-sandbox-runtime:dev"
	}
	engine, err := NewDockerEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, uid := range []string{"1000", "1001"} {
		t.Run(uid, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			spec := WorkspaceSpec{Config: workspaces.SandboxConfig{CPULimit: 4, MemoryMiB: 6144}}
			state := DefaultMachineState("echo-ci", "uid-test", ImageSet{Runtime: image})
			config, host, _, err := dockerContainerConfig("runtime", image, spec, state, netip.MustParseAddr("172.30.0.2"), nil)
			if err != nil {
				t.Fatal(err)
			}
			for index, value := range config.Env {
				if strings.HasPrefix(value, "ECHO_SANDBOX_UID=") {
					config.Env[index] = "ECHO_SANDBOX_UID=" + uid
				}
			}
			// Keep the production security/session configuration, but use an
			// isolated disposable container without host files or external access.
			config.WorkingDir = "/"
			host.NetworkMode, host.DNS, host.Mounts = "none", nil, nil
			created, err := engine.client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: config, HostConfig: host})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if t.Failed() {
					logs, err := engine.client.ContainerLogs(cleanup, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "100"})
					if err == nil {
						var data bytes.Buffer
						_, _ = stdcopy.StdCopy(&data, &data, io.LimitReader(logs, 64<<10))
						_ = logs.Close()
						t.Logf("runtime logs:\n%s", data.String())
					}
				}
				if _, err := engine.client.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
					t.Errorf("runtime cleanup: %v", err)
				}
			}()
			if _, err := engine.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			secrets := RuntimeSecrets{RuntimeAgentToken: strings.Repeat("a", 64), BrowserToken: strings.Repeat("b", 64), VNCToken: strings.Repeat("v", 24)}
			if err := engine.writeFilesWithExec(ctx, created.ID, runtimeSecretFiles(secrets, nil)["runtime"]); err != nil {
				t.Fatal(err)
			}
			// Both health endpoints require a live desktop and managed Chromium.
			// Then execute through the agent to verify the remapped user/session.
			probe := `set -eu
agent_token="$(cat /run/echo/agent.token)"
browser_token="$(cat /run/echo/lease.token)"
trap 'if [ "$?" != 0 ]; then for service in agent browser; do echo "$service health:"; cat /tmp/$service.health /tmp/$service.error 2>/dev/null || true; done; fi' EXIT
check_health() {
  curl --fail-with-body -sS --max-time 2 -H "Authorization: Bearer $2" "http://127.0.0.1:$3/v1/health" >/tmp/$1.health 2>/tmp/$1.error
}
until check_health agent "$agent_token" 7777 && check_health browser "$browser_token" 3000; do
  if jq -e '.error | select(type == "string" and length > 0)' /tmp/browser.health >/dev/null 2>&1; then exit 1; fi
  sleep .2
done
test "$(id -u echo)" = "$ECHO_SANDBOX_UID"
test "$(stat -c %u /home/echo)" = "$ECHO_SANDBOX_UID"
# Crashpad uses the browser's default config directory even with a custom
# profile. Check actual writes so this also catches the bug on Docker Desktop.
gosu echo mkdir -p /home/echo/.config/echo-uid-probe/Crashpad
gosu echo touch /home/echo/.config/echo-uid-probe/Crashpad/write-test
request=$(jq -n --arg uid "$ECHO_SANDBOX_UID" '{command:["/bin/bash","-ec","test $(id -u) = $1; test \"$HOME\" = /home/echo; test \"$DISPLAY\" = :1; test \"$XDG_RUNTIME_DIR\" = /run/user/$1; test -S \"$XDG_RUNTIME_DIR/bus\"; test -w \"$HOME/.config/chromium\"", "uid-test", $uid]}')
curl -fsS --max-time 5 -H "Authorization: Bearer $agent_token" -H 'Content-Type: application/json' -d "$request" http://127.0.0.1:7777/v1/exec | tee /tmp/uid-result.json
jq -e '.exitCode == 0' /tmp/uid-result.json`
			execution, err := engine.client.ExecCreate(ctx, created.ID, client.ExecCreateOptions{
				AttachStdout: true, AttachStderr: true,
				Cmd: []string{"timeout", "40s", "/bin/bash", "-ec", probe},
			})
			if err != nil {
				t.Fatal(err)
			}
			attached, err := engine.client.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			_, readErr := stdcopy.StdCopy(&output, &output, io.LimitReader(attached.Reader, 64<<10))
			attached.Close()
			result, err := engine.client.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
			if readErr != nil || err != nil || result.Running || result.ExitCode != 0 {
				t.Fatalf("UID %s startup/session probe: result=%+v readErr=%v err=%v output=%s", uid, result, readErr, err, output.String())
			}
		})
	}
}
