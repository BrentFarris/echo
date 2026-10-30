//go:build !windows

package p4

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}

func outputFileArgument(directory, name string) (string, error) {
	return relativeOutputPath(directory, name), nil
}
