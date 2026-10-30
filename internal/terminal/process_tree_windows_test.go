//go:build windows

package terminal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNativeTerminalStopTerminatesChildProcess(t *testing.T) {
	s, bot, _ := newTestService(t)
	pidFile := filepath.Join(bot.Roots[0], "child.pid")
	snapshot, err := s.Start(bot.ID, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	// This child is deliberately idle. A successful stop must reap it along
	// with the interactive shell, rather than merely detaching the browser.
	command := "$child = Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList '-NoLogo','-NoProfile','-Command','Start-Sleep -Seconds 60' -PassThru -WindowStyle Hidden; [IO.File]::WriteAllText('" + strings.ReplaceAll(pidFile, "'", "''") + "', [string]$child.Id)\r"
	if err = s.Write(bot.ID, snapshot.ID, command); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child did not start")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	t.Cleanup(func() { _ = windows.TerminateProcess(process, 1) })
	if err = s.Stop(bot.ID, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	status, err := windows.WaitForSingleObject(process, 5000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child survived terminal stop: wait=%d err=%v", status, err)
	}
}
