//go:build windows

package services

import (
	"os/exec"
	"testing"
)

func TestConfigureWorkspaceCommandProcessHidesWindowsConsole(t *testing.T) {
	command := exec.Command("git", "status")

	configureWorkspaceCommandProcess(command)

	if command.SysProcAttr == nil {
		t.Fatal("expected Windows process attributes to be configured")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("expected workspace command process window to be hidden")
	}
	if command.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW flag, got %#x", command.SysProcAttr.CreationFlags)
	}
}
