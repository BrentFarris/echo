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
	if !defaults.CodeLens || defaults.Timeout != "30s" {
		t.Fatalf("defaults = %#v", defaults)
	}
	if err := json.Unmarshal([]byte(`{"go":{"codeLens":false}}`), &workspace); err != nil {
		t.Fatal(err)
	}
	disabled := workspace.Normalized().Go
	if disabled.CodeLens || disabled.Timeout != "30s" {
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
