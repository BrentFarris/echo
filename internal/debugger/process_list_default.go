//go:build !windows

package debugger

import (
	"context"
	"os/exec"
)

func hostProcessList(ctx context.Context) ([]ProcessInfo, error) {
	output, err := exec.CommandContext(ctx, "ps", "-eo", "pid=,comm=,args=").Output()
	if err != nil {
		return nil, err
	}
	return parseProcessList(string(output), "host"), nil
}
