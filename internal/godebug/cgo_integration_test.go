package godebug

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/store"
)

// CI explicitly enables this suite and installs Delve plus the target C compiler.
func TestDelveCGOStepping(t *testing.T) {
	if os.Getenv("ECHO_TEST_DELVE") != "1" {
		t.Skip("set ECHO_TEST_DELVE=1 to run real Go/C debugger tests")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("testdata/cgo")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "cgo-probe")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-gcflags=all=-N -l", "-o", binary, ".")
	build.Dir = fixture
	build.Env = debugger.Environment(map[string]string{"CGO_ENABLED": "1", "CGO_CFLAGS": "-O0 -g"})
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	data, err := store.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	bot := domain.Bot{ID: "workspace", Roots: []string{root}}
	fs := editorfs.New(func(string) (domain.Bot, error) { return bot, nil }, filepath.Join(root, "state"))
	config := debugconfig.Configuration{ID: "cgo-test", Name: "CGO", AdapterProfileID: "go", Request: "launch", Folder: root, Settings: map[string]any{"go.alternateTools": map[string]any{"dlv": dlv}}, Arguments: map[string]any{"mode": "exec", "program": binary}}
	service := debugger.New(debugger.Options{FS: fs, State: debugconfig.NewStateStore(data), Resolve: func(string) error { return nil }, Configurations: func(string) (debugconfig.WorkspaceConfig, error) {
		return debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{config}}, nil
	}, Provider: func(string, string) (debugger.Provider, error) {
		return &Provider{Project: func(context.Context, string) (broker.ProcessRequest, error) { return broker.ProcessRequest{}, nil }}, nil
	}, AllowedSource: func(string) bool { return true }})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	exerciseCGOStepping(t, ctx, service, bot.ID)
}

func exerciseCGOStepping(t *testing.T, ctx context.Context, service *debugger.Service, botID string) {
	t.Helper()
	events := make(chan debugger.Event, 1024)
	service.SetNotifier(func(event debugger.Event) { events <- event })
	snapshot, err := service.Start(ctx, botID, debugger.StartRequest{ConfigurationID: "cgo-test"})
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.Sessions[0].ID
	stops := 0
	waitStop := func() debugger.SessionSnapshot {
		t.Helper()
		for {
			select {
			case event := <-events:
				if event.Event == "failed" || event.Event == "terminated" {
					t.Fatalf("unexpected end: %+v", event.Session)
				}
				if event.Event == "output" && event.Output != nil {
					t.Log(strings.TrimSpace(event.Output.Output))
				}
				if event.Event == "stopped" {
					stops++
					return *event.Session
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for debugger stop")
			}
		}
	}
	stop := waitStop()
	request := func(command string, arguments map[string]any) map[string]any {
		t.Helper()
		response, err := service.Request(ctx, botID, id, command, debugger.ControlRequest{StopGeneration: stop.StopGeneration, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		var body map[string]any
		_ = json.Unmarshal(response.Body, &body)
		return body
	}
	frames := func() []any {
		t.Helper()
		return request("stackTrace", map[string]any{"threadId": stop.ThreadID, "levels": 30})["stackFrames"].([]any)
	}
	step := func(command string) map[string]any {
		t.Helper()
		request(command, map[string]any{"threadId": stop.ThreadID})
		stop = waitStop()
		frame := frames()[0].(map[string]any)
		t.Logf("%s -> %s:%v (%s)", command, frame["name"], frame["line"], stop.StoppedText)
		return frame
	}
	frame := step("stepIn")
	if frame["name"] != "C.native_add" {
		t.Fatalf("F11 stopped at %v", frame)
	}
	if stops != 2 {
		t.Fatalf("bridge emitted intermediate stops: %d", stops)
	}
	evaluate := func(expression, want string) {
		t.Helper()
		body := request("evaluate", map[string]any{"expression": expression, "frameId": frame["id"], "context": "hover"})
		if body["result"] != want {
			t.Fatalf("%s = %v, want %s", expression, body["result"], want)
		}
	}
	evaluate("value", "7")
	source := frame["source"].(map[string]any)
	if source["echoSourceId"] == nil {
		t.Fatal("missing source identity")
	}
	content := request("source", map[string]any{"source": source})["content"].(string)
	if !strings.Contains(content, "Sample sample") {
		t.Fatal("external C source not loaded")
	}
	// Compilers assign different source lines to the native prologue. Advance
	// to the call statement rather than assuming a platform-specific step count.
	callLine := 0
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "local = native_leaf(local)") {
			callLine = index + 1
		}
	}
	for count := 0; intValue(frame["line"]) != callLine && count < 10; count++ {
		frame = step("next")
	}
	if callLine == 0 || intValue(frame["line"]) != callLine || frame["name"] != "C.native_add" {
		t.Fatalf("could not reach nested native call: %v", frame)
	}
	evaluate("sample.value", "7")
	evaluate("ptr->items[1]", "2")
	evaluate("native_global", "10")
	scopes := request("scopes", map[string]any{"frameId": frame["id"]})["scopes"].([]any)
	if len(request("variables", map[string]any{"variablesReference": scopes[0].(map[string]any)["variablesReference"]})["variables"].([]any)) == 0 {
		t.Fatal("empty C variables")
	}
	foundGo := false
	for _, raw := range frames() {
		if raw.(map[string]any)["name"] == "main.main" {
			foundGo = true
		}
	}
	if !foundGo {
		t.Fatal("Go caller missing from mixed stack")
	}
	frame = step("stepIn")
	if frame["name"] != "C.native_leaf" {
		t.Fatalf("nested C call: %v", frame)
	}
	frame = step("stepOut")
	if frame["name"] != "C.native_add" {
		t.Fatalf("C caller: %v", frame)
	}
	frame = step("stepOut")
	if frame["name"] != "main.main" {
		t.Fatalf("Go caller: %v", frame)
	}
	frame = step("continue")
	frame = step("stepIn")
	if frame["name"] != "C.native_callback" {
		t.Fatalf("callback C entry: %v", frame)
	}
	frame = step("stepIn")
	if frame["name"] != "main.goDouble" {
		t.Fatalf("callback Go entry: %v", frame)
	}
	evaluate("value", "4")
	frame = step("stepOut")
	if frame["name"] != "C.native_callback" {
		t.Fatalf("callback C caller: %v", frame)
	}
}
