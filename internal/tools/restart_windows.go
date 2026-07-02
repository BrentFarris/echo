//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008 | 0x01000000 // DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB

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
		return err
	}

	// Release resources without waiting for exit.
	_ = cmd.Process.Release()
	return nil
}
