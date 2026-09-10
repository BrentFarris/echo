//go:build !windows

package fossil

import "os/exec"

func configureFossilUICommand(*exec.Cmd) {}
