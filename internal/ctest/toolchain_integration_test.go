package ctest

import (
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
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc is not installed")
	}
	if _, err := exec.LookPath("gcov"); err != nil {
		t.Skip("gcov is not installed")
	}
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
	for _, tool := range []string{"clang", "llvm-profdata", "llvm-cov"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
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
	for _, tool := range []string{"gcc", "gcov"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
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
	for {
		coverage, _, coverageErr := service.Coverage(workspace.ID)
		if coverageErr != nil {
			t.Fatal(coverageErr)
		}
		if coverage != nil {
			if coverage.TargetID != "unit" || coverage.Provider != "gcov" || coverage.SessionID != snapshot.ID {
				t.Fatalf("coverage = %#v", coverage)
			}
			assertCoverageStates(t, coverage.Files)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("C coverage was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	for {
		coverage, _, coverageErr := service.Coverage(workspace.ID)
		if coverageErr != nil {
			t.Fatal(coverageErr)
		}
		if coverage != nil && coverage.SessionID == rerun.ID {
			assertCoverageStates(t, coverage.Files)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rerun coverage was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newCoverageFixture(t *testing.T) (*Service, workspaces.Workspace, string, string, string) {
	t.Helper()
	root := t.TempDir()
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
