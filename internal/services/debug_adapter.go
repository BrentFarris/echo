package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	debugAdapterStartupTimeout = 15 * time.Second
	debugAdapterStopTimeout    = 3 * time.Second
	debugBinaryCleanupTimeout  = time.Second
	debugBinaryCleanupInterval = 50 * time.Millisecond
)

type debugAdapterOutput func(category string, output string)

type debugAdapter interface {
	Start(context.Context, map[string]any, debugAdapterOutput) (*debugAdapterHandle, error)
}

type debugAdapterHandle struct {
	transport io.ReadWriteCloser
	cmd       *exec.Cmd
	done      chan error
	cleanup   func()
	closeOnce sync.Once
}

func (h *debugAdapterHandle) stop() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		if h.transport != nil {
			_ = h.transport.Close()
		}
		if h.cmd == nil || h.cmd.Process == nil {
			if h.cleanup != nil {
				h.cleanup()
			}
			return
		}
		select {
		case <-h.done:
			return
		case <-time.After(debugAdapterStopTimeout):
			_ = h.cmd.Process.Kill()
			select {
			case <-h.done:
			case <-time.After(time.Second):
			}
		}
	})
}

type debugAdapterRegistry struct {
	adapters map[string]debugAdapter
}

func newDebugAdapterRegistry() *debugAdapterRegistry {
	r := &debugAdapterRegistry{adapters: make(map[string]debugAdapter)}
	r.register("go", delveDebugAdapter{})
	return r
}

func (r *debugAdapterRegistry) register(adapterType string, adapter debugAdapter) {
	adapterType = strings.ToLower(strings.TrimSpace(adapterType))
	if adapterType != "" && adapter != nil {
		r.adapters[adapterType] = adapter
	}
}

func (r *debugAdapterRegistry) adapter(adapterType string) (debugAdapter, error) {
	adapterType = strings.ToLower(strings.TrimSpace(adapterType))
	adapter := r.adapters[adapterType]
	if adapter == nil {
		return nil, fmt.Errorf("debug adapter type %q is not installed", adapterType)
	}
	return adapter, nil
}

type delveDebugAdapter struct{}

var delveListenAddressPattern = regexp.MustCompile(`(?i)(?:DAP server listening at:\s*)?((?:127\.0\.0\.1|localhost|\[::1\]):[0-9]+)`) //nolint:lll

func (delveDebugAdapter) Start(ctx context.Context, config map[string]any, output debugAdapterOutput) (*debugAdapterHandle, error) {
	dlvPath, err := exec.LookPath("dlv")
	if err != nil {
		return nil, fmt.Errorf("Go debugger was not found; install Delve and ensure dlv is on PATH")
	}
	startCtx, cancel := context.WithTimeout(ctx, debugAdapterStartupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dlvPath, "dap", "--listen=127.0.0.1:0")
	if dir := debugString(config, "dlvCwd"); dir != "" {
		cmd.Dir = dir
	} else if dir := debugString(config, "cwd"); dir != "" {
		cmd.Dir = dir
	}
	configureDebugAdapterProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Delve output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Delve errors: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Delve DAP server: %w", err)
	}

	var cleanup func()
	if debugString(config, "output") == "" {
		cleanup = newDelveDebugBinaryCleanup(cmd.Dir, output)
	}
	handle := &debugAdapterHandle{cmd: cmd, done: make(chan error, 1), cleanup: cleanup}
	processDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if handle.cleanup != nil {
			handle.cleanup()
		}
		handle.done <- err
		processDone <- err
	}()

	addresses := make(chan string, 1)
	var scanWG sync.WaitGroup
	scan := func(category string, reader io.Reader) {
		defer scanWG.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			if output != nil {
				output(category, line)
			}
			if match := delveListenAddressPattern.FindStringSubmatch(line); len(match) == 2 {
				select {
				case addresses <- match[1]:
				default:
				}
			}
		}
	}
	scanWG.Add(2)
	go scan("adapter stdout", stdout)
	go scan("adapter stderr", stderr)
	go func() { scanWG.Wait() }()

	var address string
	select {
	case address = <-addresses:
	case err := <-processDone:
		handle.stop()
		if err == nil {
			return nil, fmt.Errorf("Delve exited before publishing its DAP address")
		}
		return nil, fmt.Errorf("Delve exited during startup: %w", err)
	case <-startCtx.Done():
		handle.stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out waiting for Delve DAP server")
	}

	dialer := net.Dialer{Timeout: debugAdapterStartupTimeout}
	transport, err := dialer.DialContext(startCtx, "tcp", address)
	if err != nil {
		handle.stop()
		return nil, fmt.Errorf("connect to Delve DAP server: %w", err)
	}
	handle.transport = transport
	return handle, nil
}

func newDelveDebugBinaryCleanup(directory string, output debugAdapterOutput) func() {
	if strings.TrimSpace(directory) == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return nil
		}
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil
	}
	existing, err := delveDebugBinaries(directory)
	if err != nil {
		return nil
	}
	return func() {
		if err := removeNewDelveDebugBinaries(directory, existing); err != nil && output != nil {
			output("adapter stderr", fmt.Sprintf("Echo could not remove a Delve debug binary: %v\n", err))
		}
	}
}

func delveDebugBinaries(directory string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	binaries := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Type().IsRegular() && isDelveDebugBinaryName(entry.Name()) {
			binaries[entry.Name()] = struct{}{}
		}
	}
	return binaries, nil
}

func removeNewDelveDebugBinaries(directory string, existing map[string]struct{}) error {
	deadline := time.Now().Add(debugBinaryCleanupTimeout)
	var cleanupErr error
	for {
		cleanupErr = nil
		binaries, err := delveDebugBinaries(directory)
		if err != nil {
			return err
		}
		for name := range binaries {
			if _, existed := existing[name]; existed {
				continue
			}
			path := filepath.Join(directory, name)
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", path, err))
			}
		}
		if cleanupErr == nil || time.Now().After(deadline) {
			return cleanupErr
		}
		time.Sleep(debugBinaryCleanupInterval)
	}
}

func isDelveDebugBinaryName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "__debug_bin")
	if !ok {
		return false
	}
	if strings.HasPrefix(suffix, ".exe") {
		suffix = strings.TrimPrefix(suffix, ".exe")
	}
	if suffix == "" {
		return true
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
