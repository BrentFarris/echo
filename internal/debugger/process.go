package debugger

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// StartClientAdapter owns the adapter tree and a private, ephemeral loopback
// connection. Successful startup transfers lifetime ownership to Adapter.Stop;
// cancellation must not kill an attached target before DAP can detach it.
func StartClientAdapter(ctx context.Context, executable, directory string, environment []string, args func(string) []string, output func(string, string)) (*Adapter, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	command := exec.CommandContext(context.Background(), executable, args(listener.Addr().String())...)
	configureOwnedCommand(command)
	command.Dir = directory
	command.Env = environment
	command.Stdout = outputWriter{category: "stdout", write: output}
	command.Stderr = outputWriter{category: "stderr", write: output}
	if err = command.Start(); err != nil {
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = command.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			select {
			case <-done:
				return
			default:
			}
			_ = killOwnedCommand(command)
			<-done
		})
	}
	deadline := time.AfterFunc(20*time.Second, func() { listener.Close() })
	defer deadline.Stop()
	cancel := context.AfterFunc(ctx, func() { listener.Close() })
	defer cancel()
	go func() { <-done; listener.Close() }()
	conn, err := listener.Accept()
	if err != nil {
		stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("debug adapter did not connect: %w", err)
	}
	return &Adapter{Transport: conn, Stop: func() { conn.Close(); stop() }}, nil
}

type outputWriter struct {
	category string
	write    func(string, string)
}

func (w outputWriter) Write(p []byte) (int, error) {
	if w.write != nil {
		for offset := 0; offset < len(p); offset += 16384 {
			end := offset + 16384
			if end > len(p) {
				end = len(p)
			}
			w.write(w.category, string(p[offset:end]))
		}
	}
	return len(p), nil
}
func Environment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		for i, c := range item {
			if c == '=' {
				key := item[:i]
				if runtime.GOOS == "windows" {
					key = strings.ToUpper(key)
				}
				values[key] = item[i+1:]
				break
			}
		}
	}
	for k, v := range overrides {
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		values[k] = v
	}
	result := make([]string, 0, len(values))
	for k, v := range values {
		result = append(result, k+"="+v)
	}
	return result
}

// StartTerminalAdapter connects to an adapter whose stdio belongs to a
// debugger-owned terminal. Its transport remains private host-local TCP.
func StartTerminalAdapter(ctx context.Context, scope LaunchContext, executable, directory string, environment map[string]string, args func(string) []string) (*Adapter, error) {
	if scope.RunInTerminal == nil || scope.StopTerminals == nil {
		return nil, fmt.Errorf("integrated debug terminals are unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stop := context.AfterFunc(startup, func() { listener.Close() })
	defer stop()
	argv := []any{executable}
	for _, arg := range args(listener.Addr().String()) {
		argv = append(argv, arg)
	}
	env := map[string]any{}
	for key, value := range environment {
		env[key] = value
	}
	if _, err = scope.RunInTerminal(startup, map[string]any{"kind": "integrated", "title": "Debug Terminal", "args": argv, "cwd": directory, "env": env}); err != nil {
		return nil, err
	}
	conn, err := listener.Accept()
	if err != nil {
		scope.StopTerminals()
		return nil, fmt.Errorf("debug terminal adapter did not connect: %w", err)
	}
	return &Adapter{Transport: conn, Stop: func() { conn.Close(); scope.StopTerminals() }}, nil
}
