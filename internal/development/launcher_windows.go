//go:build windows

package development

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	createNoWindow        = 0x08000000
	createNewProcessGroup = 0x00000200
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
		return fmt.Errorf("start detached PowerShell launcher %s: %w; see %s", powerShell, err, diagnosticPath)
	}
	_ = diagnostic.Close()
	return waitForLauncherReadiness(command, launcherReadyTimeout)
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
