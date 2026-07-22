package services

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestDevelopmentLoggingIsTransientAndTruncatesOnEnable(t *testing.T) {
	root := t.TempDir()
	processRoot := filepath.Join(root, "process")
	workspaceRoot := filepath.Join(root, "workspace")
	if err := os.MkdirAll(processRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	withWorkingDirectory(t, processRoot)
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	if _, err := service.AddWorkspace(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workspaceRoot, workspaceCacheDirName, "echo.log")
	if status := service.LoadDevelopmentLogStatus(); status.Enabled || !sameCleanPath(status.Path, logPath) {
		t.Fatalf("unexpected initial status: %#v", status)
	}

	status, err := service.SetDevelopmentLoggingEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled {
		t.Fatal("expected logging to be enabled")
	}
	if !sameCleanPath(status.Path, logPath) {
		t.Fatalf("expected workspace log path %q, got %q", logPath, status.Path)
	}
	service.logAIEvent(slog.LevelInfo, "old_capture_marker")
	call := llm.ToolCall{ID: "call-1", Function: llm.FunctionCall{Name: "example"}}
	service.logModelFacingToolResult(call, []llm.Message{{
		Role:         llm.RoleUser,
		Content:      "attached image",
		ContentParts: []llm.MessageContentPart{llm.ImageURLContentPart("data:image/png;base64,exact-media")},
	}})
	if _, err := service.SaveSettings(service.LoadState().Settings); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDevelopmentLoggingEnabled(false); err != nil {
		t.Fatal(err)
	}
	firstLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstLog), "old_capture_marker") || !strings.Contains(string(firstLog), "exact-media") {
		t.Fatalf("first capture is incomplete: %s", firstLog)
	}
	if _, err := os.Stat(filepath.Join(processRoot, "echo", "echo.log")); !os.IsNotExist(err) {
		t.Fatalf("development log should not be written relative to the process: %v", err)
	}

	stateData, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateData), "developmentLog") || strings.Contains(string(stateData), "flowLog") {
		t.Fatalf("development logging leaked into persisted state: %s", stateData)
	}

	if _, err := service.SetDevelopmentLoggingEnabled(true); err != nil {
		t.Fatal(err)
	}
	service.logAIEvent(slog.LevelInfo, "new_capture_marker")
	if _, err := service.SetDevelopmentLoggingEnabled(false); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), "old_capture_marker") || strings.Contains(string(logData), "exact-media") {
		t.Fatalf("log was not truncated: %s", logData)
	}
	if !strings.Contains(string(logData), "new_capture_marker") {
		t.Fatalf("new capture missing: %s", logData)
	}
	service.Shutdown()

	reloaded := NewSystemServiceWithStorePath(storePath)
	defer reloaded.Shutdown()
	if reloaded.LoadDevelopmentLogStatus().Enabled {
		t.Fatal("development logging should not persist across service instances")
	}
}

func TestDevelopmentLoggingEnableFailureLeavesStatusDisabled(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	defer service.Shutdown()
	if _, err := service.AddWorkspace(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, workspaceCacheDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDevelopmentLoggingEnabled(true); err == nil {
		t.Fatal("expected enable error")
	}
	if service.LoadDevelopmentLogStatus().Enabled {
		t.Fatal("status should remain disabled after an enable failure")
	}
}

func TestDevelopmentLoggingRequiresActiveWorkspace(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	defer service.Shutdown()

	if _, err := service.SetDevelopmentLoggingEnabled(true); err == nil {
		t.Fatal("expected enabling without an active workspace to fail")
	}
	status := service.LoadDevelopmentLogStatus()
	if status.Enabled || status.Path != developmentLogDisplayPath {
		t.Fatalf("unexpected status without an active workspace: %#v", status)
	}
}

func TestDevelopmentLoggingUsesFirstWorkspaceFolder(t *testing.T) {
	root := t.TempDir()
	firstRoot := filepath.Join(root, "first")
	secondRoot := filepath.Join(root, "second")
	if err := os.MkdirAll(firstRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	defer service.Shutdown()
	state, err := service.AddWorkspace(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddWorkspaceFolder(state.ActiveWorkspaceID, secondRoot); err != nil {
		t.Fatal(err)
	}

	status, err := service.SetDevelopmentLoggingEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(firstRoot, workspaceCacheDirName, "echo.log")
	if !sameCleanPath(status.Path, expected) {
		t.Fatalf("expected first-folder log path %q, got %q", expected, status.Path)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secondRoot, workspaceCacheDirName, "echo.log")); !os.IsNotExist(err) {
		t.Fatalf("second workspace folder should not contain the development log: %v", err)
	}
}

func withWorkingDirectory(t *testing.T, path string) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(original)
	})
}
