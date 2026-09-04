package debugconfig

import (
	"strings"
	"testing"
)

func TestPreviewVSCodeImportJSONCAndLifecycleTasks(t *testing.T) {
	launch := []byte(`{
		// The URL contains comment markers that must remain part of the string.
		"configurations": [
			{"name":"API", "type":"go", "request":"launch", "program":"${workspaceFolder}", "url":"https://example.test/a//b", "preLaunchTask":"build",},
			{"name":"Mystery", "type":"vendor-debug", "request":"attach",},
		],
		"compounds": [{"name":"Full Stack", "configurations":["API", "Mystery"],}],
		"inputs": [{"id":"token", "type":"promptString", "password":true, "description":"Token"}],
	}`)
	tasks := []byte(`{
		"tasks": [
			{"label":"build", "type":"process", "command":"go", "args":["build","./..."], "options":{"cwd":"${workspaceFolder}","env":{"MODE":"debug"}}},
			{"label":"watch", "type":"shell", "command":"npm run watch", "isBackground":true}
		]
	}`)
	profiles := make([]AdapterProfile, 0, len(Templates()))
	for _, template := range Templates() {
		profiles = append(profiles, template.Profile)
	}
	preview, err := PreviewVSCodeImport(launch, tasks, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Config.Configurations) != 2 || len(preview.Config.Compounds) != 1 {
		t.Fatalf("unexpected import: %#v", preview.Config)
	}
	api := preview.Config.Configurations[0]
	if api.AdapterProfileID != "delve" || api.PreLaunch == nil || api.PreLaunch.Command != "go" || len(api.PreLaunch.Args) != 2 {
		t.Fatalf("API mapping = %#v", api)
	}
	if api.Arguments["url"] != "https://example.test/a//b" {
		t.Fatalf("URL was changed by JSONC parser: %#v", api.Arguments["url"])
	}
	if preview.Config.Inputs[0].Type != "secret" || !preview.Config.Compounds[0].StopAll {
		t.Fatalf("input/compound defaults = %#v / %#v", preview.Config.Inputs, preview.Config.Compounds)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0].Code != "unknown_adapter" {
		t.Fatalf("warnings = %#v", preview.Warnings)
	}
}

func TestPreviewVSCodeImportFlagsUnsafeTaskAndCommandVariable(t *testing.T) {
	launch := []byte(`{"configurations":[{"name":"Node","type":"pwa-node","request":"launch","program":"${command:pick}","preLaunchTask":"watch"}]}`)
	tasks := []byte(`{"tasks":[{"label":"watch","type":"shell","command":"npm run watch","isBackground":true}]}`)
	profile, _ := TemplateByID("js-debug")
	preview, err := PreviewVSCodeImport(launch, tasks, []AdapterProfile{profile})
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(preview.Warnings))
	for _, warning := range preview.Warnings {
		codes = append(codes, warning.Code)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, "task_requires_manual_conversion") || !strings.Contains(joined, "command_variable_unsupported") {
		t.Fatalf("warnings = %#v", preview.Warnings)
	}
	if err := preview.Config.Validate([]AdapterProfile{profile}); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("unsafe preview should require correction before save: %v", err)
	}
}

func TestPreviewVSCodeImportMapsKnownProcessPicker(t *testing.T) {
	launch := []byte(`{"configurations":[{"name":"Attach","type":"python","request":"attach","processId":"${command:pickProcess}"}]}`)
	profile, _ := TemplateByID("debugpy")
	preview, err := PreviewVSCodeImport(launch, nil, []AdapterProfile{profile})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Config.Inputs) != 1 || preview.Config.Inputs[0].Type != "pickProcess" {
		t.Fatalf("inputs = %#v", preview.Config.Inputs)
	}
	want := "${input:" + preview.Config.Inputs[0].ID + "}"
	if got := preview.Config.Configurations[0].Arguments["processId"]; got != want {
		t.Fatalf("processId = %#v, want %q", got, want)
	}
	for _, warning := range preview.Warnings {
		if warning.Code == "command_variable_unsupported" {
			t.Fatalf("known process picker was left unsupported: %#v", preview.Warnings)
		}
	}
}
