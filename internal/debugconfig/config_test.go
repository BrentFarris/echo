package debugconfig

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTemplatesAreValidAndUsePrivateServerPorts(t *testing.T) {
	for _, template := range Templates() {
		t.Run(template.ID, func(t *testing.T) {
			profile := template.Profile.Normalized()
			if err := profile.Validate(); err != nil {
				t.Fatalf("template is invalid: %v", err)
			}
			if profile.Transport.Kind == "server" && !strings.Contains(strings.Join(append([]string{profile.Command}, profile.Args...), " "), "${debugAdapterPort}") {
				t.Fatal("server template does not use the allocated port")
			}
		})
	}
}

func TestProfileAndWorkspaceValidation(t *testing.T) {
	profile, _ := TemplateByID("delve")
	config := WorkspaceConfig{
		Version: CurrentVersion, EnabledAdapterProfileIDs: []string{"delve"},
		Configurations: []Configuration{{ID: "main", Name: "Go Main", AdapterProfileID: "delve", Request: "launch", Arguments: map[string]any{"program": "${workspaceFolder}"}}},
		Compounds:      []Compound{{ID: "all", Name: "All", ConfigurationIDs: []string{"main"}, StopAll: true}},
		Inputs:         []Input{{ID: "mode", Type: "pickString", Options: []string{"one", "two"}}},
	}
	if err := config.Validate([]AdapterProfile{profile}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalidServer := profile
	invalidServer.Args = []string{"dap", "--listen=127.0.0.1:4000"}
	if err := invalidServer.Validate(); err == nil || !strings.Contains(err.Error(), "debugAdapterPort") {
		t.Fatalf("fixed spawned-server port error = %v", err)
	}
	invalidServer = profile
	invalidServer.Transport.Host = "0.0.0.0"
	if err := invalidServer.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback spawned server error = %v", err)
	}

	config.Configurations[0].Arguments["program"] = "${command:askUser}"
	if err := config.ValidateStructure(); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("command variable error = %v", err)
	}
}

func TestRecursiveExpansionAndRejectedVariables(t *testing.T) {
	root := filepath.Join("tmp", "workspace")
	file := filepath.Join(root, "cmd", "main.go")
	value := map[string]any{
		"program": "${file}",
		"args":    []any{"--root=${workspaceFolder}", "--mode=${input:MODE}", "${env:ECHO_DEBUG_TEST}"},
		"nested":  map[string]any{"selected": "${selectedText}"},
	}
	expanded, err := ExpandValue(value, ExpandOptions{
		WorkspaceFolder: root, CurrentFile: file, SelectedText: "hello",
		Inputs: map[string]string{"mode": "test"}, Environment: map[string]string{"ECHO_DEBUG_TEST": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(expanded)
	text := string(encoded)
	for _, want := range []string{filepath.Base(file), "--mode=test", "value", "hello"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expanded value %s does not contain %q", text, want)
		}
	}
	if _, err := ExpandString("${command:pickProcess}", ExpandOptions{}); err == nil {
		t.Fatal("command variable was accepted")
	}
	if _, err := ExpandString("${file}", ExpandOptions{}); err == nil {
		t.Fatal("active-file variable without a saved file was accepted")
	}
	got, err := ExpandString("${debugAdapterPort}", ExpandOptions{PreserveDebugAdapterPort: true})
	if err != nil || got != "${debugAdapterPort}" {
		t.Fatalf("preserved port = %q, %v", got, err)
	}
	if runtime.GOOS == "windows" && strings.Contains(text, "/tmp/workspace") {
		t.Fatalf("expansion unexpectedly changed host path semantics: %s", text)
	}
}

func TestSandboxExpansionUsesGuestPathSemantics(t *testing.T) {
	options := ExpandOptions{
		WorkspaceFolder: "/workspace/project",
		CurrentFile:     "/workspace/project/cmd/echo/main.go",
		SlashPaths:      true,
	}
	for input, expected := range map[string]string{
		"${fileDirname}":             "/workspace/project/cmd/echo",
		"${fileBasename}":            "main.go",
		"${fileBasenameNoExtension}": "main",
		"${fileExtname}":             ".go",
		"${relativeFile}":            "cmd/echo/main.go",
		"${pathSeparator}":           "/",
	} {
		actual, err := ExpandString(input, options)
		if err != nil || actual != expected {
			t.Fatalf("ExpandString(%q) = %q, %v; want %q", input, actual, err, expected)
		}
	}
}
