package debugger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspaces"
)

type adapterHandle struct {
	transport io.ReadWriteCloser
	stop      func()
}

type stdioTransport struct {
	reader    io.ReadCloser
	writer    io.WriteCloser
	closeOnce sync.Once
	closeFn   func()
}

func (t *stdioTransport) Read(p []byte) (int, error)  { return t.reader.Read(p) }
func (t *stdioTransport) Write(p []byte) (int, error) { return t.writer.Write(p) }
func (t *stdioTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.writer.Close()
		_ = t.reader.Close()
		if t.closeFn != nil {
			t.closeFn()
		}
	})
	return nil
}

func (s *Service) startAdapter(ctx context.Context, workspace workspaces.Workspace, profile debugconfig.AdapterProfile, options debugconfig.ExpandOptions, workingDirectory string, logOutput func(string, string)) (*adapterHandle, error) {
	profile = profile.Normalized()
	transport := profile.Transport
	if transport.Kind == "connect" {
		if workspace.Sandbox.Enabled {
			return s.startSandboxAdapter(ctx, workspace.ID, "", nil, nil, options.WorkspaceFolder, transport, transport.Port, logOutput)
		}
		address := net.JoinHostPort(transport.Host, fmt.Sprint(transport.Port))
		dialer := net.Dialer{Timeout: time.Duration(transport.StartupTimeoutMS) * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, fmt.Errorf("connect debug adapter at %s: %w", address, err)
		}
		return &adapterHandle{transport: connection, stop: func() { _ = connection.Close() }}, nil
	}
	port := 0
	if transport.Kind == "server" {
		if workspace.Sandbox.Enabled {
			options.PreserveDebugAdapterPort = true
		} else {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			port = listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
		}
	}
	options.DebugAdapterPort = port
	commandName, err := debugconfig.ExpandString(profile.Command, options)
	if err != nil {
		return nil, err
	}
	args, err := debugconfig.ExpandStrings(profile.Args, options)
	if err != nil {
		return nil, err
	}
	environment, err := expandEnvironment(profile.Environment, options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(workingDirectory) == "" {
		workingDirectory = options.WorkspaceFolder
	}
	if workspace.Sandbox.Enabled {
		return s.startSandboxAdapter(ctx, workspace.ID, commandName, args, environment, workingDirectory, profile.Transport, port, logOutput)
	}
	return startHostAdapter(ctx, commandName, args, environment, workingDirectory, profile.Transport, port, logOutput)
}

func startHostAdapter(ctx context.Context, name string, args []string, environment map[string]string, workingDirectory string, transport debugconfig.Transport, port int, logOutput func(string, string)) (*adapterHandle, error) {
	command := exec.CommandContext(ctx, name, args...)
	configureOwnedCommand(command)
	command.Dir = workingDirectory
	command.Env = mergeEnvironment(os.Environ(), environment)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start debug adapter %s: %w", name, err)
	}
	stopOnce := sync.Once{}
	stop := func() {
		stopOnce.Do(func() {
			_ = killOwnedCommand(command)
			_, _ = io.Copy(io.Discard, stdout)
			_, _ = io.Copy(io.Discard, stderr)
			_ = command.Wait()
		})
	}
	go streamLog(stderr, "adapter", logOutput)
	if transport.Kind == "stdio" {
		return &adapterHandle{transport: &stdioTransport{reader: stdout, writer: stdin, closeFn: stop}, stop: stop}, nil
	}
	go streamLog(stdout, "adapter", logOutput)
	address := net.JoinHostPort(defaultHost(transport.Host), fmt.Sprint(port))
	connection, err := dialAdapter(ctx, address, time.Duration(transport.StartupTimeoutMS)*time.Millisecond)
	if err != nil {
		stop()
		return nil, err
	}
	return &adapterHandle{transport: connection, stop: func() { _ = connection.Close(); stop() }}, nil
}

func (s *Service) startSandboxAdapter(ctx context.Context, workspaceID, name string, args []string, environment map[string]string, workingDirectory string, transport debugconfig.Transport, port int, logOutput func(string, string)) (*adapterHandle, error) {
	manager := s.sandboxManager()
	if manager == nil {
		return nil, fmt.Errorf("sandbox debug adapter runtime is unavailable")
	}
	if transport.Kind != "stdio" {
		mode := transport.Kind
		command := []string(nil)
		if mode == "server" {
			command = append([]string{name}, args...)
		}
		process, bridgeErr := manager.OpenDAP(ctx, workspaceID, sandbox.DAPRequest{
			Mode: mode, Command: command, WorkingDirectory: workingDirectory,
			Environment: environmentList(environment), Host: defaultHost(transport.Host),
			Port: port, StartupTimeoutMS: transport.StartupTimeoutMS,
		})
		if bridgeErr != nil {
			return nil, bridgeErr
		}
		stopOnce := sync.Once{}
		stop := func() { stopOnce.Do(func() { _ = process.Kill() }) }
		go streamLog(process.Stderr(), "adapter", logOutput)
		return &adapterHandle{transport: &stdioTransport{reader: process.Stdout(), writer: process.Stdin(), closeFn: stop}, stop: stop}, nil
	}
	process, err := manager.OpenProcess(ctx, workspaceID, sandbox.ExecRequest{Role: "workbench", Command: append([]string{name}, args...), WorkingDirectory: workingDirectory, Environment: environmentList(environment)})
	if err != nil {
		return nil, err
	}
	stopOnce := sync.Once{}
	stop := func() { stopOnce.Do(func() { _ = process.Kill() }) }
	go streamLog(process.Stderr(), "adapter", logOutput)
	return &adapterHandle{transport: &stdioTransport{reader: process.Stdout(), writer: process.Stdin(), closeFn: stop}, stop: stop}, nil
}

