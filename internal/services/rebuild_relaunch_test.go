package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRebuildAndRelaunchValidWorkspace(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	// Create workspace directory with wails.json
	workspaceDir := filepath.Join(root, "echo-source")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "wails.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := service.AddWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	// Verify script was written to temp dir
	err = service.PrepareRebuildAndRelaunch(workspaceID)
	if err != nil {
		t.Fatalf("expected no error for valid workspace, got %v", err)
	}

	// Check that the relaunch script was created
	scriptDir := os.TempDir()
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		scriptDir = filepath.Join(localAppData, "Echo")
	}
	expectedScript := filepath.Join(scriptDir, "rebuild-relaunch.ps1")
	data, err := os.ReadFile(expectedScript)
	if err != nil {
		t.Fatalf("expected relaunch script to exist at %s, got %v", expectedScript, err)
	}
	content := string(data)
	if !strings.Contains(content, workspaceDir) {
		t.Fatalf("script should contain workspace path, got %s", content)
	}
	if !strings.Contains(content, "wails build") {
		t.Fatalf("script should contain wails build command, got %s", content)
	}
	if !strings.Contains(content, "Start-Process") {
		t.Fatalf("script should contain Start-Process for relaunch, got %s", content)
	}
	if !strings.Contains(content, "Stop-Process") {
		t.Fatalf("script should contain Stop-Process for force-kill, got %s", content)
	}
}

func TestPrepareRebuildAndRelaunchMissingWailsJson(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	// Create workspace directory WITHOUT wails.json
	workspaceDir := filepath.Join(root, "other-project")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state, err := service.AddWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	err = service.PrepareRebuildAndRelaunch(workspaceID)
	if err == nil {
		t.Fatal("expected error when workspace lacks wails.json")
	}
	if !strings.Contains(err.Error(), "wails.json") {
		t.Fatalf("expected wails.json error, got %v", err)
	}
}

func TestPrepareRebuildAndRelaunchEmptyWorkspaceID(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	err := service.PrepareRebuildAndRelaunch("")
	if err == nil {
		t.Fatal("expected error for empty workspace ID")
	}
	if !strings.Contains(err.Error(), "workspace id is required") {
		t.Fatalf("expected 'workspace id is required' error, got %v", err)
	}
}

func TestPrepareRebuildAndRelaunchWhitespaceOnlyWorkspaceID(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	err := service.PrepareRebuildAndRelaunch("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only workspace ID")
	}
	if !strings.Contains(err.Error(), "workspace id is required") {
		t.Fatalf("expected 'workspace id is required' error, got %v", err)
	}
}

func TestPrepareRebuildAndRelaunchNonexistentWorkspace(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	err := service.PrepareRebuildAndRelaunch("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent workspace")
	}
	if !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("expected 'was not found' error, got %v", err)
	}
}
