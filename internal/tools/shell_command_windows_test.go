//go:build windows

package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestConfigureShellCommandProcessHidesWindowsShell(t *testing.T) {
	command := exec.Command("powershell.exe", "-NoProfile")

	configureShellCommandProcess(command)

	if command.SysProcAttr == nil {
		t.Fatal("expected Windows process attributes to be configured")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("expected shell command process window to be hidden")
	}
	if command.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW flag, got %#x", command.SysProcAttr.CreationFlags)
	}
	if command.Cancel == nil {
		t.Fatal("expected Windows process-tree cancellation to be configured")
	}
}

func TestShellCommandTimeoutTerminatesWindowsChildProcess(t *testing.T) {
	workspace := t.TempDir()
	started := time.Now()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"shell_command",
		mustJSON(t, map[string]any{
			"command":        `& powershell.exe -NoProfile -NonInteractive -Command 'Start-Sleep -Seconds 8; Write-Output completed-after-sleep'`,
			"timeoutSeconds": 1,
		}),
	)
	elapsed := time.Since(started)

	if !result.Success {
		t.Fatalf("timed-out command should return structured shell output: %#v", result)
	}
	output := result.Output.(shellCommandOutput)
	if !output.TimedOut {
		t.Fatalf("expected command to time out, got %#v", output)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("expected the child process tree to stop promptly, elapsed %s", elapsed)
	}
	if strings.Contains(output.Stdout, "completed-after-sleep") {
		t.Fatalf("expected sleeping child to be terminated, got %#v", output)
	}
}
