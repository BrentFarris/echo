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
	return content + "\n\nWorkspace instructions:\n\n" + instructions
}

func workspaceOperatingContext(workspace Workspace) string {
	var folders strings.Builder
	if len(workspace.Folders) == 0 {
		folders.WriteString("\n- Workspace folders: none configured")
	} else {
		folders.WriteString("\n- Workspace folders:")
		for _, folder := range workspace.Folders {
			status := "available"
			if folder.Missing {
				status = "unavailable"
				if folder.Error != "" {
					status += " (" + folder.Error + ")"
				}
			}
			agents := "disabled"
			if folder.UseAgents {
				agents = "enabled"
			}
			folders.WriteString(fmt.Sprintf("\n  - %s: %s [%s, AGENTS.md %s]", folder.Label, folder.Path, status, agents))
		}
	}
	return fmt.Sprintf("Operating context:\n- Operating system: %s\n- Default shell: %s\n- Shell command guidance: %s\n- OS user: %s%s\n- Path convention: %s\n- Current time: %s",
		runtime.GOOS,
		defaultShellDescription(),
		shellCommandGuidance(),
		currentOSUser(),
		folders.String(),
		workspacePathConvention(workspace),
		time.Now().Format(time.RFC3339),
	)
}

func workspacePathConvention(workspace Workspace) string {
	for _, folder := range workspace.Folders {
		label := strings.TrimSpace(folder.Label)
		if label == "" {
			continue
		}
		return fmt.Sprintf("tool paths must be labeled workspace paths. Start every concrete file or directory path with one of the listed workspace folder labels. Example: use %s/frontend/src/main.ts, not frontend/src/main.ts. Use . only for the virtual workspace root or all workspace folders.", label)
	}
	return "tool paths must be labeled workspace paths like <folder-label>/path/to/file. Use . only for the virtual workspace root or all workspace folders."
}

func defaultShellDescription() string {
	if runtime.GOOS == "windows" {
		return "PowerShell (pwsh.exe when available, otherwise powershell.exe); do not use cmd.exe or CMD syntax"
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func shellCommandGuidance() string {
	if runtime.GOOS == "windows" {
		return "shell_command runs through PowerShell, not cmd.exe. Always write PowerShell-native commands and avoid CMD syntax such as dir /s, copy, del, type, set VAR=VALUE, and %VAR%. Use PowerShell cmdlets and forms such as Get-ChildItem, Copy-Item, Remove-Item, Get-Content, Select-String, and $env:VAR. Set the tool's workingDirectory argument instead of changing directories inside the command."
	}
	return "shell_command runs through $SHELL when set, otherwise /bin/sh; use POSIX sh-compatible commands unless workspace tooling requires otherwise. Set the tool's workingDirectory argument instead of changing directories inside the command."
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
	sections := make([]string, 0, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		if !folder.UseAgents || folder.Missing {
			continue
		}
		path := filepath.Join(folder.Path, workspaceInstructionsFile)
		data, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(data) {
			continue
		}

		truncated := false
		if len(data) > maxWorkspaceInstructionsBytes {
			data = data[:maxWorkspaceInstructionsBytes]
			truncated = true
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		if truncated {
			content += fmt.Sprintf("\n\n[AGENTS.md truncated after %d bytes.]", maxWorkspaceInstructionsBytes)
		}
		sections = append(sections, fmt.Sprintf("AGENTS.md from %s (%s):\n\n%s", folder.Label, path, content))
	}
	return strings.Join(sections, "\n\n")
}
