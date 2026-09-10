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
		Args: []string{"-c", "test -t 0 && test -t 1 && test -t 2 && : </dev/tty || exit 1; printf 'PTY_READY\\n'; read -r line; [ \"$line\" = done ] || exit 2; exit 7"},
		Dir:  t.TempDir(),
		Env:  terminalEnvironment(),
	})
	if err != nil {
		t.Fatalf("start real PTY shell: %v", err)
	}
	t.Cleanup(func() { _ = process.Kill() })
	finished := make(chan int, 1)
	go func() {
		code, _ := process.Wait()
		finished <- code
	}()

	pid := process.(*realProcess).PID()
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		t.Fatalf("PTY process group = %d, %v; want PID %d for group cancellation", pgid, err, pid)
	}
	output := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(backend).ReadString('\n')
		output <- line
	}()
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
	case code := <-finished:
		if code != 7 {
			t.Fatalf("shell exit code = %d, want 7", code)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for shell exit")
	}
}
