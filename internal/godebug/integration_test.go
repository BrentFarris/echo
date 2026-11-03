package godebug

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDelveLaunchModesAndAttach(t *testing.T) {
	if os.Getenv("ECHO_TEST_DELVE") != "1" {
		t.Skip("set ECHO_TEST_DELVE=1 for native Delve acceptance")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod": "module example.com/debugprobe\ngo 1.26.0\n",
		"main.go": `package main
import("fmt";"runtime";"time";"os")
func main(){if len(os.Args)>1 {if os.Args[1]=="content" {cwd,err:=os.Getwd();if err!=nil {panic(err)};content,err:=os.ReadFile("content/marker.txt");if err!=nil {panic(err)};fmt.Printf("cwd=%s\ncontent=%s\n",cwd,content);return};if os.Args[1]=="no-debug" {fmt.Println("no-debug=42");return};os.WriteFile(os.Args[1],[]byte("ready"),0600);for {time.Sleep(time.Second)}};value:=42;runtime.Breakpoint();fmt.Printf("probe=%d\n",value)}
`,
		"main_test.go": `package main
import("runtime";"testing")
func TestProbe(t *testing.T){value:=42;runtime.Breakpoint();if value!=42 {t.Fatal(value)}}
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for directory, marker := range map[string]string{root: "workspace", filepath.Join(root, "runtime data"): "runtime"} {
		content := filepath.Join(directory, "content")
		if err := os.MkdirAll(content, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(content, "marker.txt"), []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "probe")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", binary, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bot := domain.Bot{ID: "bot", Roots: []string{root}}
	fs := editorfs.New(func(string) (domain.Bot, error) { return bot, nil }, "")
	roots, _ := fs.Roots(bot.ID)
	config := debugconfig.Configuration{ID: "probe", Name: "Probe", AdapterProfileID: "go", Request: "launch", Folder: root, RootID: roots[0].ID, Settings: map[string]any{"go.alternateTools": map[string]any{"dlv": dlv}}}
	service := debugger.New(debugger.Options{FS: fs, State: debugconfig.NewStateStore(db), Configurations: func(string) (debugconfig.WorkspaceConfig, error) {
		return debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{config}}, nil
	}, Provider: func(string, string) (debugger.Provider, error) {
		return &Provider{Project: func(context.Context, string) (broker.ProcessRequest, error) { return broker.ProcessRequest{}, nil }}, nil
	}, AllowedSource: func(string) bool { return true }})
	defer service.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stopped := func(id string) debugger.SessionSnapshot {
		t.Helper()
		for {
			snapshot, _ := service.Snapshot(bot.ID)
			for _, session := range snapshot.Sessions {
				if session.ID != id {
					continue
				}
				if session.Status == debugger.StatusStopped {
					return session
				}
				if session.Status == debugger.StatusFailed || session.Status == debugger.StatusTerminated {
					t.Fatalf("unexpected session end: %+v", session)
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	for _, mode := range []string{"debug", "test", "auto", "exec"} {
		t.Run(mode, func(t *testing.T) {
			program := root
			if mode == "exec" {
				program = binary
			}
			if mode == "auto" {
				program = filepath.Join(root, "main_test.go")
			}
			config.Arguments = map[string]any{"mode": mode, "program": program}
			snapshot, err := service.Start(ctx, bot.ID, debugger.StartRequest{ConfigurationID: "probe"})
			if err != nil {
				t.Fatal(err)
			}
			session := stopped(snapshot.Sessions[len(snapshot.Sessions)-1].ID)
			response, err := service.Request(ctx, bot.ID, session.ID, "stackTrace", debugger.ControlRequest{StopGeneration: session.StopGeneration, Arguments: map[string]any{"threadId": session.ThreadID}})
			if err != nil {
				t.Fatal(err)
			}
			var stack struct {
				Frames []debugger.Frame `json:"stackFrames"`
			}
			_ = json.Unmarshal(response.Body, &stack)
			found := false
			for _, frame := range stack.Frames {
				if strings.HasPrefix(frame.Name, "main.") || strings.Contains(frame.Name, "TestProbe") {
					found = true
				}
			}
			if !found {
				t.Fatal(string(response.Body))
			}
			if err = service.Stop(bot.ID, session.ID, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, probe := range []struct {
		name, mode, program, cwd, argument, want string
		noDebug                                  bool
	}{
		{"without-debugging", "exec", binary, "", "no-debug", "no-debug=42", true},
		{"cwd-workspace", "debug", "${workspaceFolder}", "${workspaceFolder}", "content", "content=workspace", false},
		{"cwd-runtime-folder", "debug", "${workspaceFolder}", "${workspaceFolder}/runtime data", "content", "content=runtime", false},
		{"cwd-exec", "exec", binary, "runtime data", "content", "content=runtime", false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			config.Arguments = map[string]any{"mode": probe.mode, "program": probe.program, "cwd": probe.cwd, "args": []any{probe.argument}}
			snap, err := service.Start(ctx, bot.ID, debugger.StartRequest{ConfigurationID: "probe", NoDebug: probe.noDebug})
			if err != nil {
				t.Fatal(err)
			}
			id := snap.Sessions[len(snap.Sessions)-1].ID
			for {
				snapshot, _ := service.Snapshot(bot.ID)
				for _, session := range snapshot.Sessions {
					if session.ID == id {
						if session.Status == debugger.StatusFailed || session.Status == debugger.StatusStopped {
							t.Fatalf("content launch did not finish: %+v", session)
						}
						if session.Status == debugger.StatusTerminated {
							output := ""
							for _, entry := range session.Output {
								output += entry.Output
							}
							if !strings.Contains(output, probe.want) {
								t.Fatal(output)
							}
							return
						}
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
	t.Run("attach-detach", func(t *testing.T) {
		ready := filepath.Join(root, "ready")
		process := exec.Command(binary, ready)
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- process.Wait() }()
		defer process.Process.Kill()
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			select {
			case err := <-exited:
				t.Fatalf("target exited: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
		config.Request = "attach"
		config.Arguments = map[string]any{"mode": "local", "processId": process.Process.Pid, "stopOnEntry": true}
		snapshot, err := service.Start(ctx, bot.ID, debugger.StartRequest{ConfigurationID: "probe"})
		if err != nil {
			t.Fatal(err)
		}
		session := stopped(snapshot.Sessions[len(snapshot.Sessions)-1].ID)
		if err = service.Stop(bot.ID, session.ID, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-exited:
			t.Fatalf("detach killed target: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
	})
}
