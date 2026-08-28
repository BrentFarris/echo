//go:build !windows

package sandbox

import "os"

func sandboxHostUID() int {
	uid := os.Getuid()
	if uid <= 0 {
		return 1000
	}
	return uid
}
