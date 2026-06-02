package services

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	workspaceInstructionsFile     = "AGENTS.md"
	maxWorkspaceInstructionsBytes = 64 * 1024
)

func workspaceSystemPrompt(base string, workspace Workspace) string {
	content := strings.TrimSpace(base) + "\n\n" + workspaceOperatingContext(workspace)
	instructions := workspaceInstructions(workspace)
	if instructions == "" {
		return content
	}
	return content + "\n\nWorkspace instructions from AGENTS.md:\n\n" + instructions
}

func workspaceOperatingContext(workspace Workspace) string {
	return fmt.Sprintf("Operating context:\n- Operating system: %s\n- OS user: %s\n- Workspace: %s\n- Current time: %s",
		runtime.GOOS,
		currentOSUser(),
		workspace.FolderPath,
		time.Now().Format(time.RFC3339),
	)
}

func currentOSUser() string {
	if current, err := user.Current(); err == nil {
		if username := strings.TrimSpace(current.Username); username != "" {
			return username
		}
	}
	for _, key := range []string{"USER", "USERNAME"} {
		if username := strings.TrimSpace(os.Getenv(key)); username != "" {
			return username
		}
	}
	return "unknown"
}

func workspaceInstructions(workspace Workspace) string {
	path := filepath.Join(workspace.FolderPath, workspaceInstructionsFile)
	data, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(data) {
		return ""
	}

	truncated := false
	if len(data) > maxWorkspaceInstructionsBytes {
		data = data[:maxWorkspaceInstructionsBytes]
		truncated = true
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	if truncated {
		content += fmt.Sprintf("\n\n[AGENTS.md truncated after %d bytes.]", maxWorkspaceInstructionsBytes)
	}
	return content
}
