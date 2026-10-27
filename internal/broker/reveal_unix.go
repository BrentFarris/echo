//go:build linux || darwin

package broker

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func RevealInFileBrowser(target RevealTarget) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		args := []string{target.Path}
		if target.Select {
			args = append([]string{"-R"}, args...)
		}
		return exec.CommandContext(ctx, "/usr/bin/open", args...).Run()
	}
	path := target.Path
	if target.Select {
		path = filepath.Dir(path)
	}
	return exec.CommandContext(ctx, "xdg-open", path).Run()
}
