//go:build windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"golang.org/x/sys/windows"
)

func TestNativeGitStatusReusesSandboxWithoutFileScan(t *testing.T) {
	testNativeGitStatus(t, false)
}

func TestNativeGitHostStatusHasNoSandboxSetup(t *testing.T) {
	testNativeGitStatus(t, true)
}

func testNativeGitStatus(t *testing.T, hostStatus bool) {
	t.Helper()
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	s := testServer(t)
	repository := t.TempDir()
	roots := []string{filepath.Join(repository, "Content", "Lua"), filepath.Join(repository, "Source", "Modding")}
	for _, root := range roots {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fixtureSCM(t, repository, "git", "init", "--ref-format=files")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("locked.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(roots[0], "locked.txt")
	if err := os.WriteFile(locked, []byte("ignored by Git; must not be fingerprinted by status"), 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "git-status", Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	response := scmAPI(t, s, "PATCH", "/plugins/git", map[string]any{"enabled": true, "config": map[string]any{"hostStatus": hostStatus}})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	m, err := s.Plugins.Get("git")
	if err != nil {
		t.Fatal(err)
	}
	collector := &fileChangeCollector{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), fileChangesKey{}, collector), time.Minute)
	defer cancel()
	status := func(directory string) string {
		t.Helper()
		start := time.Now()
		value, err := s.runSourceControl(ctx, domain.ExecutionScope{Bot: bot, PluginID: "git", Standalone: true}, m, "git.run", map[string]any{"command": "status", "directory": directory, "args": []string{"--short"}})
		if err != nil {
			t.Fatal(err)
		}
		result := value.(broker.ProcessResult)
		if result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" {
			t.Fatalf("status response: %+v", result)
		}
		t.Logf("status in %q: %s", directory, time.Since(start))
		return result.Output
	}
	status("") // Prepare the inheritable grant before locking an ignored file.
	journalFile := func() string {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(s.Process.Dir, "*.json"))
		expected := 1
		if hostStatus {
			expected = 0
		}
		if err != nil || len(files) != expected {
			t.Fatalf("expected %d sandbox journals, got %v: %v", expected, files, err)
		}
		if len(files) == 0 {
			return ""
		}
		return files[0]
	}
	journal := journalFile()
	name, err := windows.UTF16PtrFromString(locked)
	if err != nil {
		t.Fatal(err)
	}
	// Git ignores this file. A before/after file scan would try to read it
	// and mark the collector incomplete because this handle denies reads.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if snapshot := s.Files.SnapshotFiles(ctx, bot, bot.Roots); snapshot.Complete {
		t.Fatal("fixture did not prevent file fingerprinting")
	}
	for _, directory := range []string{roots[1], repository} {
		if output := status(directory); output == "" {
			t.Fatal("combined Git output was lost")
		}
		if journalFile() != journal {
			t.Fatal("Git status replaced its sandbox when the working directory changed")
		}
	}
	if collector.incomplete || len(collector.changes) != 0 {
		t.Fatalf("status scanned working files: %+v", collector)
	}
	if hostStatus {
		unrelated := t.TempDir()
		fixtureSCM(t, unrelated, "git", "init")
		_, err := invokeGitTool(t, s, bot, "git_status", unrelated, "--short")
		if err == nil {
			t.Fatal("host status accepted an unrelated working directory")
		}
		setHostGit(t, s, true)
		if r, err := invokeGitTool(t, s, bot, "git_status", unrelated, "--short"); err != nil || r.ExitCode != 0 {
			t.Fatalf("full host mode with host status enabled: %+v %v", r, err)
		}
		setHostGit(t, s, false)
		if _, err := invokeGitTool(t, s, bot, "git_status", unrelated, "--short"); err == nil {
			t.Fatal("disabling full host mode did not restore the status directory check")
		}
	}
}

func TestNativeCommandFileChangesIncludePartialEffects(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	s := testServer(t)
	root := t.TempDir()
	for _, name := range []string{"edited.txt", "deleted.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collector := &fileChangeCollector{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), fileChangesKey{}, collector), 30*time.Second)
	defer cancel()
	bot := domain.Bot{ID: "file-changes", Roots: []string{root}}
	result, err := s.runTrackedProcess(ctx, bot, broker.ProcessRequest{BotID: bot.ID, Roots: bot.Roots, Directory: root, Command: "[IO.File]::WriteAllText([IO.Path]::Combine($env:ECHO_WORKSPACE, 'created.txt'), 'new'); [IO.File]::WriteAllText([IO.Path]::Combine($env:ECHO_WORKSPACE, 'edited.txt'), 'after'); [IO.File]::Delete([IO.Path]::Combine($env:ECHO_WORKSPACE, 'deleted.txt')); exit 7"}, nil)
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("%+v %v", result, err)
	}
	if collector.incomplete || len(collector.changes) != 3 {
		t.Fatalf("%+v", collector)
	}
	operations := map[string]string{}
	for _, change := range collector.changes {
		operations[filepath.Base(change.Path)] = change.Operation
	}
	if operations["created.txt"] != "created" || operations["edited.txt"] != "edited" || operations["deleted.txt"] != "deleted" {
		t.Fatal(operations)
	}
}
