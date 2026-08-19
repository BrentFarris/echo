//go:build windows

package rebuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
