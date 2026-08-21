//go:build windows

package plugins

import (
	"os"

	"golang.org/x/sys/windows"
)

func replaceAtomic(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			return err
		}
		return os.Rename(source, destination)
	}
	return nil
}
