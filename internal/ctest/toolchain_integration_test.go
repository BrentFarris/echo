package ctest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

const coverageFixtureSource = `
static int classify(int value) {
    if (value > 0) return 1;
    if (value == 0) return 0;
    return -1;
}

static int branchy(int value) {
    if (value) { value++; } else { value--; }
    return value;
}

int main(void) {
    return classify(1) == 1 && branchy(1) == 2 ? 0 : 1;
}
`

func TestInstalledGcovCoverageWorkflow(t *testing.T) {
	requireGcovToolchain(t)
	service, workspace, root, source, build := newCoverageFixture(t)
	executable := filepath.Join(build, executableName("gcov-suite"))
	runIntegrationCommand(t, build, nil, "gcc", "--coverage", "-O0", source, "-o", executable)
	runIntegrationCommand(t, build, nil, executable)
	target := resolvedTarget{
		config:      gotestconfig.CTarget{ID: "gcov", Coverage: gotestconfig.CCoverage{Provider: "gcov"}},
		sourceRoots: []string{root}, objectRoots: []string{build}, cwd: build,
		workspace: workspace,
	}
	files, err := service.loadGcovCoverage(workspace.ID, filepath.Join(root, ".echo", "gcov-report"), target)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverageStates(t, files)
}

func TestInstalledLLVMCoverageWorkflow(t *testing.T) {
	if version := coverageToolOutput(t, "clang", "--version"); !strings.Contains(version, "clang version") {
		unavailableCoverageToolchain(t, "clang does not identify itself as a Clang compiler")
	}
	if help := coverageToolOutput(t, "llvm-profdata", "--help"); !strings.Contains(help, "merge") {
		unavailableCoverageToolchain(t, "llvm-profdata does not support profile merging")
	}
	if help := coverageToolOutput(t, "llvm-cov", "--help"); !strings.Contains(help, "export") {
		unavailableCoverageToolchain(t, "llvm-cov does not support coverage export")
	}
	service, workspace, root, source, build := newCoverageFixture(t)
	executable := filepath.Join(build, executableName("llvm-suite"))
	runIntegrationCommand(t, build, nil, "clang", "-fprofile-instr-generate", "-fcoverage-mapping", "-O0", source, "-o", executable)
	scratch := filepath.Join(root, ".echo", "llvm-report")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	runIntegrationCommand(t, build, map[string]string{"LLVM_PROFILE_FILE": filepath.Join(scratch, "profile-%p.profraw")}, executable)
	rawProfiles, err := discoverFiles([]string{scratch}, ".profraw")
	if err != nil || len(rawProfiles) != 1 {
		t.Fatalf("raw profiles = %#v, %v", rawProfiles, err)
	}
	rawCopy, err := os.ReadFile(rawProfiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "second-process.profraw"), rawCopy, 0o600); err != nil {
		t.Fatal(err)
	}
	target := resolvedTarget{
		config:      gotestconfig.CTarget{ID: "llvm", Coverage: gotestconfig.CCoverage{Provider: "llvm"}},
		sourceRoots: []string{root}, executable: executable, runtimeExecutable: executable, cwd: build,
		workspace: workspace,
	}
	files, err := service.loadLLVMCoverage(workspace.ID, scratch, target)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverageStates(t, files)
}

