//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008 // DETACHED_PROCESS only; CREATE_BREAKAWAY_FROM_JOB fails under job objects (e.g. Wails dev)

func launchDetachedRestart(scriptPath string) error {
	pwsh, err := exec.LookPath("pwsh.exe")
	if err != nil {
		pwsh, err = exec.LookPath("powershell.exe")
		if err != nil {
			return &SafeError{Code: "shell_not_found", Message: "PowerShell was not found"}
		}
	}

	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess,
	}

	// Start (not Run) so we detach immediately.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start PowerShell process: %w", err)
	}

	// Release resources without waiting for exit.
	_ = cmd.Process.Release()
	return nil
}
