//go:build !windows

package terminal

import (
	"bufio"
	"context"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRealPTYStartsWithControllingTerminalAndProcessGroup(t *testing.T) {
	backend, err := newRealBackend()
	if err != nil {
		t.Fatalf("create PTY: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.Resize(80, 24); err != nil {
		t.Fatalf("resize PTY: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := backend.Start(ctx, CommandSpec{
		Name: "/bin/sh",
		Args: []string{"-c", "test -t 0 && test -t 1 && test -t 2 && : </dev/tty || exit 1; printf 'PTY_READY\\n'; read -r line; [ \"$line\" = done ] || exit 2; printf 'PTY_DONE\\n'; exit 7"},
		Dir:  t.TempDir(),
		Env:  terminalEnvironment(),
	})
	if err != nil {
		t.Fatalf("start real PTY shell: %v", err)
	}
	finished := make(chan int, 1)
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		code, _ := process.Wait()
		finished <- code
	}()
	output := make(chan string, 1)
	inputAcknowledged := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := bufio.NewScanner(backend)
		if !reader.Scan() {
			output <- ""
			return
		}
		output <- reader.Text()
		// Keep draining echoed input and shell output while Wait runs. On
		// macOS, closing the controlling terminal can wait for output to drain.
		for reader.Scan() {
			if strings.TrimSpace(reader.Text()) == "PTY_DONE" {
				close(inputAcknowledged)
			}
		}
	}()
	t.Cleanup(func() {
		_ = process.Kill()
		_ = backend.Close()
		for name, done := range map[string]<-chan struct{}{"process": waitDone, "reader": readerDone} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("PTY %s did not stop during cleanup", name)
			}
		}
	})

	pid := process.(*realProcess).PID()
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		t.Fatalf("PTY process group = %d, %v; want PID %d for group cancellation", pgid, err, pid)
	}
	select {
	case line := <-output:
		if strings.TrimSpace(line) != "PTY_READY" {
			t.Fatalf("PTY output = %q, want PTY_READY", line)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for PTY output")
	}
	if _, err := backend.Write([]byte("done\n")); err != nil {
		t.Fatalf("write PTY input: %v", err)
	}
	select {
	case <-inputAcknowledged:
	case <-ctx.Done():
		t.Fatal("timed out waiting for PTY output after input")
	}
	select {
	case code := <-finished:
		if code != 7 {
			t.Fatalf("shell exit code = %d, want 7", code)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for shell exit")
	}
}
