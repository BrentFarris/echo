//go:build windows

package development

import (
	"os/exec"
	"strconv"
	"syscall"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	command.Cancel = func() error {
		// Build scripts own descendants (npm, node, go); cancel that exact tree.
		kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if err := kill.Run(); err != nil {
			return command.Process.Kill()
		}
		return nil
	}
}
