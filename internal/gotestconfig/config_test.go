package gotestconfig

import (
	"encoding/json"
	"testing"
)

func TestGoConfigDefaultsAndExplicitDisable(t *testing.T) {
	var workspace WorkspaceConfig
	if err := json.Unmarshal([]byte(`{}`), &workspace); err != nil {
		t.Fatal(err)
	}
	defaults := workspace.Normalized().Go
	if !defaults.CodeLens || !defaults.Coverage || defaults.Timeout != "30s" {
		t.Fatalf("defaults = %#v", defaults)
	}
	if err := json.Unmarshal([]byte(`{"go":{"codeLens":false,"coverage":false}}`), &workspace); err != nil {
		t.Fatal(err)
	}
	disabled := workspace.Normalized().Go
	if disabled.CodeLens || disabled.Coverage || disabled.Timeout != "30s" {
		t.Fatalf("explicit disable = %#v", disabled)
	}
}

func TestGoConfigValidation(t *testing.T) {
	config := DefaultGoConfig()
	config.Timeout = "-1s"
	if err := config.Validate(); err == nil {
		t.Fatal("negative timeout was accepted")
	}
	config = DefaultGoConfig()
	config.Environment = map[string]string{"BAD=KEY": "value"}
	if err := config.Validate(); err == nil {
		t.Fatal("invalid environment key was accepted")
	}
}

func TestCConfigDefaultsAndGoOnlyCompatibility(t *testing.T) {
	var workspace WorkspaceConfig
	if err := json.Unmarshal([]byte(`{"go":{"timeout":"5s"}}`), &workspace); err != nil {
		t.Fatal(err)
	}
	normalized := workspace.Normalized()
	if normalized.Go.Timeout != "5s" || !normalized.Go.CodeLens || !normalized.C.CodeLens || !normalized.C.Coverage || len(normalized.C.Targets) != 0 {
		t.Fatalf("normalized = %#v", normalized)
	}
	if err := json.Unmarshal([]byte(`{"c":{"codeLens":false,"coverage":false}}`), &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.Normalized().C.CodeLens || workspace.Normalized().C.Coverage {
		t.Fatalf("explicit C disable was not preserved: %#v", workspace.Normalized().C)
	}
}

func TestCConfigDefaultsAndValidation(t *testing.T) {
	config := CConfig{Targets: []CTarget{{
		ID: "Unit", Name: "Unit tests", Entry: CEntry{File: "${workspaceFolder}/tests.c", Function: "main"},
		Build: &Command{Command: "gcc"}, Executable: "${workspaceFolder}/tests.exe",
		SourceRoots: []string{"${workspaceFolder}/src"}, Coverage: CCoverage{Provider: "GCOV", ObjectRoots: []string{"${workspaceFolder}/build"}},
	}}}.Normalized()
	if config.Targets[0].ID != "unit" || config.Targets[0].Cwd != "${workspaceFolder}" || config.Targets[0].Timeout != "30s" || config.Targets[0].Build.Timeout != "5m" {
		t.Fatalf("defaults = %#v", config.Targets[0])
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	duplicate := config
	duplicate.Targets = append(duplicate.Targets, duplicate.Targets[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate target id was accepted")
	}
	config.Targets[0].Environment = map[string]string{"BAD": "${selectedText}"}
	if err := config.Validate(); err == nil {
		t.Fatal("dynamic environment variable was accepted")
	}
}
