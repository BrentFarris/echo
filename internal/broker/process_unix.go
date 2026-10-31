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
	"syscall"
	"time"
)

func (b *ProcessBroker) run(ctx context.Context, r ProcessRequest, emit func(string)) (ProcessResult, error) {
	timeout := r.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 300 {
		timeout = 300
	}
	var cancel context.CancelFunc
	if r.duplex == nil {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
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
	executable, args := r.Executable, captureArguments(r, scratch)
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
	paths := append([]string{}, r.PathPrepend...)
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
	if r.duplex != nil {
		command.Stdin = r.duplex.input
	}
	output := newProcessOutput(r, emit)
	command.Stdout = processStream{output: output}
	command.Stderr = processStream{output: output, stderr: true}
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
	if r.duplex != nil {
		close(r.duplex.started)
	}
	err = command.Wait()
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	result := output.snapshot()
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
	if err == nil && result.ExitCode == 0 {
		err = captureFileResult(r, scratch, &result)
	}
	return result, err
}
func (b *ProcessBroker) Recover() error { return nil }

func (b *ProcessBroker) CloseSessions(string) error { return nil }
