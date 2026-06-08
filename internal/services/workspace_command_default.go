//go:build !windows

package services

import "os/exec"

func configureWorkspaceCommandProcess(command *exec.Cmd) {
}
