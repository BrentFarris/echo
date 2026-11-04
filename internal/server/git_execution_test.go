package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

func setHostGit(t *testing.T, s *Server, on bool) {
	t.Helper()
	response := scmAPI(t, s, "PATCH", "/plugins/git", map[string]any{"enabled": true, "config": map[string]any{"runOutsideSandbox": on}})
	if response.Code != http.StatusOK {
		t.Fatalf("save host Git: %d %s", response.Code, response.Body.String())
	}
}

func TestHostGitOwnerSettings(t *testing.T) {
	s := testServer(t)
	echo, err := s.Bot("echo")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.Plugins.Get("git")
	if boolean(current.Config, "runOutsideSandbox") {
		t.Fatal("host mode enabled by default")
	}
	unauthenticated := httptest.NewRequest("PATCH", "http://localhost/api/v1/plugins/git", strings.NewReader(`{"enabled":true,"config":{"runOutsideSandbox":true}}`))
	rejected := httptest.NewRecorder()
	s.Handler().ServeHTTP(rejected, unauthenticated)
	if rejected.Code != http.StatusUnauthorized && rejected.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated settings update: %d", rejected.Code)
	}
	for _, on := range []bool{false, true} {
		setHostGit(t, s, on)
		raw, _ := json.Marshal(map[string]any{"id": "git", "config": map[string]any{"runOutsideSandbox": !on}})
		_, err := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: echo}, "plugin_configure", raw)
		if err == nil || !strings.Contains(err.Error(), "only the owner") {
			t.Fatalf("chat changed owner setting: %v", err)
		}
		_, err = s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: echo}, "git_status", json.RawMessage(`{"runOutsideSandbox":true}`))
		if err == nil {
			t.Fatal("tool arguments selected host mode")
		}
	}
	if _, err := s.Plugins.Install(domain.PluginManifest{API: 1, ID: "git", Name: "Replacement", Config: map[string]any{"runOutsideSandbox": true}}, nil); err == nil {
		t.Fatal("replaced built-in Git")
	}
	for _, id := range []string{"fossil", "p4", "go"} {
		response := scmAPI(t, s, "PATCH", "/plugins/"+id, map[string]any{"enabled": true, "config": map[string]any{"runOutsideSandbox": true}})
		if response.Code == 200 {
			t.Fatalf("host mode accepted by %s", id)
		}
	}
	runtimeFolder := t.TempDir()
	grant := scmAPI(t, s, "PATCH", "/plugins/git", map[string]any{"enabled": true, "config": map[string]any{"toolchains": []string{runtimeFolder}}})
	if grant.Code != http.StatusOK {
		t.Fatal(grant.Body.String())
	}
	if err := os.Remove(runtimeFolder); err != nil {
		t.Fatal(err)
	}
	setHostGit(t, s, false)
	setHostGit(t, s, true)
	other := testServer(t)
	m, _ := other.Plugins.Get("git")
	if boolean(m.Config, "runOutsideSandbox") {
		t.Fatal("host setting crossed instances")
	}
	s.Close()
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.Dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	m, err = reopened.Plugins.Get("git")
	if err != nil || !boolean(m.Config, "runOutsideSandbox") {
		t.Fatalf("host setting lost on restart: %v %v", m.Config, err)
	}
}

// All repositories, user configuration, credentials and local remotes in the
// native fixture belong to the test account's disposable directories.
func hostGitFixture(t *testing.T) (s *Server, bot domain.Bot, repo, linked string) {
	t.Helper()
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for native Git")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	s = testServer(t)
	repo = t.TempDir()
	folder := filepath.Join(repo, "src")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(folder, "file with spaces.txt")
	if err := os.WriteFile(file, []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSCM(t, repo, "git", "init")
	fixtureSCM(t, repo, "git", "add", ".")
	fixtureSCM(t, repo, "git", "commit", "-m", "initial")
	linked = filepath.Join(t.TempDir(), "linked worktree")
	fixtureSCM(t, repo, "git", "worktree", "add", "--detach", linked)
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[user]\n name = Host Fixture\n email = host@example.invalid\n[alias]\n echo-marker = config user.email\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// Give the fixture a controlled HOME even when the native test uses host permissions.
	t.Setenv("HOME", filepath.Dir(config))
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", filepath.Dir(config))
	}
	var err error
	bot, err = s.saveBot(domain.Bot{Handle: "host-git", Roots: []string{folder, filepath.Join(linked, "src")}, Plugins: map[string]bool{"fossil": false, "p4": false}})
	if err != nil {
		t.Fatal(err)
	}
	setHostGit(t, s, true)
	return
}

