//go:build windows

package store

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func lockDirectory(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("data directory is already in use or cannot be locked: %w", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}
