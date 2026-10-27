//go:build windows

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func nativeSCM(t *testing.T, s *Server, bot domain.Bot, tool, directory, command string, args []string, input string) broker.ProcessResult {
	t.Helper()
	params := map[string]any{"directory": directory}
	if strings.HasSuffix(tool, "_command") {
		params["command"] = command
		if input != "" {
			params["input"] = input
		}
	}
	if args != nil {
		params["args"] = args
	}
	raw, _ := json.Marshal(params)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value, e := s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, tool, raw)
	if e != nil {
		t.Fatal(tool, command, e)
	}
	data, _ := json.Marshal(value)
	var result broker.ProcessResult
	json.Unmarshal(data, &result)
	if result.ExitCode != 0 {
		t.Fatalf("%s %s: %+v", tool, command, result)
	}
	return result
}

func TestNativeSourceControlRepositories(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native source-control integration")
	}
	for _, id := range []string{"git", "fossil"} {
		t.Run(id, func(t *testing.T) {
			if _, e := exec.LookPath(id + ".exe"); e != nil {
				if os.Getenv("ECHO2_REQUIRE_TOOLCHAINS") == "1" {
					t.Fatal(e)
				}
				t.Skip(e)
			}
			s := testServer(t)
			root := t.TempDir()
			checkout := filepath.Join(root, "checkout")
			os.Mkdir(checkout, 0700)
			bot, e := s.saveBot(domain.Bot{Handle: "builder", Roots: []string{root}, Plugins: map[string]bool{"shell": false}})
			if e != nil {
				t.Fatal(e)
			}
			plugin, _ := s.Plugins.Get(id)
			if id == "git" {
				plugin.Config = map[string]any{"userName": "Echo Test", "userEmail": "echo@example.invalid"}
			} else {
				plugin.Config = map[string]any{"user": "echo-test"}
			}
			s.Plugins.Save(plugin)
			nativeSCM(t, s, bot, id+"_command", checkout, "version", nil, "")
			if id == "git" {
				nativeSCM(t, s, bot, "git_command", checkout, "init", nil, "")
			} else {
				repository := filepath.Join(root, "project.fossil")
				nativeSCM(t, s, bot, "fossil_command", root, "init", []string{"--admin-user", "echo-test", repository}, "")
				nativeSCM(t, s, bot, "fossil_command", checkout, "open", []string{repository, "--nosync"}, "")
			}
			os.WriteFile(filepath.Join(checkout, "file with spaces.txt"), []byte("initial\n"), 0600)
			nativeSCM(t, s, bot, id+"_command", checkout, "add", []string{"file with spaces.txt"}, "")
			message := `literal $(no shell) ; & 'quotes' "double"`
			commitArgs := []string{"-m", message}
			if id == "fossil" {
				commitArgs = append(commitArgs, "--nosync", "--no-prompt", "--nosign", "--no-verify-comment")
			}
			nativeSCM(t, s, bot, id+"_command", checkout, "commit", commitArgs, "")
			status := nativeSCM(t, s, bot, id+"_status", checkout, "", nil, "")
			if strings.Contains(status.Output, "ADDED") {
				t.Fatal(status.Output)
			}
			os.WriteFile(filepath.Join(checkout, "file with spaces.txt"), []byte("initial\nchanged\n"), 0600)
			diff := nativeSCM(t, s, bot, id+"_diff", checkout, "", nil, "")
			if !strings.Contains(diff.Output, "changed") {
				t.Fatal(diff.Output)
			}
			if id == "git" {
				log := nativeSCM(t, s, bot, "git_command", checkout, "log", []string{"-1", "--format=%s"}, "")
				if !strings.Contains(log.Output, message) {
					t.Fatal("argument quoting changed commit message", log.Output)
				}
			}
			denied := t.TempDir()
			params, _ := json.Marshal(map[string]any{"command": "status", "directory": denied})
			if _, e = s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, id+"_command", params); e == nil {
				t.Fatal("outside directory allowed")
			}
			journals, _ := filepath.Glob(filepath.Join(s.Process.Dir, "*.json"))
			if len(journals) != 0 {
				t.Fatal("ACL journal leaked", journals)
			}
		})
	}
}

