package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspaces"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/types/network"
)

func TestChromiumSeccompProfileExtendsDefaultWithOnlyNamespaceCalls(t *testing.T) {
	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names   []string `json:"names"`
			Action  string   `json:"action"`
			Comment string   `json:"comment"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(chromiumSeccompJSON), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Fatalf("default seccomp action = %q", profile.DefaultAction)
	}
	found := false
	for _, rule := range profile.Syscalls {
		if rule.Comment == "Chromium sandbox: allow creation and entry of user namespaces" {
			found = true
			if rule.Action != "SCMP_ACT_ALLOW" || len(rule.Names) != 3 || rule.Names[0] != "clone" || rule.Names[1] != "setns" || rule.Names[2] != "unshare" {
				t.Fatalf("unexpected Chromium namespace rule: %+v", rule)
			}
		}
	}
	if !found {
		t.Fatal("Chromium namespace seccomp rule is missing")
	}
}

func TestImagePullErrorIdentifiesReferenceAndAction(t *testing.T) {
	reference := "ghcr.io/brentfarris/echo-sandbox-egress:protocol-1"
	tests := []struct {
		cause error
		want  string
	}{
		{errors.New("pull access denied"), "published publicly"},
		{errors.New("manifest unknown"), "image or tag is not published"},
		{context.DeadlineExceeded, "registry request timed out"},
		{errors.New("proxyconnect tcp: connection refused"), "registry and proxy connectivity"},
	}
	for _, test := range tests {
		err := imagePullError("gateway", reference, test.cause)
		if ErrorCode(err) != "image_pull_failed" || !strings.Contains(err.Error(), reference) || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("imagePullError(%v) = %q", test.cause, err)
		}
		if !errors.Is(err, test.cause) {
			t.Fatalf("imagePullError(%v) did not retain its cause", test.cause)
		}
	}
}

func TestRuntimeSecretsAreRoleSeparatedAndRootOnly(t *testing.T) {
	secrets := RuntimeSecrets{
		WorkbenchAgentToken: "workbench", DesktopAgentToken: "desktop", BrowserToken: "browser",
		ProxyToken: "proxy", VNCToken: "vnc",
	}
	files := runtimeSecretFiles(secrets, nil)
	if len(files["workbench"]) != 1 || string(files["workbench"]["agent.token"].data) != "workbench" {
		t.Fatalf("workbench received unrelated management credentials: %+v", files["workbench"])
	}
	if string(files["desktop"]["agent.token"].data) != "desktop" || string(files["desktop"]["lease.token"].data) != "browser" {
		t.Fatalf("desktop credentials are not role-separated: %+v", files["desktop"])
	}
	for role, roleFiles := range files {
		for name, file := range roleFiles {
			if file.mode != 0o400 {
				t.Fatalf("%s/%s mode = %o", role, name, file.mode)
			}
		}
	}
}

func TestContainerPoliciesDoNotExposeHostOrDangerousPrivileges(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := DefaultMachineState("install", "workspace", BuildImages())
	spec := WorkspaceSpec{
		ID: "workspace", Installation: "install", Config: workspaces.SandboxConfig{CPULimit: 4, MemoryMiB: 6144},
		Roots: []RootMount{{ID: "main", HostPath: root, GuestPath: "/workspace/main", Main: true}},
	}
	gateway := netip.MustParseAddr("172.28.0.2")
	_, workbench, _, err := dockerContainerConfig("workbench", WorkbenchImage, spec, state, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if workbench.Privileged || !slices.Contains(workbench.CapDrop, "ALL") || len(workbench.Devices) != 0 {
		t.Fatalf("workbench has dangerous host privileges: %+v", workbench)
	}
	for _, forbidden := range []string{"NET_ADMIN", "NET_RAW", "SYS_ADMIN", "SYS_PTRACE", "MKNOD"} {
		if slices.Contains(workbench.CapAdd, forbidden) {
			t.Fatalf("workbench capability %s must stay dropped", forbidden)
		}
	}
	for _, item := range workbench.Mounts {
		if strings.Contains(strings.ToLower(item.Source), "docker.sock") {
			t.Fatal("Docker socket was mounted")
		}
		if item.Target == "/workspace/main/.echo" && !item.ReadOnly {
			t.Fatal(".echo mask is writable")
		}
	}

	_, desktop, _, err := dockerContainerConfig("desktop", DesktopImage, spec, state, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if desktop.Privileged || desktop.ShmSize != 1<<30 || !slices.Contains(desktop.CapDrop, "ALL") {
		t.Fatalf("desktop policy is incomplete: %+v", desktop)
	}
	if !slices.Contains(desktop.CapAdd, "SYS_CHROOT") {
		t.Fatal("desktop is missing Chromium's required SYS_CHROOT capability")
	}
	for _, forbidden := range []string{"NET_ADMIN", "NET_RAW", "SYS_ADMIN", "SYS_PTRACE", "MKNOD"} {
		if slices.Contains(desktop.CapAdd, forbidden) {
			t.Fatalf("desktop capability %s must stay dropped", forbidden)
		}
	}
	if len(desktop.SecurityOpt) != 2 || desktop.SecurityOpt[0] != "no-new-privileges=true" || !strings.HasPrefix(desktop.SecurityOpt[1], "seccomp=") {
		t.Fatalf("desktop seccomp/no-new-privileges policy is missing: %v", desktop.SecurityOpt)
	}
	if len(workbench.PortBindings) != 0 || len(desktop.PortBindings) != 0 {
		t.Fatal("internal-only workbench or desktop unexpectedly publishes a host port")
	}
	desktopMounts := map[string]string{}
	for _, item := range desktop.Mounts {
		desktopMounts[item.Target] = item.Source
	}
	if desktopMounts["/home/echo"] != state.VolumeNames["desktop"] || desktopMounts["/home/echo/.config/chromium"] != state.VolumeNames["browser"] {
		t.Fatalf("desktop home and browser profile are not independently persistent: %+v", desktopMounts)
	}

	_, gatewayHost, _, err := dockerContainerConfig("gateway", GatewayImage, spec, state, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{workbenchAgentForwardPort, desktopAgentForwardPort, desktopVNCForwardPort, desktopBrowserForwardPort} {
		port, err := network.ParsePort(value)
		if err != nil || len(gatewayHost.PortBindings[port]) != 1 || gatewayHost.PortBindings[port][0].HostIP.String() != "127.0.0.1" {
			t.Fatalf("gateway management port %s is not loopback-only: %v", value, gatewayHost.PortBindings[port])
		}
	}
}

func TestForwardedManagementPortsAreRoleScoped(t *testing.T) {
	tests := []struct{ role, service, forward string }{
		{"workbench", agentPort, workbenchAgentForwardPort},
		{"desktop", agentPort, desktopAgentForwardPort},
		{"desktop", vncPort, desktopVNCForwardPort},
		{"desktop", playwrightPort, desktopBrowserForwardPort},
	}
	for _, test := range tests {
		if got, ok := forwardedManagementPort(test.role, test.service); !ok || got != test.forward {
			t.Fatalf("forwardedManagementPort(%q, %q) = %q, %v", test.role, test.service, got, ok)
		}
	}
	if _, ok := forwardedManagementPort("workbench", vncPort); ok {
		t.Fatal("workbench unexpectedly received the desktop VNC forward")
	}
}

func TestLogicalResourceLimitIsSplitAcrossRoles(t *testing.T) {
	config := workspaces.SandboxConfig{CPULimit: 4, MemoryMiB: 6144}
	workbench := roleResources("workbench", config)
	desktop := roleResources("desktop", config)
	gateway := roleResources("gateway", config)
	if got, want := workbench.NanoCPUs+desktop.NanoCPUs+gateway.NanoCPUs, int64(4_000_000_000); got != want {
		t.Fatalf("CPU shares total %d, want %d", got, want)
	}
	if got, want := workbench.Memory+desktop.Memory+gateway.Memory, int64(6144<<20); got != want {
		t.Fatalf("memory shares total %d, want %d", got, want)
	}
	if workbench.NanoCPUs <= desktop.NanoCPUs || workbench.Memory <= desktop.Memory {
		t.Fatal("workbench did not receive the larger build-oriented share")
	}
}

func TestInternalGatewayAddressUsesFirstUnallocatedContainerAddress(t *testing.T) {
	address, err := internalGatewayAddress([]network.IPAMConfig{{Subnet: netip.MustParsePrefix("172.28.0.0/16"), Gateway: netip.MustParseAddr("172.28.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	if address.String() != "172.28.0.2" {
		t.Fatalf("gateway address = %s", address)
	}
	if _, err := internalGatewayAddress([]network.IPAMConfig{{Subnet: netip.MustParsePrefix("fd00::/64")}}); err == nil {
		t.Fatal("IPv6-only network unexpectedly accepted")
	}
}

func TestSandboxRoleAddressesAreStableAndDistinct(t *testing.T) {
	gateway := netip.MustParseAddr("10.72.4.2")
	want := map[string]string{"gateway": "10.72.4.2", "workbench": "10.72.4.3", "desktop": "10.72.4.4"}
	seen := map[netip.Addr]bool{}
	for role, expected := range want {
		address, err := sandboxRoleAddress(gateway, role)
		if err != nil || address.String() != expected || seen[address] {
			t.Fatalf("sandboxRoleAddress(%q) = %s, %v", role, address, err)
		}
		seen[address] = true
	}
	if _, err := sandboxRoleAddress(gateway, "unknown"); err == nil {
		t.Fatal("unknown role unexpectedly received an address")
	}
}

func TestAgentProcessProtocolDemultiplexesStdio(t *testing.T) {
	t.Helper()
	received := make(chan string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var start map[string]any
		if connection.ReadJSON(&start) != nil {
			return
		}
		_ = connection.WriteJSON(map[string]any{"type": "started"})
		messageType, input, err := connection.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage || len(input) == 0 || input[0] != 0 {
			return
		}
		received <- string(input[1:])
		_, _, _ = connection.ReadMessage() // close_stdin
		_ = connection.WriteMessage(websocket.BinaryMessage, append([]byte{1}, []byte("stdout")...))
		_ = connection.WriteMessage(websocket.BinaryMessage, append([]byte{2}, []byte("stderr")...))
		_ = connection.WriteJSON(map[string]any{"type": "exit", "exitCode": 7})
	}))
	defer server.Close()

	connection, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"command": []string{"fake"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := newAgentProcess(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	var stdout, stderr []byte
	wait.Add(2)
	go func() { defer wait.Done(); stdout, _ = io.ReadAll(process.Stdout()) }()
	go func() { defer wait.Done(); stderr, _ = io.ReadAll(process.Stderr()) }()
	if _, err := process.Stdin().Write([]byte("input")); err != nil {
		t.Fatal(err)
	}
	if err := process.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	code, err := process.Wait()
	wait.Wait()
	if err != nil || code != 7 || string(stdout) != "stdout" || string(stderr) != "stderr" {
		t.Fatalf("process result code=%d err=%v stdout=%q stderr=%q", code, err, stdout, stderr)
	}
	select {
	case input := <-received:
		if input != "input" {
			t.Fatalf("stdin = %q", input)
		}
	case <-ctx.Done():
		t.Fatal("agent did not receive stdin")
	}
}

func TestAgentPTYProtocolSupportsOutputInputResizeAndExit(t *testing.T) {
	received := make(chan map[string]any, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteMessage(websocket.BinaryMessage, []byte("terminal output"))
		for index := 0; index < 2; index++ {
			messageType, data, readErr := connection.ReadMessage()
			if readErr != nil {
				return
			}
			if messageType == websocket.BinaryMessage {
				received <- map[string]any{"input": string(data)}
				continue
			}
			var control map[string]any
			_ = json.Unmarshal(data, &control)
			received <- control
		}
		_ = connection.WriteJSON(map[string]any{"type": "exit", "exitCode": 0})
	}))
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	terminal := newAgentPTY(connection)
	if err := terminal.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(terminal)
	if err != nil || string(output) != "terminal output" {
		t.Fatalf("terminal output=%q err=%v", output, err)
	}
	code, err := terminal.Wait()
	if err != nil || code != 0 {
		t.Fatalf("terminal exit code=%d err=%v", code, err)
	}
	first, second := <-received, <-received
	values := []map[string]any{first, second}
	foundResize, foundInput := false, false
	for _, value := range values {
		foundResize = foundResize || value["type"] == "resize" && value["cols"] == float64(120) && value["rows"] == float64(40)
		foundInput = foundInput || value["input"] == "typed"
	}
	if !foundResize || !foundInput {
		t.Fatalf("terminal controls = %+v", values)
	}
}
