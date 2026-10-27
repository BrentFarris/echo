package development

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const launcherReadyTimeout = 30 * time.Second

func waitForLauncherReadiness(command *exec.Cmd, timeout time.Duration) error {
	readyPath := filepath.Join(command.Dir, "rebuild-relaunch.ready")
	diagnosticPath := filepath.Join(command.Dir, "rebuild-launcher.log")
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	stop := func() error {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop detached launcher: %w", err)
		}
		<-exited
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.Join(fmt.Errorf("check detached launcher %s readiness: %w; see %s", command.Path, err, diagnosticPath), stop())
		}
		select {
		case err := <-exited:
			if _, statErr := os.Stat(readyPath); statErr == nil {
				return nil
			}
			if err == nil {
				err = errors.New("launcher exited without reporting readiness")
			}
			return fmt.Errorf("detached PowerShell launcher exited before readiness (%s): %w; see %s", command.Path, err, diagnosticPath)
		case <-deadline.C:
			if _, err := os.Stat(readyPath); err == nil {
				return nil
			}
			return errors.Join(fmt.Errorf("detached PowerShell launcher %s did not report readiness within %s; see %s", command.Path, timeout, diagnosticPath), stop())
		case <-ticker.C:
		}
	}
}
