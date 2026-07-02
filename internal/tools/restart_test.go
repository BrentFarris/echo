package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRestartToolRequiresEchoWorkspace(t *testing.T) {
	tmpDir := t.TempDir()

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: tmpDir,
	}

	result := Execute(ctx, "restart", json.RawMessage("{}"))
	if result.Success {
		t.Fatalf("expected failure when workspace lacks wails.json, got success")
	}
	if result.Error == nil || result.Error.Code != "not_echo_workspace" {
		t.Fatalf("expected error code 'not_echo_workspace', got %v", result.Error)
	}
}

func TestRestartToolRequiresWorkspace(t *testing.T) {
	ctx := ExecutionContext{
		Context: context.Background(),
	}

	result := Execute(ctx, "restart", json.RawMessage("{}"))
	if result.Success {
		t.Fatalf("expected failure with no workspace, got success")
	}
	if result.Error == nil || result.Error.Code != "missing_workspace" {
		t.Fatalf("expected error code 'missing_workspace', got %v", result.Error)
	}
}

func TestRestartToolLaunchesDetachedProcess(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a minimal wails.json so the workspace check passes.
	wailsConfig := filepath.Join(tmpDir, "wails.json")
	if err := os.WriteFile(wailsConfig, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create wails.json: %v", err)
	}

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: tmpDir,
	}

	result := Execute(ctx, "restart", json.RawMessage("{}"))
	if !result.Success {
		if result.Error != nil {
			t.Fatalf("expected success, got error: code=%s message=%s", result.Error.Code, result.Error.Message)
		}
		t.Fatal("expected success")
	}

	output, ok := result.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", result.Output)
	}

	if output["status"] != "restarting" {
		t.Errorf("expected status 'restarting', got %v", output["status"])
	}

	if _, ok := output["message"].(string); !ok {
		t.Error("expected message string in output")
	}
	if _, ok := output["binaryPath"].(string); !ok {
		t.Error("expected binaryPath string in output")
	}
	if _, ok := output["workspaceDir"].(string); !ok {
		t.Error("expected workspaceDir string in output")
	}
}