func TestNativeHostGitBotAndEditor(t *testing.T) {
	s, bot, repo, linked := hostGitFixture(t)
	run := func(tool, cwd string, args ...string) string {
		t.Helper()
		r, err := invokeGitTool(t, s, bot, tool, cwd, args...)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("%s %q: %+v %v", tool, args, r, err)
		}
		return r.Output
	}
	if actual := strings.TrimSpace(run("git_command", "", "rev-parse", "--show-toplevel")); !sameGitPath(actual, repo) {
		t.Fatal(actual)
	}
	if actual := strings.TrimSpace(run("git_command", ".", "echo-marker")); actual != "host@example.invalid" {
		t.Fatal(actual)
	}
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "post-commit"), []byte("#!/bin/sh\nprintf 'hook ran' > hook-result.txt\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run("git_command", repo, "config", "--global", "core.hooksPath", filepath.ToSlash(hooks))
	path := filepath.Join(linked, "src", "file with spaces.txt")
	if err := os.WriteFile(path, []byte("linked change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Explicit cwd outside both configured folders and shared external metadata.
	for _, item := range []struct {
		tool, command string
		args          []string
	}{{"git_status", "status", []string{"--short"}}, {"git_diff", "diff", nil}} {
		if got, want := run(item.tool, linked, item.args...), fixtureSCM(t, linked, "git", append([]string{item.command}, item.args...)...); got != want {
			t.Fatalf("%s differs from terminal: %q / %q", item.tool, got, want)
		}
	}
	run("git_command", linked, "add", "--", "src/file with spaces.txt")
	run("git_command", linked, "commit", "-m", "message with spaces & literal arguments")
	if data, err := os.ReadFile(filepath.Join(linked, "hook-result.txt")); err != nil || string(data) != "hook ran" {
		t.Fatalf("host post-commit hook: %q %v", data, err)
	}
	if email := strings.TrimSpace(fixtureSCM(t, linked, "git", "log", "-1", "--format=%ae")); email != "host@example.invalid" {
		t.Fatalf("host identity: %q", email)
	}
	// Even an incomplete marker that Git walks past must not make the editor
	// list a parent checkout until parent search is enabled.
	marker := filepath.Join(bot.Roots[0], ".git")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: false}); err != nil {
		t.Fatal(err)
	}
	if repos, err := s.SourceControl.Repositories(ctx, bot.ID); err != nil || len(repos) != 0 {
		t.Fatalf("editor with parent search off: %+v %v", repos, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("source-control-settings", bot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.SourceControl.Repositories(ctx, bot.ID)
	if err != nil || len(repos) != 2 {
		t.Fatalf("editor discovery: %+v %v", repos, err)
	}
	editorFile := filepath.Join(repo, "src", "file with spaces.txt")
	if err := os.WriteFile(editorFile, []byte("editor change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edited := false
	for _, repoView := range repos {
		st, err := s.SourceControl.Status(ctx, bot.ID, repoView.ID)
		if err != nil {
			t.Fatal(err)
		}
		if st.TotalChangeCount == 0 {
			continue
		}
		_, err = s.SourceControl.Action(ctx, bot.ID, repoView.ID, sourcecontrol.ActionRequest{RequestID: "host-commit", Action: "commit_all", Message: "editor host commit"})
		if err != nil {
			t.Fatal(err)
		}
		edited = true
	}
	if !edited || strings.TrimSpace(fixtureSCM(t, repo, "git", "log", "-1", "--format=%s")) != "editor host commit" {
		t.Fatal("editor commit missing")
	}
	if runtime.GOOS != "windows" {
		run("git_command", repo, "config", "alias.null-rw", "!exec 3<>/dev/null")
		if res, err := s.executeSCM(ctx, bot.ID, scmexec.Request{Command: []string{"git", "null-rw"}, WorkingDirectory: repo}); err != nil || res.ExitCode != 0 {
			t.Fatalf("editor /dev/null: %+v %v", res, err)
		}
		run("git_command", repo, "null-rw")
	}
	// Exercise host networking with both sandbox network switches off.
	hash := strings.TrimSpace(fixtureSCM(t, repo, "git", "rev-parse", "HEAD"))
	run("git_command", repo, "config", "--global", "credential.helper", "!f() { printf 'username=fixture\\npassword=fixture-secret\\n'; }; f")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "fixture" || password != "fixture-secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git-fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/info/refs" {
			fmt.Fprintf(w, "%s\trefs/heads/fixture\n", hash)
			return
		}
		if r.URL.Path == "/HEAD" {
			fmt.Fprintln(w, "ref: refs/heads/fixture")
			return
		}
		http.NotFound(w, r)
	}))
	defer remote.Close()
	t.Setenv("NO_PROXY", "127.0.0.1")
	if output := run("git_command", repo, "ls-remote", remote.URL); !strings.Contains(output, hash) {
		t.Fatal(output)
	}
	run("git_status", repo, "--short")
	if files, err := os.ReadDir(s.Process.Dir); err != nil && !os.IsNotExist(err) || len(files) != 0 {
		t.Fatalf("host Git created sandbox state: %v %v", files, err)
	}
	// A working-file mutation still appears in the bot's normal change report.
	if err := os.WriteFile(editorFile, []byte("restore me"), 0600); err != nil {
		t.Fatal(err)
	}
	collector := &fileChangeCollector{}
	ctx = context.WithValue(ctx, fileChangesKey{}, collector)
	_, err = s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, "git_command", json.RawMessage(`{"command":"restore","args":["--","file with spaces.txt"]}`))
	if err != nil || len(collector.changes) != 1 || !sameGitPath(collector.changes[0].Path, editorFile) {
		t.Fatalf("restore change report: %+v %v", collector.changes, err)
	}
	updated, err := s.Bot(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Plugins["git"] = false
	if _, err := s.updateBot(updated, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := invokeGitTool(t, s, bot, "git_command", repo, "version"); err == nil {
		t.Fatal("stale bot snapshot retained host Git permission")
	}
}

func TestHostGitBlockingHelper(t *testing.T) {
	addr := os.Getenv("ECHO_HOST_GIT_HELPER")
	if addr == "" {
		t.Skip("Git alias descendant fixture")
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.Copy(io.Discard, conn)
}

func TestNativeHostGitRevokesRunningAndQueued(t *testing.T) {
	s, bot, repo, linked := hostGitFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("ECHO_HOST_GIT_HELPER", listener.Addr().String())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alias := "!'" + strings.ReplaceAll(filepath.ToSlash(exe), "'", "'\\''") + "' -test.run=^TestHostGitBlockingHelper$"
	fixtureSCM(t, repo, "git", "config", "alias.echo-hold", alias)
	running := make(chan error, 1)
	go func() { _, err := invokeGitTool(t, s, bot, "git_command", repo, "echo-hold"); running <- err }()

	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	child, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	// Wait for the second worktree's real rev-parse probe to finish. Its
	// status must then wait for the first worktree's common index lock.
	trace := filepath.Join(t.TempDir(), "git-trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	queued := make(chan error, 1)
	go func() { _, err := invokeGitTool(t, s, bot, "git_status", linked, "--short"); queued <- err }()
	for deadline := time.Now().Add(8 * time.Second); ; {
		b, _ := os.ReadFile(trace)
		if strings.Contains(string(b), `"event":"exit"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second Git probe did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	old, _ := s.Plugins.Get("git")
	setHostGit(t, s, false)
	for _, done := range []chan error{running, queued} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("old-mode command completed after revocation")
			}
		case <-time.After(time.Second):
			t.Fatal("old-mode command remained queued or running")
		}
	}
	_ = child.SetReadDeadline(time.Now().Add(time.Second))
	// A terminated child closes its socket. Unix returns EOF and Windows
	// can report a Winsock reset. A live child keeps the connection idle.
	if n, err := child.Read(make([]byte, 1)); n != 0 || err == nil || os.IsTimeout(err) {
		t.Fatalf("Git descendant survived Save: %v", err)
	}
	if _, _, release, err := s.beginGit(context.Background(), bot, old); err == nil {
		release()
		t.Fatal("stale manifest still authorized host execution")
	}
	if r, err := invokeGitTool(t, s, bot, "git_status", repo, "--short"); err == nil && r.ExitCode == 0 {
		t.Fatal("new command bypassed the restored sandbox")
	}
}
