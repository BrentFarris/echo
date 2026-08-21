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

	"github.com/brent/echo/internal/echoupdate"
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

func TestUpdateAndPreparePullsMasterBeforeSharedBuild(t *testing.T) {
	source := echoSource(t)
	type call struct {
		name string
		args []string
	}
	var calls []call
	coordinator := NewCoordinator()
	coordinator.run = func(_ context.Context, _ string, output io.Writer, name string, args ...string) error {
		calls = append(calls, call{name: name, args: append([]string(nil), args...)})
		if name == "git" && args[0] == "branch" {
			_, _ = io.WriteString(output, "master\n")
		}
		return nil
	}
	coordinator.launch = func(launchSpec, string) error { return nil }

	if _, err := coordinator.UpdateAndPrepare(context.Background(), Request{SourceDir: source, DataDir: t.TempDir()}); err != nil {
		t.Fatalf("UpdateAndPrepare: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("commands = %#v", calls)
	}
	if calls[0].name != "git" || strings.Join(calls[0].args, " ") != "branch --show-current" {
		t.Fatalf("branch command = %#v", calls[0])
	}
	wantPull := "pull --ff-only " + echoupdate.RepositoryURL + " " + echoupdate.MasterBranch
	if calls[1].name != "git" || strings.Join(calls[1].args, " ") != wantPull {
		t.Fatalf("pull command = %#v", calls[1])
	}
	if calls[2].args[0] != "run" || calls[3].name != "go" {
		t.Fatalf("build commands = %#v", calls[2:])
	}
}

func TestUpdateRequiresCheckedOutMasterWithoutPullingOrBuilding(t *testing.T) {
	coordinator := NewCoordinator()
	var calls int
	coordinator.run = func(_ context.Context, _ string, output io.Writer, _ string, _ ...string) error {
		calls++
		_, _ = io.WriteString(output, "feature\n")
		return nil
	}
	launched := false
	coordinator.launch = func(launchSpec, string) error { launched = true; return nil }

	_, err := coordinator.UpdateAndPrepare(context.Background(), Request{SourceDir: echoSource(t), DataDir: t.TempDir()})
	if !errors.Is(err, ErrMasterNotCheckedOut) {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 || launched {
		t.Fatalf("calls = %d, launched = %v", calls, launched)
	}
}

func TestUpdatePullFailureDoesNotBuildOrPrepareLauncher(t *testing.T) {
	coordinator := NewCoordinator()
	var calls int
	coordinator.run = func(_ context.Context, _ string, output io.Writer, _ string, args ...string) error {
		calls++
		if args[0] == "branch" {
			_, _ = io.WriteString(output, "master\n")
			return nil
		}
		return errors.New("local edits would be overwritten")
	}
	launched := false
	coordinator.launch = func(launchSpec, string) error { launched = true; return nil }

	_, err := coordinator.UpdateAndPrepare(context.Background(), Request{SourceDir: echoSource(t), DataDir: t.TempDir()})
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || buildErr.Stage != "git pull" {
		t.Fatalf("error = %v", err)
	}
	if calls != 2 || launched {
		t.Fatalf("calls = %d, launched = %v", calls, launched)
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
	if _, err := coordinator.UpdateAndPrepare(context.Background(), request); !errors.Is(err, ErrInProgress) {
		t.Fatalf("concurrent update error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first build: %v", err)
	}
}

func TestLaunchScriptsUseExactPIDAndPreserveQuotedArguments(t *testing.T) {
	spec := launchSpec{
		ProcessID: 77, StagedPath: `C:\Echo Source\echo.rebuild.exe`, BinaryPath: `C:\Echo Source\echo.exe`,
		Arguments: []string{"-port", "4872", `-data=C:\Echo Data\echo.json`}, WorkingDir: `C:\Echo Source`, LogPath: `C:\Echo Data\rebuild.log`, ReadyPath: `C:\Echo Data\rebuild.ready`, WaitSeconds: 15,
	}
	powerShell := buildPowerShellLauncher(spec)
	for _, required := range []string{"$echoProcessId = 77", "Get-Process -Id $echoProcessId", "Stop-Process -Id $echoProcessId", `"-data=C:\Echo Data\echo.json"`, "Set-Content -LiteralPath $readyFile", "-WindowStyle Hidden"} {
		if !strings.Contains(powerShell, required) {
			t.Errorf("PowerShell launcher missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(powerShell), "get-process -name") {
		t.Fatal("PowerShell launcher kills processes by name")
	}

	shell := buildShellLauncher(launchSpec{ProcessID: 77, StagedPath: "/tmp/echo build", BinaryPath: "/tmp/echo", Arguments: []string{"-data=/tmp/echo data.json"}, WorkingDir: "/tmp", LogPath: "/tmp/rebuild.log", ReadyPath: "/tmp/rebuild.ready", WaitSeconds: 15})
	for _, required := range []string{`echo_pid=77`, `kill -0 "$echo_pid"`, `kill -9 "$echo_pid"`, `'-data=/tmp/echo data.json'`, `ready_file='/tmp/rebuild.ready'`} {
		if !strings.Contains(shell, required) {
			t.Errorf("shell launcher missing %q", required)
		}
	}
}
