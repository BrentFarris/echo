package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	for _, argument := range os.Args[1:] {
		if strings.HasPrefix(argument, "--echo-plugin-helper=") {
			runPluginHelper(strings.TrimPrefix(argument, "--echo-plugin-helper="))
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func runPluginHelper(mode string) {
	if mode == "crash" {
		os.Exit(17)
	}
	if mode == "malformed" {
		_, _ = fmt.Fprintln(os.Stdout, "this is not JSON-RPC")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "helper-started")
	var outputMu sync.Mutex
	respond := func(id json.RawMessage, result any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), maxRPCMessageBytes)
	for scanner.Scan() {
		var request rpcMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		switch request.Method {
		case "echo.initialize":
			respond(request.ID, map[string]any{"protocol": RPCProtocol})
		case "echo.shutdown":
			respond(request.ID, map[string]any{"ok": true})
			return
		case "tools.echo":
			var params any
			_ = json.Unmarshal(request.Params, &params)
			respond(request.ID, map[string]any{"received": params})
		case "tools.wait":
			// Deliberately no response. The host must time out and send a
			// cancellation notification without wedging the process.
		case "tools.large":
			respond(request.ID, strings.Repeat("x", maxRPCResultBytes+1))
		case "$/cancelRequest":
			_, _ = fmt.Fprintln(os.Stderr, "cancellation-received")
		}
	}
}

func runtimeTestPlugin(t *testing.T, mode string) (InstalledPlugin, RuntimeOptions) {
	t.Helper()
	root := localTestDir(t)
	packageRoot := filepath.Join(root, "package")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	targetKey := runtime.GOOS + "-" + runtime.GOARCH
	name := "helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	relative := filepath.ToSlash(filepath.Join("backend", targetKey, name))
	target := filepath.Join(packageRoot, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		ManifestVersion: 1, ID: "runtime-test", Name: "Runtime Test", Version: "1.0.0", Echo: Compatibility{API: "^1"},
		Runtime: &Runtime{Protocol: RPCProtocol, Targets: map[string]RuntimeTarget{targetKey: {Path: relative, Args: []string{"--echo-plugin-helper=" + mode}}}},
	}
	digest, err := HashPackage(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return InstalledPlugin{Manifest: manifest, Digest: digest, PackagePath: packageRoot}, RuntimeOptions{RootDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
}

func TestRuntimeHandshakeCallTimeoutAndShutdown(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "normal")
	manager := NewRuntimeManager(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Ensure(ctx, installed); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Call(ctx, installed, "tools.echo", map[string]any{"value": "hello"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["received"] == nil {
		t.Fatalf("unexpected RPC result: %#v", result)
	}
	_, err = manager.Call(ctx, installed, "tools.wait", map[string]any{}, 30*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if err := manager.Stop(installed.Manifest.ID); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(filepath.Join(options.LogDir, installed.Manifest.ID+".log"))
	if err != nil || !strings.Contains(string(logData), "helper-started") || !strings.Contains(string(logData), "cancellation-received") {
		t.Fatalf("stderr log missing: %v, %q", err, logData)
	}
}

func TestRuntimeRejectsSnapshotChangedAfterApproval(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "normal")
	target := installed.Manifest.Runtime.Targets[runtime.GOOS+"-"+runtime.GOARCH]
	path := filepath.Join(installed.PackagePath, filepath.FromSlash(target.Path))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manager := NewRuntimeManager(options)
	if _, err := manager.Ensure(context.Background(), installed); err == nil || !strings.Contains(err.Error(), "approved digest") {
		t.Fatalf("expected changed runtime snapshot rejection, got %v", err)
	}
}

func TestRuntimeRejectsOversizedResult(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "normal")
	manager := NewRuntimeManager(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Call(ctx, installed, "tools.large", map[string]any{}, time.Second); err == nil || !strings.Contains(err.Error(), "result exceeds") {
		t.Fatalf("expected oversized result rejection, got %v", err)
	}
	_ = manager.Stop(installed.Manifest.ID)
}

func TestRuntimeRejectsMalformedStdout(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "malformed")
	manager := NewRuntimeManager(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Ensure(ctx, installed); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed RPC error, got %v", err)
	}
}

func TestRepeatedRuntimeCrashesBecomeUnhealthy(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "crash")
	events := []RuntimeEvent{}
	options.Events = func(event RuntimeEvent) { events = append(events, event) }
	manager := NewRuntimeManager(options)
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = manager.Ensure(ctx, installed)
		cancel()
		if attempt < 2 {
			time.Sleep(time.Duration(1<<attempt)*100*time.Millisecond + 30*time.Millisecond)
		}
	}
	if !manager.Unhealthy(installed.Manifest.ID) {
		t.Fatal("repeated crashes did not mark runtime unhealthy")
	}
	found := false
	for _, event := range events {
		found = found || event.Type == "runtime_unhealthy"
	}
	if !found {
		t.Fatalf("unhealthy event was not emitted: %#v", events)
	}
	if _, err := manager.Ensure(context.Background(), installed); err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("unhealthy runtime restarted: %v", err)
	}
	_ = manager.Stop(installed.Manifest.ID)
	if manager.Unhealthy(installed.Manifest.ID) {
		t.Fatal("explicit stop did not clear crash state")
	}
}

func TestRuntimeCrashUsesRestartBackoff(t *testing.T) {
	installed, options := runtimeTestPlugin(t, "crash")
	manager := NewRuntimeManager(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = manager.Ensure(ctx, installed)
	if _, err := manager.Ensure(ctx, installed); err == nil || !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("expected restart backoff, got %v", err)
	}
}

func TestRotatingLogRemainsBounded(t *testing.T) {
	path := filepath.Join(localTestDir(t), "plugin.log")
	log, err := newRotatingLog(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1400<<10)
	for index := range chunk {
		chunk[index] = 'x'
	}
	if _, err := log.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 2<<20 {
		t.Fatalf("active log grew beyond cap: %d", info.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("rotated log was not retained")
	}
}

func TestPluginLogRedactsSecretsAcrossWrites(t *testing.T) {
	path := filepath.Join(localTestDir(t), "plugin.log")
	target, err := newRotatingLog(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := &pluginLogWriter{pluginID: "redaction-test", target: target, redact: func(_ string, value string) string {
		return strings.ReplaceAll(value, "top-secret", "[REDACTED]")
	}}
	_, _ = writer.Write([]byte("value=top-"))
	_, _ = writer.Write([]byte("secret\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("secret was not redacted: %q", data)
	}
}

func TestWorkspaceMetadataRequiresFilesystemPermissionForPaths(t *testing.T) {
	workspace := map[string]any{
		"id": "workspace-1",
		"roots": []map[string]string{{
			"id": "root-1", "label": "Project", "path": `C:\work\project`,
		}},
	}
	installed := InstalledPlugin{ApprovedPermissions: nil}
	filtered := permittedToolWorkspaceMetadata(installed, workspace)
	roots, ok := filtered["roots"].([]map[string]string)
	if !ok || len(roots) != 1 {
		t.Fatalf("unexpected filtered roots: %#v", filtered["roots"])
	}
	if _, disclosed := roots[0]["path"]; disclosed {
		t.Fatalf("workspace path was disclosed without filesystem permission: %#v", roots[0])
	}

	installed.ApprovedPermissions = []string{"filesystem"}
	permitted := permittedToolWorkspaceMetadata(installed, workspace)
	permittedRoots := permitted["roots"].([]map[string]string)
	if permittedRoots[0]["path"] != `C:\work\project` {
		t.Fatalf("workspace path was not disclosed with filesystem permission: %#v", permittedRoots[0])
	}
}

func TestUIWorkspaceMetadataRequiresFilesystemPermissionForPath(t *testing.T) {
	manager := &Manager{workspacePath: func(workspaceID string) (string, error) {
		return `C:\work\project`, nil
	}}
	installed := InstalledPlugin{}
	metadata := manager.uiWorkspaceMetadata(installed, "workspace-1")
	if _, disclosed := metadata["path"]; disclosed {
		t.Fatalf("UI workspace path was disclosed without filesystem permission: %#v", metadata)
	}
	installed.ApprovedPermissions = []string{"filesystem"}
	metadata = manager.uiWorkspaceMetadata(installed, "workspace-1")
	if metadata["path"] != `C:\work\project` {
		t.Fatalf("UI workspace path was not disclosed with filesystem permission: %#v", metadata)
	}
}
