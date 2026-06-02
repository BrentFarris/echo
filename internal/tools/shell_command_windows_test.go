//go:build windows

package tools

import (
	"os/exec"
	"testing"
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
}
