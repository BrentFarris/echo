//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

func launchDetachedRestart(scriptPath string) error {
	cmd := exec.Command("sh", "-c", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Detach from parent process group
	}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return err
	}

	_ = cmd.Process.Release()
	return nil
}