func (s *Service) runHook(ctx context.Context, workspace workspaces.Workspace, hook *debugconfig.LifecycleHook, options debugconfig.ExpandOptions, category string, logOutput func(string, string)) error {
	if hook == nil {
		return nil
	}
	name, err := debugconfig.ExpandString(hook.Command, options)
	if err != nil {
		return err
	}
	args, err := debugconfig.ExpandStrings(hook.Args, options)
	if err != nil {
		return err
	}
	cwd := options.WorkspaceFolder
	if hook.Cwd != "" {
		cwd, err = debugconfig.ExpandString(hook.Cwd, options)
		if err != nil {
			return err
		}
	}
	environment, err := expandEnvironment(hook.Environment, options)
	if err != nil {
		return err
	}
	timeout := time.Duration(hook.TimeoutMS) * time.Millisecond
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if workspace.Sandbox.Enabled {
		manager := s.sandboxManager()
		if manager == nil {
			return fmt.Errorf("sandbox lifecycle runtime is unavailable")
		}
		result, err := manager.Execute(hookCtx, workspace.ID, sandbox.ExecRequest{Role: "workbench", Command: append([]string{name}, args...), WorkingDirectory: cwd, Environment: environmentList(environment), OutputLimit: 4 << 20})
		if len(result.Stdout) > 0 {
			logOutput(category, string(result.Stdout))
		}
		if len(result.Stderr) > 0 {
			logOutput(category, string(result.Stderr))
		}
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("%s exited with code %d", name, result.ExitCode)
		}
		return nil
	}
	command := exec.CommandContext(hookCtx, name, args...)
	configureOwnedCommand(command)
	command.Dir = cwd
	command.Env = mergeEnvironment(os.Environ(), environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	var streams sync.WaitGroup
	streams.Add(2)
	go func() { defer streams.Done(); streamLog(stdout, category, logOutput) }()
	go func() { defer streams.Done(); streamLog(stderr, category, logOutput) }()
	waitErr := command.Wait()
	streams.Wait()
	if hookCtx.Err() != nil {
		return hookCtx.Err()
	}
	return waitErr
}

func (s *Service) executionOptions(workspace workspaces.Workspace, currentFile string, inputs map[string]string, selectedText string) (debugconfig.ExpandOptions, error) {
	main := workspace.MainPath
	folders := map[string]string{}
	for _, folder := range workspace.Folders {
		folders[filepath.Base(folder)] = folder
	}
	if workspace.Sandbox.Enabled {
		manager := s.sandboxManager()
		if manager == nil {
			return debugconfig.ExpandOptions{}, fmt.Errorf("sandbox runtime is unavailable")
		}
		var err error
		main, err = manager.HostToGuest(workspace.ID, workspace.MainPath)
		if err != nil {
			return debugconfig.ExpandOptions{}, err
		}
		mapped := map[string]string{}
		for name, folder := range folders {
			guest, mapErr := manager.HostToGuest(workspace.ID, folder)
			if mapErr != nil {
				return debugconfig.ExpandOptions{}, mapErr
			}
			mapped[name] = guest
		}
		folders = mapped
		if currentFile != "" {
			currentFile, err = manager.HostToGuest(workspace.ID, currentFile)
			if err != nil {
				return debugconfig.ExpandOptions{}, err
			}
		}
	}
	return debugconfig.ExpandOptions{WorkspaceFolder: main, WorkspaceFolders: folders, CurrentFile: currentFile, SelectedText: selectedText, Inputs: inputs, SlashPaths: workspace.Sandbox.Enabled}, nil
}

func expandEnvironment(values map[string]string, options debugconfig.ExpandOptions) (map[string]string, error) {
	result := map[string]string{}
	for key, value := range values {
		expanded, err := debugconfig.ExpandString(value, options)
		if err != nil {
			return nil, err
		}
		result[key] = expanded
	}
	return result, nil
}
func mergeEnvironment(base []string, overrides map[string]string) []string {
	result := append([]string(nil), base...)
	positions := map[string]int{}
	for index, value := range result {
		key, _, _ := strings.Cut(value, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		positions[key] = index
	}
	for key, value := range overrides {
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToLower(lookup)
		}
		entry := key + "=" + value
		if index, ok := positions[lookup]; ok {
			result[index] = entry
		} else {
			result = append(result, entry)
		}
	}
	return result
}
func environmentList(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
func defaultHost(value string) string {
	if strings.TrimSpace(value) == "" {
		return "127.0.0.1"
	}
	return strings.TrimSpace(value)
}
func dialAdapter(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		dialer := net.Dialer{Timeout: 250 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			return connection, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("connect spawned debug adapter at %s: %w", address, last)
}
func streamLog(reader io.Reader, category string, notify func(string, string)) {
	if reader == nil {
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		count, err := reader.Read(buffer)
		if count > 0 && notify != nil {
			notify(category, string(buffer[:count]))
		}
		if err != nil {
			return
		}
	}
}
func executableAvailable(command string) bool {
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil || errors.Is(err, exec.ErrDot)
}
