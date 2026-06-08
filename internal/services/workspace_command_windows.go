//go:build windows

package services

import (
	"os/exec"
	"syscall"
)

const windowsCreateNoWindow = 0x08000000

func configureWorkspaceCommandProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= windowsCreateNoWindow
}
