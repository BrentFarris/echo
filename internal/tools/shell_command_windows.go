//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

const windowsCreateNoWindow = 0x08000000

func configureShellCommandProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windowsCreateNoWindow,
	}
}
