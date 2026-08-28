//go:build !windows

package sandbox

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
