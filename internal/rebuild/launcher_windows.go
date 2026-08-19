//go:build windows

package rebuild

import (
	"fmt"
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

func launchDetached(scriptPath string) error {
	powerShell, err := exec.LookPath("pwsh.exe")
	if err != nil {
		powerShell, err = exec.LookPath("powershell.exe")
		if err != nil {
			return fmt.Errorf("PowerShell was not found: %w", err)
		}
	}
	command := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start detached PowerShell launcher: %w", err)
	}
	_ = command.Process.Release()
	return nil
}
