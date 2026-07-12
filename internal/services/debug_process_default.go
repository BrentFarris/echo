//go:build !windows

package services

import "os/exec"

func configureDebugAdapterProcess(_ *exec.Cmd) {}
