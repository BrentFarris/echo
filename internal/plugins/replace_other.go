//go:build !windows

package plugins

import "os"

func replaceAtomic(source, destination string) error {
	return os.Rename(source, destination)
}
