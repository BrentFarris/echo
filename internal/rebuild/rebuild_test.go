package rebuild

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func echoSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/brent/echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsEchoSource(dir) {
		t.Fatal("accepted a legacy Echo tree without the current web frontend")
	}
	if err := os.Mkdir(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIsEchoSourceRequiresExactModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/not-echo\n// github.com/brent/echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsEchoSource(dir) {
		t.Fatal("accepted a comment or different module as Echo source")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/brent/echo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsEchoSource(dir) {
		t.Fatal("did not accept the Echo module")
	}
}

func TestBuildAndPrepareRunsFrontendThenServerAndSanitizesRelaunch(t *testing.T) {
	source := echoSource(t)
	dataDir := t.TempDir()
	type call struct {
		dir  string
		name string
		args []string
	}
	var calls []call
	var launched launchSpec
	coordinator := NewCoordinator()
	coordinator.run = func(_ context.Context, dir string, _ io.Writer, name string, args ...string) error {
		calls = append(calls, call{dir: dir, name: name, args: append([]string(nil), args...)})
		return nil
	}
	coordinator.launch = func(spec launchSpec, gotDataDir string) error {
		launched = spec
		if gotDataDir != dataDir {
			t.Fatalf("launcher data dir = %q, want %q", gotDataDir, dataDir)
		}
		return nil
	}

	result, err := coordinator.BuildAndPrepare(context.Background(), Request{
		SourceDir:  source,
		DataDir:    dataDir,
		ProcessID:  42,
		Arguments:  []string{"-port", "4872", "--reset-auth", "-data=C:\\Echo Data\\echo.json"},
		WorkingDir: source,
	})
	if err != nil {
		t.Fatalf("BuildAndPrepare: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("commands = %d, want 2", len(calls))
	}
	wantNPM := "npm"
	if runtime.GOOS == "windows" {
		wantNPM = "npm.cmd"
	}
	if calls[0].name != wantNPM || calls[0].dir != filepath.Join(source, "web") || strings.Join(calls[0].args, " ") != "run build" {
		t.Fatalf("frontend command = %#v", calls[0])
	}
	if calls[1].name != "go" || calls[1].dir != source || len(calls[1].args) != 4 || calls[1].args[0] != "build" || calls[1].args[1] != "-o" || calls[1].args[3] != "." {
		t.Fatalf("server command = %#v", calls[1])
	}
	if launched.ProcessID != 42 {
		t.Fatalf("launcher pid = %d", launched.ProcessID)
	}
	if strings.Join(launched.Arguments, "|") != "-port|4872|-data=C:\\Echo Data\\echo.json" {
		t.Fatalf("launcher arguments = %#v", launched.Arguments)
	}
	if result.SourcePath != source || result.LogPath != filepath.Join(dataDir, "rebuild-relaunch.log") {
		t.Fatalf("result = %#v", result)
	}
	if _, err := coordinator.BuildAndPrepare(context.Background(), Request{SourceDir: source, DataDir: dataDir}); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second build after relaunch preparation = %v", err)
	}
}

func TestBuildFailureDoesNotPrepareLauncher(t *testing.T) {
	coordinator := NewCoordinator()
	coordinator.run = func(_ context.Context, _ string, _ io.Writer, _ string, _ ...string) error {
		return errors.New("typescript failed")
	}
	launched := false
	coordinator.launch = func(launchSpec, string) error { launched = true; return nil }

	_, err := coordinator.BuildAndPrepare(context.Background(), Request{SourceDir: echoSource(t), DataDir: t.TempDir()})
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || buildErr.Stage != "frontend build" {
		t.Fatalf("error = %v", err)
	}
	if launched {
		t.Fatal("prepared a launcher after a failed build")
	}
}

func TestConcurrentBuildIsRejected(t *testing.T) {
	coordinator := NewCoordinator()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	coordinator.run = func(_ context.Context, _ string, _ io.Writer, _ string, _ ...string) error {
		once.Do(func() { close(started); <-release })
		return nil
	}
	coordinator.launch = func(launchSpec, string) error { return nil }
	request := Request{SourceDir: echoSource(t), DataDir: t.TempDir()}
	done := make(chan error, 1)
	go func() { _, err := coordinator.BuildAndPrepare(context.Background(), request); done <- err }()
	<-started
	if _, err := coordinator.BuildAndPrepare(context.Background(), request); !errors.Is(err, ErrInProgress) {
		t.Fatalf("concurrent error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first build: %v", err)
	}
}

func TestLaunchScriptsUseExactPIDAndPreserveQuotedArguments(t *testing.T) {
	spec := launchSpec{
		ProcessID: 77, StagedPath: `C:\Echo Source\echo.rebuild.exe`, BinaryPath: `C:\Echo Source\echo.exe`,
		Arguments: []string{"-port", "4872", `-data=C:\Echo Data\echo.json`}, WorkingDir: `C:\Echo Source`, LogPath: `C:\Echo Data\rebuild.log`, WaitSeconds: 15,
	}
	powerShell := buildPowerShellLauncher(spec)
	for _, required := range []string{"$echoProcessId = 77", "Get-Process -Id $echoProcessId", "Stop-Process -Id $echoProcessId", `"-data=C:\Echo Data\echo.json"`} {
		if !strings.Contains(powerShell, required) {
			t.Errorf("PowerShell launcher missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(powerShell), "get-process -name") {
		t.Fatal("PowerShell launcher kills processes by name")
	}

	shell := buildShellLauncher(launchSpec{ProcessID: 77, StagedPath: "/tmp/echo build", BinaryPath: "/tmp/echo", Arguments: []string{"-data=/tmp/echo data.json"}, WorkingDir: "/tmp", LogPath: "/tmp/rebuild.log", WaitSeconds: 15})
	for _, required := range []string{`echo_pid=77`, `kill -0 "$echo_pid"`, `kill -9 "$echo_pid"`, `'-data=/tmp/echo data.json'`} {
		if !strings.Contains(shell, required) {
			t.Errorf("shell launcher missing %q", required)
		}
	}
}
