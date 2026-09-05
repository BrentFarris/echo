//go:build !windows

package terminal

import (
	"errors"
	"os"
	"syscall"

	ptylib "github.com/aymanbagabas/go-pty"
)

func configurePTYCommand(command *ptylib.Cmd) {
	// go-pty sets Setsid and Setctty, which also give the child its own
	// process group. Adding Setpgid makes startup fail with EPERM on Unix.
	command.Cancel = func() error { return killPTYCommand(command) }
}

func killPTYCommand(command *ptylib.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
