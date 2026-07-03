//go:build windows

package services

import (
	"fmt"
	"os/exec"
	"syscall"
)

func launchDetachedRebuild(scriptPath string) error {
	// Use "cmd /c start" to spawn PowerShell as a grandchild of cmd.exe.
	// This creates a fully independent process tree that survives when Echo
	// calls runtime.Quit() and exits. Using DETACHED_PROCESS +
	// CREATE_BREAKAWAY_FROM_JOB directly on the PowerShell process does not
	// reliably survive parent termination because the job object cleanup can
	// still kill the child before it has a chance to break away.
	cmd := exec.Command("cmd", "/c", "start", "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start rebuild process: %w", err)
	}

	_ = cmd.Process.Release()
	return nil
}
