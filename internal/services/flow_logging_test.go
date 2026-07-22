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
	withWorkingDirectory(t, root)
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	if status := service.LoadDevelopmentLogStatus(); status.Enabled || status.Path != developmentLogDisplayPath {
		t.Fatalf("unexpected initial status: %#v", status)
	}

	status, err := service.SetDevelopmentLoggingEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled {
		t.Fatal("expected logging to be enabled")
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
	firstLog, err := os.ReadFile(filepath.Join(root, "echo", "echo.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstLog), "old_capture_marker") || !strings.Contains(string(firstLog), "exact-media") {
		t.Fatalf("first capture is incomplete: %s", firstLog)
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
	logData, err := os.ReadFile(filepath.Join(root, "echo", "echo.log"))
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
	withWorkingDirectory(t, root)
	if err := os.WriteFile(filepath.Join(root, "echo"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	defer service.Shutdown()
	if _, err := service.SetDevelopmentLoggingEnabled(true); err == nil {
		t.Fatal("expected enable error")
	}
	if service.LoadDevelopmentLogStatus().Enabled {
		t.Fatal("status should remain disabled after an enable failure")
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
