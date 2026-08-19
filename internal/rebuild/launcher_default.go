//go:build !windows

package rebuild

import (
	"fmt"
	"os/exec"
	"syscall"
)

func launchDetached(scriptPath string) error {
	command := exec.Command("sh", scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return fmt.Errorf("start detached shell launcher: %w", err)
	}
	_ = command.Process.Release()
	return nil
}
