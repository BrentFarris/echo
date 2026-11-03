//go:build !windows

package store

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func lockDirectory(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("data directory is already in use: %w", e)
	}
	return f, nil
}
