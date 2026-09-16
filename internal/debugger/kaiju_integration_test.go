package debugger

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

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/workspaces"
)

// Optional acceptance against the existing Kaiju Editor launch. Stops before
// window creation, writes the debug binary/settings to a test directory, and
// never saves changes to Kaiju's launch or workspace configuration.
func TestKaijuCGOStepping(t *testing.T) {
	root := os.Getenv("ECHO_KAIJU_WORKSPACE")
	if root == "" || runtime.GOOS != "windows" {
		t.Skip("set ECHO_KAIJU_WORKSPACE on Windows to verify the existing Editor launch")
	}
	content, err := os.ReadFile(filepath.Join(root, ".echo", "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Debug debugconfig.WorkspaceConfig `json:"debug"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	configuration, found := document.Debug.Configuration("editor")
	if !found {
		t.Fatal("Kaiju Editor debug configuration was not found")
	}
	temporary := t.TempDir()
	configuration.Arguments["output"] = filepath.Join(temporary, "kaiju-debug.exe")
	configuration.Arguments["buildFlags"] = stringValue(configuration.Arguments["buildFlags"], "") + " -mod=readonly"
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := debugconfig.TemplateByID("delve")
	profile.Command = dlv
	data := appdata.NewStore(filepath.Join(temporary, "echo.json"))
	profiles := debugconfig.NewProfileStore(data)
	if err := profiles.Save([]debugconfig.AdapterProfile{profile}); err != nil {
		t.Fatal(err)
	}
	states := debugconfig.NewStateStore(data)
	if _, err := states.Save("kaiju-test", 0, debugconfig.State{FunctionBreakpoints: []debugconfig.FunctionBreakpoint{{ID: "before-window", Name: "kaijuengine.com/platform/windowing.(*Window).checkToggleKeyState", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	workspace := workspaces.Workspace{ID: "kaiju-test", MainPath: root, Folders: []string{root}, Debug: document.Debug}
	service := New(profiles, states, staticWorkspaceResolver{workspace}, nil)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	events := make(chan Event, 256)
	service.SetNotifier(func(event Event) { events <- event })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	snapshot, err := service.StartTransient(ctx, workspace.ID, configuration, StartRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.Sessions[0].ID
	var stopped SessionSnapshot
	for steps := 0; steps < 4; steps++ {
		waiting := true
		for waiting {
			select {
			case event := <-events:
				if event.Output != nil {
					t.Log(strings.TrimSpace(event.Output.Output))
				}
				if event.Event == "failed" || event.Event == "terminated" {
					t.Fatalf("Editor launch ended: %+v", event.Session)
				}
				if event.Event == "stopped" {
					stopped = *event.Session
					waiting = false
				}
			case <-ctx.Done():
				t.Fatal("Editor launch did not reach its breakpoint")
			}
		}
		response, err := service.Request(ctx, workspace.ID, id, "stackTrace", ControlRequest{StopGeneration: stopped.StopGeneration, Arguments: map[string]any{"threadId": stopped.ThreadID, "levels": 30}})
		if err != nil {
			t.Fatal(err)
		}
		var stack struct {
			Frames []stepFrame `json:"stackFrames"`
		}
		if err := json.Unmarshal(response.Body, &stack); err != nil || len(stack.Frames) == 0 {
			t.Fatalf("stack: %s, %v", response.Body, err)
		}
		frame := stack.Frames[0]
		t.Logf("Editor stop: %s at %s:%d", frame.Name, frame.Source.Path, frame.Line)
		if frame.Name == "C.get_toggle_key_state" && strings.HasSuffix(frame.Source.Path, "win32.c") {
			values, err := service.Request(ctx, workspace.ID, id, "scopes", ControlRequest{StopGeneration: stopped.StopGeneration, Arguments: map[string]any{"frameId": frame.ID}})
			if err != nil || !strings.Contains(string(values.Body), "Locals") {
				t.Fatalf("C scopes: %s, %v", values.Body, err)
			}
			return
		}
		if !strings.Contains(frame.Name, "checkToggleKeyState") {
			t.Fatalf("unexpected Editor stop: %+v", frame)
		}
		if _, err := service.Request(ctx, workspace.ID, id, "stepIn", ControlRequest{StopGeneration: stopped.StopGeneration, Arguments: map[string]any{"threadId": stopped.ThreadID}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("F11 did not enter Kaiju's C implementation")
}
