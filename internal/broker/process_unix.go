//go:build linux || darwin

package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type unixOutput struct {
	mu     sync.Mutex
	result ProcessResult
	emit   func(string)
}

func (o *unixOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(data)
	remaining := (1 << 20) - len(o.result.Output)
	if len(data) > remaining {
		data = data[:remaining]
		o.result.Truncated = true
	}
	if len(data) > 0 {
		o.result.Output += string(data)
		if o.emit != nil {
			o.emit(string(data))
		}
	}
	return n, nil
}

func (b *ProcessBroker) run(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	timeout := r.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 300 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	scratch, err := os.MkdirTemp("", "echo-run-")
	if err != nil {
		return ProcessResult{}, err
	}
	defer os.RemoveAll(scratch)
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return ProcessResult{}, err
	}
	roots, err := CanonicalRoots(r.Roots)
	if err != nil {
		return ProcessResult{}, err
	}
	r.Roots = roots
	private, err := filepath.Abs(filepath.Dir(b.Dir))
	if err != nil {
		return ProcessResult{}, err
	}
	if real, e := filepath.EvalSymlinks(private); e == nil {
		private = real
	}
	for _, root := range roots {
		if Within(private, root) {
			return ProcessResult{}, errors.New("native workspace cannot be inside Echo private state")
		}
	}
	for _, root := range r.Toolchains {
		if Within(private, root) || Within(root, private) {
			return ProcessResult{}, errors.New("native toolchain cannot include Echo private state")
		}
	}
	executable, args := r.Executable, r.Args
	if r.Command != "" {
		executable = "/bin/sh"
		args = []string{"-c", r.Command}
	}
	if executable == "" {
		return ProcessResult{}, errors.New("executable unavailable")
	}
	if len(r.Roots) == 0 {
		r.Internet = true
		r.PrivateNetwork = true
	}
	proxy, err := startEgress(ctx, r.Internet, r.PrivateNetwork, scratch)
	if err != nil {
		return ProcessResult{}, err
	}
	defer proxy.Close()
	command, proxyURL, err := unixSandbox(ctx, r, scratch, private, proxy, executable, args)
	if err != nil {
		return ProcessResult{}, err
	}
	env := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "HOME": scratch, "TMPDIR": scratch, "XDG_CACHE_HOME": scratch, "XDG_CONFIG_HOME": scratch, "LANG": "C.UTF-8", "ECHO_WORKSPACE": r.Directory, "GOCACHE": filepath.Join(scratch, "go-build"), "GOMODCACHE": filepath.Join(scratch, "go-mod"), "npm_config_cache": filepath.Join(scratch, "npm"), "DOTNET_CLI_HOME": scratch, "NUGET_PACKAGES": filepath.Join(scratch, "nuget"), "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_GLOBAL": "/dev/null", "FOSSIL_HOME": scratch, "P4ENVIRO": filepath.Join(scratch, "p4enviro"), "P4TICKETS": filepath.Join(scratch, "p4tickets"), "P4TRUST": filepath.Join(scratch, "p4trust")}
	paths := []string{}
	for _, root := range r.Toolchains {
		paths = append(paths, root, filepath.Join(root, "bin"))
	}
	paths = append(paths, env["PATH"])
	env["PATH"] = strings.Join(paths, ":")
	for key, value := range workspaceGoCaches(r) {
		env[key] = value
	}
	for key, value := range r.Environment {
		env[key] = value
	}
	// Only this broker's filtered proxy is reachable from the sandbox. Inherited
	// proxy variables, agents, credentials, and user configuration are excluded.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		env[key] = proxyURL
	}
	env["ALL_PROXY"] = "socks5h://" + strings.TrimPrefix(proxyURL, "http://")
	env["all_proxy"] = env["ALL_PROXY"]
	env["NO_PROXY"] = ""
	env["no_proxy"] = ""
	command.Env = []string{}
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	sort.Strings(command.Env)
	command.Dir = r.Directory
	command.Stdin = strings.NewReader(r.Input)
	output := &unixOutput{emit: emit}
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
	if err = command.Start(); err != nil {
		return ProcessResult{}, fmt.Errorf("start native containment (no host fallback): %w", err)
	}
	err = command.Wait()
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	output.mu.Lock()
	result := output.result
	output.mu.Unlock()
	if command.ProcessState != nil {
		result.ExitCode = uint32(command.ProcessState.ExitCode())
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return result, nil
	}
	return result, err
}
func (b *ProcessBroker) Recover() error { return nil }