func TestNativePerforceDepot(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native Perforce integration")
	}
	client, daemon := os.Getenv("ECHO2_TEST_P4"), os.Getenv("ECHO2_TEST_P4D")
	if client == "" {
		client, _ = exec.LookPath("p4.exe")
	}
	if daemon == "" {
		daemon, _ = exec.LookPath("p4d.exe")
	}
	if client == "" || daemon == "" {
		if os.Getenv("ECHO2_REQUIRE_TOOLCHAINS") == "1" {
			t.Fatal("release tests require p4.exe and p4d.exe on PATH or ECHO2_TEST_P4 / ECHO2_TEST_P4D")
		}
		t.Skip("set ECHO2_TEST_P4 and ECHO2_TEST_P4D to portable test binaries")
	}
	root := t.TempDir()
	depot := filepath.Join(root, "server")
	workspace := filepath.Join(root, "workspace")
	os.Mkdir(depot, 0700)
	os.Mkdir(workspace, 0700)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	listener.Close()
	process := exec.Command(daemon, "-r", depot, "-p", address, "-L", filepath.Join(root, "p4d.log"), "-J", filepath.Join(root, "journal"))
	process.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if e = process.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { process.Process.Kill(); process.Wait() }()
	defer func() {
		if t.Failed() {
			log, _ := os.ReadFile(filepath.Join(root, "p4d.log"))
			t.Log(string(log))
		}
	}()
	ready := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !ready {
		t.Fatal("test P4 server did not start")
	}
	s := testServer(t)
	m, _ := s.Plugins.Get("p4")
	m.Config = map[string]any{"executable": client, "port": address, "user": "echo-test", "client": "echo-workspace"}
	s.Plugins.Save(m)
	echo, _ := s.Bot("echo")
	// No machine-wide loopback exemptions: test depot networking with the normal
	// unbound actor; separately verify the client executable in AppContainer.
	bound := domain.Bot{ID: "scope", Handle: "scope", Roots: []string{workspace}}
	nativeSCM(t, s, bound, "p4_command", workspace, "version", nil, "")
	info := nativeSCM(t, s, echo, "p4_command", workspace, "info", nil, "")
	if !strings.Contains(info.Output, "Server address:") {
		t.Fatal(info.Output)
	}
	spec := fmt.Sprintf("Client: echo-workspace\nOwner: echo-test\nRoot: %s\nOptions: noallwrite noclobber nocompress unlocked nomodtime normdir\nLineEnd: local\nView:\n\t//depot/... //echo-workspace/...\n", workspace)
	nativeSCM(t, s, echo, "p4_command", workspace, "client", []string{"-i"}, spec)
	file := filepath.Join(workspace, "notes.txt")
	os.WriteFile(file, []byte("first\n"), 0600)
	nativeSCM(t, s, echo, "p4_command", workspace, "add", []string{"notes.txt"}, "")
	submitted := nativeSCM(t, s, echo, "p4_command", workspace, "submit", []string{"-d", "Echo plugin test"}, "")
	if !strings.Contains(submitted.Output, "submitted") {
		t.Fatal(submitted.Output)
	}
	nativeSCM(t, s, echo, "p4_command", workspace, "edit", []string{"notes.txt"}, "")
	os.WriteFile(file, []byte("first\nsecond\n"), 0600)
	diff := nativeSCM(t, s, echo, "p4_diff", workspace, "", nil, "")
	if !strings.Contains(diff.Output, "second") {
		t.Fatal(diff.Output)
	}
	nativeSCM(t, s, echo, "p4_status", workspace, "", nil, "")
	if _, e = s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bound}, "p4_command", json.RawMessage(`{"command":"info"}`)); e == nil || !strings.Contains(e.Error(), "network") {
		t.Fatal("missing owner grant was not actionable", e)
	}
}
