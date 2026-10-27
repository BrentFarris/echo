//go:build linux || darwin

package development

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeUnixLauncherWaitsAndReplaces(t *testing.T) {
	dir := t.TempDir()
	previous := exec.Command("sleep", "30")
	if err := previous.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = previous.Process.Kill(); _ = previous.Wait() })
	binary, staged := filepath.Join(dir, "echo"), filepath.Join(dir, "echo staged")
	if err := os.WriteFile(binary, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > received\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := launchSpec{ProcessID: previous.Process.Pid, BinaryPath: binary, StagedPath: staged, WorkingDir: dir, Arguments: []string{"space path", "literal'quote", "$(no-execution)"}, LogPath: filepath.Join(dir, "launcher.log"), WaitSeconds: 10}
	if err := prepareAndLaunch(spec, dir); err != nil {
		t.Fatal(err)
	}
	if old, _ := os.ReadFile(binary); string(old) != "old" {
		t.Fatal("replaced executable before host exited")
	}
	if err := previous.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = previous.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, "received"))
		if err == nil {
			if string(b) != strings.Join(spec.Arguments, "\n")+"\n" {
				t.Fatalf("arguments changed: %q", b)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replacement did not launch")
}
