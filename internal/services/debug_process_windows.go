//go:build windows

package services

import (
	"os/exec"
)

func configureDebugAdapterProcess(cmd *exec.Cmd) {
	configureWorkspaceCommandProcess(cmd)
}
