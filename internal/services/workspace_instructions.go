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
	return fmt.Sprintf("Operating context:\n- Operating system: %s\n- Default shell: %s\n- Shell command guidance: %s\n- OS user: %s\n- Workspace: %s\n- Current time: %s",
		runtime.GOOS,
		defaultShellDescription(),
		shellCommandGuidance(),
		currentOSUser(),
		workspace.FolderPath,
		time.Now().Format(time.RFC3339),
	)
}

func defaultShellDescription() string {
	if runtime.GOOS == "windows" {
		return "PowerShell (pwsh.exe when available, otherwise powershell.exe)"
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func shellCommandGuidance() string {
	if runtime.GOOS == "windows" {
		return "shell_command runs through PowerShell; use PowerShell-native commands such as Select-String instead of assuming Unix utilities like grep are installed."
	}
	return "shell_command runs through $SHELL when set, otherwise /bin/sh; use POSIX sh-compatible commands unless workspace tooling requires otherwise."
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
