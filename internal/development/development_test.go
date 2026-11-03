package development

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func commitTest(t *testing.T, dir, name string) {
	t.Helper()
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "-c", "user.name=Echo Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", name)
}

func repositoryTest(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote repo")
	if err := os.MkdirAll(remote, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, remote, "init", "-b", "echo2")
	for _, f := range []string{"main.go", "build.ps1", "build.sh", "web/package.json", "web/package-lock.json", "internal/plugins/guest/main.go", "internal/store/store.go"} {
		writeTest(t, filepath.Join(remote, f), "fixture\n")
	}
	writeTest(t, filepath.Join(remote, "go.mod"), "module github.com/brent/echo\n")
	commitTest(t, remote, "initial")
	local := filepath.Join(t.TempDir(), "local repo")
	gitTest(t, "", "clone", remote, local)
	return remote, local
}

func managerTest(t *testing.T, repo string) *Manager {
	t.Helper()
	m := New(context.Background(), t.TempDir(), Config{RepositoryPath: repo}, func() {})
	t.Cleanup(m.Close)
	return m
}

func TestRepositoryValidationAndWorktree(t *testing.T) {
	_, repo := repositoryTest(t)
	m := managerTest(t, repo)
	ctx := context.Background()
	if err := m.validate(ctx, repo); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative", t.TempDir(), filepath.Join(repo, "web")} {
		if m.validate(ctx, bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	worktree := filepath.Join(t.TempDir(), "work tree")
	gitTest(t, repo, "worktree", "add", "-b", "feature", worktree)
	if err := m.validate(ctx, worktree); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(ctx, Config{RepositoryPath: worktree}, func(c Config) error {
		if c.RepositoryPath != worktree {
			t.Fatal(c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot(ctx).RepositoryPath != worktree {
		t.Fatal("path not saved")
	}
	if err := m.Save(ctx, Config{}, func(Config) error { return errors.New("disk full") }); err == nil {
		t.Fatal("persistence error lost")
	}
	if m.Snapshot(ctx).RepositoryPath != worktree {
		t.Fatal("failed save changed path")
	}
}

func TestTrackingRequiresUpstreamAndBranch(t *testing.T) {
	_, repo := repositoryTest(t)
	m := managerTest(t, repo)
	gitTest(t, repo, "branch", "--unset-upstream")
	_, _, err := m.tracking(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "no upstream") {
		t.Fatal(err)
	}
	gitTest(t, repo, "checkout", "--detach")
	_, _, err = m.tracking(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatal(err)
	}
}

func TestComparisonAndFastForward(t *testing.T) {
	remote, repo := repositoryTest(t)
	m := managerTest(t, repo)
	ctx := context.Background()
	compare := func(want string) Comparison {
		t.Helper()
		c, e := m.compare(ctx, repo, io.Discard)
		if e != nil || c.State != want {
			t.Fatalf("want %s: %+v %v", want, c, e)
		}
		return c
	}
	compare("up-to-date")
	writeTest(t, filepath.Join(remote, "main.go"), "upstream\n")
	commitTest(t, remote, "upstream")
	c := compare("behind")
	original, err := os.ReadFile(filepath.Join(repo, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(repo, "main.go"), "local edits\n")
	if err := m.fastForward(ctx, repo, c, io.Discard); err == nil {
		t.Fatal("overwrote conflicting local edits")
	}
	contents, _ := os.ReadFile(filepath.Join(repo, "main.go"))
	if string(contents) != "local edits\n" {
		t.Fatal("edits changed")
	}
	// Restore the fixture to its committed content, then test non-conflicting edits.
	writeTest(t, filepath.Join(repo, "main.go"), string(original))
	writeTest(t, filepath.Join(repo, "web/package.json"), "uncommitted\n")
	var mergeLog strings.Builder
	if err := m.fastForward(ctx, repo, c, &mergeLog); err != nil {
		t.Fatal(err, mergeLog.String())
	}
	contents, _ = os.ReadFile(filepath.Join(repo, "web/package.json"))
	if string(contents) != "uncommitted\n" {
		t.Fatal("local edits not preserved")
	}
	compare("up-to-date")
	commitTest(t, repo, "local")
	compare("ahead")
	writeTest(t, filepath.Join(remote, "main.go"), "another upstream\n")
	commitTest(t, remote, "remote again")
	compare("diverged")
}

func TestUpdateRejectsChangedBranch(t *testing.T) {
	remote, repo := repositoryTest(t)
	m := managerTest(t, repo)
	ctx := context.Background()
	writeTest(t, filepath.Join(remote, "main.go"), "new\n")
	commitTest(t, remote, "new")
	c, err := m.compare(ctx, repo, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "checkout", "-b", "other")
	if err = m.fastForward(ctx, repo, c, io.Discard); err == nil {
		t.Fatal("updated a different branch")
	}
}

func waitOperation(t *testing.T, m *Manager, id string) Operation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op, _ := m.Operation(id)
		if op.Status != "running" {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation timed out")
	return Operation{}
}

func TestBuildFailureAndSerialization(t *testing.T) {
	_, repo := repositoryTest(t)
	m := managerTest(t, repo)
	started, release := make(chan struct{}), make(chan struct{})
	m.run = func(ctx context.Context, dir string, output io.Writer, name string, args ...string) error {
		if name == "git" {
			return runCommand(ctx, dir, output, name, args...)
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		io.WriteString(output, strings.Repeat("x", tailLimit*2))
		return errors.New("frontend build failed")
	}
	m.launch = func(launchSpec, string) error { t.Error("launched after failure"); return nil }
	op, err := m.Start("build-relaunch")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = m.Start("terminate"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err = m.Save(context.Background(), Config{}, func(Config) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	close(release)
	result := waitOperation(t, m, op.ID)
	if result.Status != "failed" || !strings.Contains(result.Error, "frontend build failed") || len(result.LogTail) > tailLimit {
		t.Fatal(result)
	}
	log, err := os.ReadFile(result.LogPath)
	if err != nil || len(log) <= tailLimit {
		t.Fatal("full log missing", err)
	}
}

func TestUpdatedCheckoutSurvivesBuildFailure(t *testing.T) {
	remote, repo := repositoryTest(t)
	m := managerTest(t, repo)
	writeTest(t, filepath.Join(remote, "main.go"), "updated\n")
	commitTest(t, remote, "update")
	m.run = func(ctx context.Context, dir string, w io.Writer, name string, args ...string) error {
		if name == "git" {
			return runCommand(ctx, dir, w, name, args...)
		}
		return errors.New("build failed")
	}
	op, err := m.Start("update-relaunch")
	if err != nil {
		t.Fatal(err)
	}
	result := waitOperation(t, m, op.ID)
	if !result.CheckoutUpdated || !strings.Contains(result.Error, "checkout was updated") {
		t.Fatal(result)
	}
	if gitTest(t, repo, "rev-parse", "HEAD") != gitTest(t, remote, "rev-parse", "HEAD") {
		t.Fatal("update was rolled back")
	}
}

func TestRelaunchPreservesArgumentsAndRequiresReadiness(t *testing.T) {
	m := managerTest(t, "")
	m.arguments = []string{"-data", `C:\profile space`, "-port=3999", "-web", `C:\assets`}
	m.launch = func(spec launchSpec, dir string) error {
		if spec.BinaryPath != m.executable || spec.StagedPath != m.executable || strings.Join(spec.Arguments, "|") != strings.Join(m.arguments, "|") || spec.WorkingDir != m.workingDir {
			t.Errorf("bad launch: %+v", spec)
		}
		return errors.New("launcher not ready")
	}
	op, err := m.Start("relaunch")
	if err != nil {
		t.Fatal(err)
	}
	result := waitOperation(t, m, op.ID)
	if result.Status != "failed" || !strings.Contains(result.Error, "launcher not ready") {
		t.Fatal(result)
	}
}

func TestTerminationRequestsHostShutdown(t *testing.T) {
	m := managerTest(t, "")
	stopped := make(chan struct{})
	m.stop = func() { close(stopped) }
	op, err := m.Start("terminate")
	if err != nil {
		t.Fatal(err)
	}
	if waitOperation(t, m, op.ID).Status != "stopping" {
		t.Fatal("not stopping")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("no shutdown requested")
	}
}

func TestBuildStagesBeforeLaunchAndCleansFailedPreparation(t *testing.T) {
	for _, failLaunch := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "launcher-failure"}[failLaunch], func(t *testing.T) {
			_, repo := repositoryTest(t)
			m := managerTest(t, repo)
			binaryName, outputFlag := "echo", "--output"
			if runtime.GOOS == "windows" {
				binaryName, outputFlag = "echo.exe", "-OutputPath"
			}
			binary := filepath.Join(repo, binaryName)
			writeTest(t, binary, "previous executable")
			staged := ""
			m.run = func(ctx context.Context, dir string, output io.Writer, name string, args ...string) error {
				if name == "git" {
					return runCommand(ctx, dir, output, name, args...)
				}
				staged = args[len(args)-1]
				if dir != repo || args[len(args)-2] != outputFlag || staged == binary {
					t.Error("build did not stage separately", args)
				}
				return os.WriteFile(staged, []byte("replacement"), 0600)
			}
			m.launch = func(spec launchSpec, dir string) error {
				if spec.BinaryPath != binary || spec.StagedPath != staged {
					t.Error("wrong replacement paths", spec)
				}
				old, _ := os.ReadFile(binary)
				if string(old) != "previous executable" {
					t.Error("replaced before shutdown")
				}
				if spec.LogPath == filepath.Join(dir, "operation.log") {
					t.Error("launcher shares the open operation log")
				}
				if failLaunch {
					return errors.New("not ready")
				}
				return nil
			}
			op, err := m.Start("build-relaunch")
			if err != nil {
				t.Fatal(err)
			}
			result := waitOperation(t, m, op.ID)
			_, statErr := os.Stat(staged)
			if failLaunch {
				if result.Status != "failed" || !os.IsNotExist(statErr) {
					t.Fatal(result, statErr)
				}
			} else if result.Status != "restarting" || statErr != nil {
				t.Fatal(result, statErr)
			}
		})
	}
}

func TestWindowsLauncherQuotesAndWaits(t *testing.T) {
	script := buildPowerShellLauncher(launchSpec{ProcessID: 42, StagedPath: `C:\a'b\staged.exe`, BinaryPath: `C:\a'b\echo.exe`, Arguments: []string{"-data", `C:\space dir\`, `a"b`}, WaitSeconds: 60})
	for _, want := range []string{"C:\\a''b", "WaitForExit", "WindowStyle = 'Hidden'", "RedirectStandardError", "Set-Content -LiteralPath", "$env:ECHO_NO_OPEN = '1'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(script, "Stop-Process") || strings.Contains(script, "-no-open") {
		t.Fatal("unsafe or obsolete launcher command")
	}
	if got := quoteWindowsArgument(`C:\space dir\`); got != `"C:\space dir\\"` {
		t.Fatal(got)
	}
}

func TestUnixLauncherSuppressesWindowOpening(t *testing.T) {
	script := buildShellLauncher(launchSpec{BinaryPath: "/tmp/echo", Arguments: []string{"-no-open=false"}})
	if !strings.Contains(script, "ECHO_NO_OPEN=1 nohup") {
		t.Fatal("development relaunch must suppress opening a window even when startup arguments allow it")
	}
}
