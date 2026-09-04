//go:build !windows

package checkpoint

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
