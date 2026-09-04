package debugger

import (
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/debugconfig"
)

func TestPrepareLaunchArgumentsNormalizesVSCodeGoAutoMode(t *testing.T) {
	profile := debugconfig.AdapterProfile{AdapterID: "go"}
	arguments := map[string]any{"mode": "auto", "program": filepath.Join("workspace", "src")}
	prepareLaunchArguments(profile, "launch", arguments)
	if arguments["mode"] != "debug" {
		t.Fatalf("mode = %#v, want debug", arguments["mode"])
	}

	testArguments := map[string]any{"mode": "AUTO", "program": filepath.Join("workspace", "main_test.go")}
	prepareLaunchArguments(profile, "launch", testArguments)
	if testArguments["mode"] != "test" {
		t.Fatalf("test mode = %#v, want test", testArguments["mode"])
	}
}

func TestLaunchAdapterWorkingDirectoryUsesGoProgramWithoutChangingDebuggeeCwd(t *testing.T) {
	workspace := t.TempDir()
	program := filepath.Join(workspace, "src")
	arguments := map[string]any{"mode": "debug", "program": program, "cwd": workspace}
	got := launchAdapterWorkingDirectory(debugconfig.AdapterProfile{AdapterID: "go"}, "launch", arguments, debugconfig.ExpandOptions{WorkspaceFolder: workspace})
	if got != program {
		t.Fatalf("adapter working directory = %q, want %q", got, program)
	}
	if arguments["cwd"] != workspace {
		t.Fatalf("debuggee cwd changed to %#v", arguments["cwd"])
	}
}

func TestLaunchAdapterWorkingDirectoryUsesSandboxPathSemantics(t *testing.T) {
	arguments := map[string]any{"mode": "test", "program": "/workspace/kaiju/src/main_test.go"}
	got := launchAdapterWorkingDirectory(debugconfig.AdapterProfile{AdapterID: "go"}, "launch", arguments, debugconfig.ExpandOptions{WorkspaceFolder: "/workspace/kaiju", SlashPaths: true})
	if got != "/workspace/kaiju/src" {
		t.Fatalf("adapter working directory = %q", got)
	}
}