func TestInstalledGcovServiceBuildRunAndCoveragePipeline(t *testing.T) {
	requireGcovToolchain(t)
	root := t.TempDir()
	settingsPath := filepath.Join(t.TempDir(), "echo.json")
	data := appdata.NewStore(settingsPath)
	manager := workspaces.NewManagerWithData(data)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "C pipeline", MainPath: root})
	if err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, settingsPath)
	t.Cleanup(fs.Close)
	terminalService := terminal.New(manager, data)
	t.Cleanup(func() { _ = terminalService.Shutdown(t.Context()) })
	build := filepath.Join(root, "build")
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "suite.c")
	if err := os.WriteFile(source, []byte(coverageFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(build, executableName("pipeline-suite"))
	service := New(manager, fs, terminalService, nil, nil)
	coverageErrors := make(chan string, 1)
	service.SetCoverageNotifier(func(event CoverageEvent) {
		if event.State == "error" {
			select {
			case coverageErrors <- event.Message:
			default:
			}
		}
	})
	waitForCoverage := func(sessionID string) *CoverageSnapshot {
		t.Helper()
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			coverage, _, err := service.Coverage(workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			if coverage != nil && coverage.SessionID == sessionID {
				return coverage
			}
			select {
			case message := <-coverageErrors:
				t.Fatalf("C coverage failed: %s", message)
			case <-deadline.C:
				t.Fatalf("C coverage was not published for session %s", sessionID)
			case <-ticker.C:
			}
		}
	}
	_, err = service.SetConfig(workspace.ID, gotestconfig.CConfig{Targets: []gotestconfig.CTarget{{
		ID: "unit", Name: "Unit tests", Entry: gotestconfig.CEntry{File: source, Function: "main"},
		Build:      &gotestconfig.Command{Command: "gcc", Args: []string{"--coverage", "-O0", source, "-o", executable}, Cwd: build},
		Executable: executable, Cwd: root, SourceRoots: []string{root},
		Coverage: gotestconfig.CCoverage{Provider: "gcov", ObjectRoots: []string{build}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Run(workspace.ID, RunRequest{TargetID: "unit"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case message := <-coverageErrors:
			t.Fatalf("C coverage failed: %s", message)
		default:
		}
		current, syncErr := terminalService.Sync(workspace.ID, snapshot.ID, 0)
		if syncErr != nil {
			t.Fatal(syncErr)
		}
		if current.Status == "exited" {
			if current.TaskStatus != "passed" {
				t.Fatalf("pipeline = %#v", current)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("C test pipeline did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	coverage := waitForCoverage(snapshot.ID)
	if coverage.TargetID != "unit" || coverage.Provider != "gcov" {
		t.Fatalf("coverage = %#v", coverage)
	}
	assertCoverageStates(t, coverage.Files)
	ref, err := refForHostPath(fs, workspace.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	service.HandleWorkspaceChanges(workspace.ID, []workspacefs.Change{{Ref: ref}})
	if coverage, _, coverageErr := service.Coverage(workspace.ID); coverageErr != nil || coverage == nil {
		t.Fatalf("delayed unchanged event cleared coverage: %#v, %v", coverage, coverageErr)
	}
	if err := os.WriteFile(source, []byte(coverageFixtureSource+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.HandleWorkspaceChanges(workspace.ID, []workspacefs.Change{{Ref: ref}})
	if coverage, _, coverageErr := service.Coverage(workspace.ID); coverageErr != nil || coverage != nil {
		t.Fatalf("source edit did not clear coverage: %#v, %v", coverage, coverageErr)
	}
	rerun, err := service.Rerun(workspace.ID, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverageStates(t, waitForCoverage(rerun.ID).Files)
}

func newCoverageFixture(t *testing.T) (*Service, workspaces.Workspace, string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(t.TempDir(), "echo.json")
	data := appdata.NewStore(settingsPath)
	manager := workspaces.NewManagerWithData(data)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "C coverage", MainPath: root})
	if err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, settingsPath)
	t.Cleanup(fs.Close)
	build := filepath.Join(root, "build")
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "suite.c")
	if err := os.WriteFile(source, []byte(coverageFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(manager, fs, nil, nil, nil), workspace, root, source, build
}

func requireGcovToolchain(t *testing.T) {
	t.Helper()
	if version := coverageToolOutput(t, "gcc", "--version"); !strings.Contains(version, "Free Software Foundation") {
		unavailableCoverageToolchain(t, "gcc is not GNU GCC (Apple's gcc is a Clang alias)")
	}
	release := strings.TrimSpace(coverageToolOutput(t, "gcc", "-dumpfullversion", "-dumpversion"))
	gcovVersion := coverageToolOutput(t, "gcov", "--version")
	firstLine, _, _ := strings.Cut(gcovVersion, "\n")
	if release == "" || !strings.Contains(gcovVersion, "Free Software Foundation") || !strings.Contains(firstLine, release) {
		unavailableCoverageToolchain(t, fmt.Sprintf("gcov must match GNU GCC %s; found %s", release, firstLine))
	}
	if help := coverageToolOutput(t, "gcov", "--help"); !strings.Contains(help, "--json-format") {
		unavailableCoverageToolchain(t, "gcov does not support --json-format")
	}
}

func coverageToolOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		unavailableCoverageToolchain(t, fmt.Sprintf("%s is not installed: %v", name, err))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.Env = mergeEnvironment(os.Environ(), map[string]string{"LC_ALL": "C"})
	output, err := command.CombinedOutput()
	if err != nil {
		unavailableCoverageToolchain(t, fmt.Sprintf("%s %s failed: %v\n%s", path, strings.Join(args, " "), err, output))
	}
	if len(args) == 1 && args[0] == "--version" {
		firstLine, _, _ := strings.Cut(string(output), "\n")
		t.Logf("%s: %s", path, firstLine)
	}
	return string(output)
}

func unavailableCoverageToolchain(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("ECHO_REQUIRE_C_COVERAGE_TOOLCHAINS") == "1" {
		t.Fatalf("C coverage toolchains are required by ECHO_REQUIRE_C_COVERAGE_TOOLCHAINS: %s", reason)
	}
	t.Skip(reason)
}

func runIntegrationCommand(t *testing.T, cwd string, environment map[string]string, command string, args ...string) {
	t.Helper()
	process := exec.Command(command, args...)
	process.Dir = cwd
	process.Env = mergeEnvironment(os.Environ(), environment)
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", command, err, output)
	}
}

func assertCoverageStates(t *testing.T, files []CoverageFile) {
	t.Helper()
	if len(files) != 1 || !stringsEqualFold(files[0].Ref.Path, "suite.c") {
		t.Fatalf("coverage files = %#v", files)
	}
	states := map[string]bool{}
	for _, line := range files[0].Lines {
		states[line.State] = true
	}
	for _, state := range []string{"covered", "partial", "uncovered"} {
		if !states[state] {
			t.Fatalf("coverage did not contain %s lines: %#v", state, files[0].Lines)
		}
	}
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func stringsEqualFold(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
