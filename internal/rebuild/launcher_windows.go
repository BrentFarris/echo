//go:build windows

package rebuild

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	createNoWindow        = 0x08000000
	createNewProcessGroup = 0x00000200
	launcherReadyTimeout  = 5 * time.Second
)

func launchDetached(scriptPath string) error {
	powerShell, err := findPowerShell()
	if err != nil {
		return err
	}
	command := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	command.Dir = filepath.Dir(scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | createNewProcessGroup, HideWindow: true}

	diagnosticPath := filepath.Join(filepath.Dir(scriptPath), "rebuild-launcher.log")
	diagnostic, err := os.OpenFile(diagnosticPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open detached launcher log: %w", err)
	}
	command.Stdout = diagnostic
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		_ = diagnostic.Close()
		return fmt.Errorf("start detached PowerShell launcher: %w", err)
	}
	_ = diagnostic.Close()

	readyPath := filepath.Join(filepath.Dir(scriptPath), "rebuild-relaunch.ready")
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	deadline := time.NewTimer(launcherReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = command.Process.Kill()
			return fmt.Errorf("check detached launcher readiness: %w", err)
		}
		select {
		case err := <-exited:
			if _, statErr := os.Stat(readyPath); statErr == nil {
				return nil
			}
			if err == nil {
				err = errors.New("launcher exited without reporting readiness")
			}
			return fmt.Errorf("detached PowerShell launcher exited before readiness: %w; see %s", err, diagnosticPath)
		case <-deadline.C:
			_ = command.Process.Kill()
			return fmt.Errorf("detached PowerShell launcher did not report readiness within %s; see %s", launcherReadyTimeout, diagnosticPath)
		case <-ticker.C:
		}
	}
}

func findPowerShell() (string, error) {
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		candidate := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if powerShell, err := exec.LookPath("powershell.exe"); err == nil {
		return powerShell, nil
	}
	if powerShell, err := exec.LookPath("pwsh.exe"); err == nil {
		return powerShell, nil
	}
	return "", errors.New("PowerShell was not found")
}
