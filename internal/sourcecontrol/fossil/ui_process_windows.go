//go:build windows

package fossil

import (
	"os/exec"
	"syscall"
)

func configureFossilUICommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
