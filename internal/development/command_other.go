//go:build !windows

package development

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
}

func findPowerShell() (string, error) {
	return "", errors.New("development builds require Windows PowerShell")
}
