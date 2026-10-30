package p4

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

func outputFileArgument(directory, name string) (string, error) {
	argument := relativeOutputPath(directory, name)
	ascii := func(value string) bool {
		for _, character := range value {
			if character > 127 {
				return false
			}
		}
		return true
	}
	if ascii(argument) {
		return argument, nil
	}
	// Older Windows P4 clients use ANSI argv. An existing temporary file can be
	// addressed by its short path without ever reconstructing a depot filename.
	input, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(input, &buffer[0], uint32(len(buffer)))
	if err == nil && n > 0 && n < uint32(len(buffer)) {
		argument = windows.UTF16ToString(buffer[:n])
		if ascii(argument) {
			return argument, nil
		}
	}
	return "", errors.New("P4 2024.1 cannot address the temporary baseline path losslessly; configure an ASCII TEMP directory")
}
