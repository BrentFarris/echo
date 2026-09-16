//go:build windows

package rebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLaunchDetachedWaitsForReadiness(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "rebuild-relaunch.ready")
	scriptPath := filepath.Join(dir, "rebuild-relaunch.ps1")
	script := "Set-Content -LiteralPath '" + quotePowerShell(readyPath) + "' -Value $PID -Encoding ascii\r\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := launchDetached(scriptPath); err != nil {
		diagnostic, _ := os.ReadFile(filepath.Join(dir, "rebuild-launcher.log"))
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("launchDetached: %v; diagnostic = %q; files = %v", err, diagnostic, names)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("readiness marker: %v", err)
	}
}

func TestLaunchDetachedReportsImmediateFailure(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "rebuild-relaunch.ps1")
	if err := os.WriteFile(scriptPath, []byte("exit 9\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := launchDetached(scriptPath)
	if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("launchDetached error = %v", err)
	}
}

func TestLauncherReadinessWaitsForDelayedMarker(t *testing.T) {
	started := time.Now()
	command := startLauncherReadinessProcess(t, "delayed")
	if err := waitForLauncherReadiness(command, launcherReadyTimeout); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 150*time.Millisecond {
		t.Fatal("launcher was accepted before its delayed readiness marker")
	}
	if _, err := os.Stat(filepath.Join(command.Dir, "rebuild-relaunch.ready")); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherReadinessTimeoutKillsAndReapsProcess(t *testing.T) {
	command := startLauncherReadinessProcess(t, "timeout")
	err := waitForLauncherReadiness(command, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not report readiness within 100ms") ||
		!strings.Contains(err.Error(), command.Path) || !strings.Contains(err.Error(), "rebuild-launcher.log") {
		t.Fatalf("timeout error = %v", err)
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatal("timed-out launcher was not killed and reaped before returning")
	}
	if _, err := os.Stat(filepath.Join(command.Dir, "rebuild-relaunch.ready")); !os.IsNotExist(err) {
		t.Fatalf("timed-out launcher wrote a readiness marker: %v", err)
	}
}

func startLauncherReadinessProcess(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestLauncherReadinessProcess$")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "ECHO_LAUNCHER_TEST_PROCESS="+mode)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | createNewProcessGroup, HideWindow: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	return command
}

func TestLauncherReadinessProcess(t *testing.T) {
	switch os.Getenv("ECHO_LAUNCHER_TEST_PROCESS") {
	case "delayed":
		time.Sleep(150 * time.Millisecond)
		if err := os.WriteFile("rebuild-relaunch.ready", []byte("ready"), 0o600); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "timeout":
		time.Sleep(time.Minute)
		os.Exit(1)
	}
}
