//go:build !windows

package development

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func launchDetached(scriptPath string) error {
	command := exec.Command("sh", scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Dir = filepath.Dir(scriptPath)
	command.Stdin = nil
	diagnostic, err := os.OpenFile(filepath.Join(command.Dir, "rebuild-launcher.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer diagnostic.Close()
	command.Stdout = diagnostic
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		return fmt.Errorf("start detached shell launcher: %w", err)
	}
	return waitForLauncherReadiness(command, launcherReadyTimeout)
}
